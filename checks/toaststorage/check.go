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
	toastHeavyPercent = 50
	toastHeavyBytes   = int64(10 * check.GiB)

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
	checkToastHeavy(rows, report)
	checkToastBloat(rows, report)
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

// checkToastHeavy lists tables where TOAST dominates by ratio or absolute size; informational only.
func checkToastHeavy(rows []db.ToastStorageRow, report *check.Report) {
	var heavy []db.ToastStorageRow

	for _, row := range rows {
		if getToastPercent(row) >= toastHeavyPercent || row.ToastSize.Int64 >= toastHeavyBytes {
			heavy = append(heavy, row)
		}
	}

	if len(heavy) == 0 {
		report.AddFinding(check.Finding{
			ID:       "toast-ratio",
			Name:     "TOAST-Heavy Tables",
			Severity: check.SeverityPass,
			Details:  "No TOAST-heavy tables (>50% ratio or >10GB)",
		})
		return
	}

	sort.SliceStable(heavy, func(i, j int) bool { return heavy[i].ToastSize.Int64 > heavy[j].ToastSize.Int64 })

	headers := []string{"TOAST Size", "TOAST %", "Main Size", "Total", "Table"}
	var tableRows []check.TableRow

	for _, row := range heavy {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				check.FormatBytes(row.ToastSize.Int64),
				fmt.Sprintf("%.1f%%", getToastPercent(row)),
				check.FormatBytes(row.MainTableSize.Int64),
				check.FormatBytes(row.TotalSize.Int64),
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
			},
			Severity: check.SeverityInfo,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "toast-ratio",
		Name:     "TOAST-Heavy Tables",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("Found %d TOAST-heavy table(s) (>50%% ratio or >10GB)", len(heavy)),
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
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("default_toast_compression is %s; set it to lz4 in the parameter group (new writes only, no DDL)", current),
	})
}
