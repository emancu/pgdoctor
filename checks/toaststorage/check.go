// Package toaststorage implements checks for PostgreSQL TOAST storage analysis.
package toaststorage

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

// ToastStorageQueries defines the database queries needed by this check.
type ToastStorageQueries interface {
	ToastStorage(context.Context) ([]db.ToastStorageRow, error)
	ToastDefaultCompression(context.Context) (string, error)
}

type checker struct {
	queries ToastStorageQueries
}

const (
	toastRatioWarnPercent = 50

	toastSizeFailBytes = int64(100 * check.GiB) // 100GB
	toastSizeWarnBytes = int64(10 * check.GiB)  // 10GB

	wideColumnJSONBThreshold = 5000  // 5KB
	wideColumnTextThreshold  = 10000 // 10KB

	compressionToastFloorBytes = int64(check.GiB) // itemize only tables with TOAST > 1GiB

	compressionPglz = "pglz"
	compressionLz4  = "lz4"
)

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategorySchema,
		CheckID:     "toast-storage",
		Name:        "TOAST Storage Analysis",
		Description: "Analyzes TOAST storage usage for large value storage optimization",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries ToastStorageQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.ToastStorage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze TOAST storage: %w", err)
	}

	defaultCompression := c.fetchDefaultCompression(ctx)
	checkCompressionDefaultFinding(ctx, defaultCompression, report)

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
			Details:  "No tables with significant TOAST storage found",
		})
		return report, nil
	}

	// Run all subchecks
	checkExcessiveToastRatio(rows, report)
	checkLargeToastTables(rows, report)
	checkToastBloat(rows, report)
	checkWideColumns(rows, report)
	checkCompressionAlgorithm(ctx, rows, defaultCompression, report)

	return report, nil
}

// fetchDefaultCompression reads the cluster GUC, defaulting to pglz when unavailable.
func (c *checker) fetchDefaultCompression(ctx context.Context) string {
	guc, err := c.queries.ToastDefaultCompression(ctx)
	if err != nil || guc == "" {
		return compressionPglz
	}
	return guc
}

// pgMajor extracts the PostgreSQL major version from context metadata, defaulting to 14.
func pgMajor(ctx context.Context) int {
	meta := check.InstanceMetadataFromContext(ctx)
	major := 14
	if meta != nil && meta.EngineVersion != "" {
		_, _ = fmt.Sscanf(meta.EngineVersion, "%d", &major)
	}
	return major
}

// getToastPercent extracts TOAST percentage from pgtype.Numeric.
func getToastPercent(row db.ToastStorageRow) float64 {
	if !row.ToastPercent.Valid {
		return 0
	}
	f, _ := row.ToastPercent.Float64Value()
	return f.Float64
}

// checkExcessiveToastRatio identifies tables where TOAST dominates storage.
func checkExcessiveToastRatio(rows []db.ToastStorageRow, report *check.Report) {
	var toastHeavy []db.ToastStorageRow

	for _, row := range rows {
		if getToastPercent(row) >= toastRatioWarnPercent {
			toastHeavy = append(toastHeavy, row)
		}
	}

	if len(toastHeavy) == 0 {
		report.AddFinding(check.Finding{
			ID:       "toast-ratio",
			Name:     "TOAST Storage Ratio",
			Severity: check.SeverityPass,
			Details:  "All tables have acceptable TOAST ratios (<50%)",
		})
		return
	}

	headers := []string{"Table", "TOAST %", "TOAST Size", "Main Size", "Total"}
	var tableRows []check.TableRow

	for _, row := range toastHeavy {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				fmt.Sprintf("%.1f%%", getToastPercent(row)),
				check.FormatBytes(row.ToastSize.Int64),
				check.FormatBytes(row.MainTableSize.Int64),
				check.FormatBytes(row.TotalSize.Int64),
			},
			Severity: check.SeverityInfo,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "toast-ratio",
		Name:     "TOAST Storage Ratio",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("Found %d table(s) with high TOAST storage ratio (>50%%)", len(toastHeavy)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// checkLargeToastTables identifies tables with very large TOAST storage.
func checkLargeToastTables(rows []db.ToastStorageRow, report *check.Report) {
	var critical []db.ToastStorageRow
	var warning []db.ToastStorageRow

	for _, row := range rows {
		toastSize := row.ToastSize.Int64
		if toastSize >= toastSizeFailBytes {
			critical = append(critical, row)
		} else if toastSize >= toastSizeWarnBytes {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-toast",
			Name:     "Large TOAST Tables",
			Severity: check.SeverityPass,
			Details:  "No tables with very large TOAST storage (>10GB)",
		})
		return
	}

	headers := []string{"Table", "TOAST Size", "TOAST %", "Wide Columns"}
	var tableRows []check.TableRow

	for _, row := range critical {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				check.FormatBytes(row.ToastSize.Int64),
				fmt.Sprintf("%.1f%%", getToastPercent(row)),
				formatWideColumns(row.WideColumns),
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				check.FormatBytes(row.ToastSize.Int64),
				fmt.Sprintf("%.1f%%", getToastPercent(row)),
				formatWideColumns(row.WideColumns),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "large-toast",
		Name:     "Large TOAST Tables",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with very large TOAST storage", len(critical)+len(warning)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// checkToastBloat identifies TOAST tables with high dead tuple ratio.
func checkToastBloat(rows []db.ToastStorageRow, report *check.Report) {
	const bloatFailPercent = 50 // >50% dead tuples is critical
	const bloatWarnPercent = 30 // >30% dead tuples needs attention

	var critical []db.ToastStorageRow
	var warning []db.ToastStorageRow

	for _, row := range rows {
		if !row.ToastLiveTuples.Valid || !row.ToastDeadTuples.Valid {
			continue
		}

		totalTuples := row.ToastLiveTuples.Int64 + row.ToastDeadTuples.Int64
		if totalTuples == 0 {
			continue
		}

		bloatPercent := (float64(row.ToastDeadTuples.Int64) / float64(totalTuples)) * 100

		if bloatPercent >= bloatFailPercent {
			critical = append(critical, row)
		} else if bloatPercent >= bloatWarnPercent {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "toast-bloat",
			Name:     "TOAST Table Bloat",
			Severity: check.SeverityPass,
			Details:  "No TOAST tables with excessive dead tuples detected",
		})
		return
	}

	headers := []string{"Table", "TOAST Size", "Dead Tuples %", "Dead Tuples", "Live Tuples"}
	var tableRows []check.TableRow

	for _, row := range critical {
		totalTuples := row.ToastLiveTuples.Int64 + row.ToastDeadTuples.Int64
		bloatPercent := (float64(row.ToastDeadTuples.Int64) / float64(totalTuples)) * 100

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				check.FormatBytes(row.ToastSize.Int64),
				fmt.Sprintf("%.1f%%", bloatPercent),
				check.FormatNumber(row.ToastDeadTuples.Int64),
				check.FormatNumber(row.ToastLiveTuples.Int64),
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		totalTuples := row.ToastLiveTuples.Int64 + row.ToastDeadTuples.Int64
		bloatPercent := (float64(row.ToastDeadTuples.Int64) / float64(totalTuples)) * 100

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				check.FormatBytes(row.ToastSize.Int64),
				fmt.Sprintf("%.1f%%", bloatPercent),
				check.FormatNumber(row.ToastDeadTuples.Int64),
				check.FormatNumber(row.ToastLiveTuples.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "toast-bloat",
		Name:     "TOAST Table Bloat",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d TOAST table(s) with excessive dead tuples", len(critical)+len(warning)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// checkWideColumns identifies tables with columns likely causing TOAST usage.
func checkWideColumns(rows []db.ToastStorageRow, report *check.Report) {
	type wideColumnInfo struct {
		tableName  string
		columnName string
		avgWidth   int
		columnType string
		toastSize  int64
	}

	var jsonbColumns []wideColumnInfo
	var largeTextColumns []wideColumnInfo

	for _, row := range rows {
		tableName := fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String)
		for _, colInfo := range row.WideColumns {
			parts := strings.Split(colInfo, ":")
			if len(parts) != 3 {
				continue
			}

			colName := parts[0]
			avgWidth := 0
			_, _ = fmt.Sscanf(parts[1], "%d", &avgWidth)
			colType := parts[2]

			info := wideColumnInfo{
				tableName:  tableName,
				columnName: colName,
				avgWidth:   avgWidth,
				columnType: colType,
				toastSize:  row.ToastSize.Int64,
			}

			if colType == "jsonb" && avgWidth > wideColumnJSONBThreshold {
				jsonbColumns = append(jsonbColumns, info)
			} else if avgWidth > wideColumnTextThreshold {
				largeTextColumns = append(largeTextColumns, info)
			}
		}
	}

	if len(jsonbColumns) == 0 && len(largeTextColumns) == 0 {
		report.AddFinding(check.Finding{
			ID:       "wide-columns",
			Name:     "Wide Column Analysis",
			Severity: check.SeverityPass,
			Details:  "No columns with excessive average width detected",
		})
		return
	}

	headers := []string{"Table", "Column", "Avg Width", "Type", "TOAST Size"}
	var tableRows []check.TableRow

	// JSONB columns first (often the biggest offenders)
	for _, col := range jsonbColumns {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				col.tableName,
				col.columnName,
				check.FormatBytes(int64(col.avgWidth)),
				col.columnType,
				check.FormatBytes(col.toastSize),
			},
			Severity: check.SeverityWarn,
		})
	}

	for _, col := range largeTextColumns {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				col.tableName,
				col.columnName,
				check.FormatBytes(int64(col.avgWidth)),
				col.columnType,
				check.FormatBytes(col.toastSize),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "wide-columns",
		Name:     "Wide Column Analysis",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d JSONB and %d text columns with large average widths", len(jsonbColumns), len(largeTextColumns)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// effectiveCompression resolves a column's on-disk method: explicit wins, unset falls back to the GUC.
func effectiveCompression(algo string, defaultIsLz4 bool) string {
	switch algo {
	case compressionLz4:
		return compressionLz4
	case compressionPglz:
		return compressionPglz
	default:
		if defaultIsLz4 {
			return compressionLz4
		}
		return compressionPglz
	}
}

// checkCompressionAlgorithm counts columns effectively on pglz; itemization lives in Debug.
func checkCompressionAlgorithm(ctx context.Context, rows []db.ToastStorageRow, defaultCompression string, report *check.Report) {
	if pgMajor(ctx) < 14 {
		return
	}

	defaultIsLz4 := defaultCompression == compressionLz4

	pglzColumns := 0
	tablesWithPglz := map[string]struct{}{}

	for _, row := range rows {
		tableName := fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String)

		for _, compInfo := range row.ColumnCompressionInfo {
			parts := strings.Split(compInfo, ":")
			if len(parts) != 4 {
				continue
			}

			if effectiveCompression(parts[1], defaultIsLz4) != compressionPglz {
				continue
			}

			pglzColumns++
			tablesWithPglz[tableName] = struct{}{}
		}
	}

	debug := bigToastCompressionDebug(rows, defaultIsLz4)

	if pglzColumns == 0 {
		report.AddFinding(check.Finding{
			ID:       "compression-algorithm",
			Name:     "TOAST Compression Algorithm",
			Severity: check.SeverityPass,
			Details:  "All columns are using optimal compression settings (LZ4 or appropriate strategy)",
			Debug:    debug,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "compression-algorithm",
		Name:     "TOAST Compression Algorithm",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("%d column(s) on %d table(s) use pglz compression", pglzColumns, len(tablesWithPglz)),
		Debug:    debug,
	})
}

// bigToastCompressionDebug lists >1GiB columns with their effective method, keeping pglz residue visible after a flip.
func bigToastCompressionDebug(rows []db.ToastStorageRow, defaultIsLz4 bool) string {
	type entry struct {
		toastSize int64
		effective string
		name      string
	}

	var entries []entry
	for _, row := range rows {
		if row.ToastSize.Int64 <= compressionToastFloorBytes {
			continue
		}
		for _, compInfo := range row.ColumnCompressionInfo {
			parts := strings.Split(compInfo, ":")
			if len(parts) != 4 {
				continue
			}
			effective := effectiveCompression(parts[1], defaultIsLz4)
			if parts[2] == "EXTERNAL" {
				effective = "external"
			}
			entries = append(entries, entry{
				toastSize: row.ToastSize.Int64,
				effective: effective,
				name:      fmt.Sprintf("%s.%s.%s", row.SchemaName.String, row.TableName.String, parts[0]),
			})
		}
	}

	if len(entries) == 0 {
		return ""
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].toastSize > entries[j].toastSize })

	var b strings.Builder
	b.WriteString("Big-TOAST columns (>1GiB, effective compression for new writes):")
	for _, e := range entries {
		fmt.Fprintf(&b, "\n#  %-8s effective %-4s  %s", check.FormatBytes(e.toastSize), e.effective, e.name)
	}
	return b.String()
}

// checkCompressionDefault flags a cluster default_toast_compression that is not lz4.
func checkCompressionDefaultFinding(ctx context.Context, current string, report *check.Report) {
	if pgMajor(ctx) < 14 {
		return
	}

	if current == compressionLz4 {
		report.AddFinding(check.Finding{
			ID:       "compression-default",
			Name:     "Default TOAST Compression",
			Severity: check.SeverityPass,
			Details:  "default_toast_compression is lz4",
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "compression-default",
		Name:     "Default TOAST Compression",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("default_toast_compression is %s; unset columns compress new writes with %s — lz4 is strictly better here", current, current),
	})
}

// Helper functions

func formatWideColumns(cols []string) string {
	if len(cols) == 0 {
		return "-"
	}

	// Extract just column names
	var names []string
	for _, col := range cols {
		parts := strings.Split(col, ":")
		if len(parts) >= 1 {
			names = append(names, parts[0])
		}
		if len(names) >= 3 {
			break
		}
	}

	result := strings.Join(names, ", ")
	if len(cols) > 3 {
		result += fmt.Sprintf(" (+%d more)", len(cols)-3)
	}
	return result
}
