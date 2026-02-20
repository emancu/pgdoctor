// Package toaststorage implements checks for PostgreSQL TOAST storage analysis.
package toaststorage

import (
	"context"
	_ "embed"
	"fmt"
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
}

type checker struct {
	queries ToastStorageQueries
}

const (
	toastRatioFailPercent = 80
	toastRatioWarnPercent = 50

	toastSizeFailBytes = int64(100 * check.GiB) // 100GB
	toastSizeWarnBytes = int64(10 * check.GiB)  // 10GB

	wideColumnJSONBThreshold = 5000  // 5KB
	wideColumnTextThreshold  = 10000 // 10KB
)

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategorySchema,
		CheckID:     "toast-storage",
		Name:        "TOAST Storage Analysis",
		Description: "Analyzes TOAST storage usage for large value storage optimization",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries ToastStorageQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.ToastStorage(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze TOAST storage: %w", err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No tables with significant TOAST storage found",
		})
		return report, nil
	}

	// Run all subchecks
	checkExcessiveToastRatio(rows, report)
	checkLargeToastTables(rows, report)
	checkToastBloat(rows, report)
	checkWideColumns(rows, report)
	checkCompressionAlgorithm(ctx, rows, report)

	return report, nil
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
	var critical []db.ToastStorageRow
	var warning []db.ToastStorageRow

	for _, row := range rows {
		pct := getToastPercent(row)
		if pct >= toastRatioFailPercent {
			critical = append(critical, row)
		} else if pct >= toastRatioWarnPercent {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "toast-ratio",
			Name:     "TOAST Storage Ratio",
			Severity: check.SeverityOK,
			Details:  "All tables have acceptable TOAST ratios (<50%)",
		})
		return
	}

	headers := []string{"Table", "TOAST %", "TOAST Size", "Main Size", "Total"}
	var tableRows []check.TableRow

	for _, row := range critical {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				fmt.Sprintf("%.1f%%", getToastPercent(row)),
				check.FormatBytes(row.ToastSize.Int64),
				check.FormatBytes(row.MainTableSize.Int64),
				check.FormatBytes(row.TotalSize.Int64),
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String),
				fmt.Sprintf("%.1f%%", getToastPercent(row)),
				check.FormatBytes(row.ToastSize.Int64),
				check.FormatBytes(row.MainTableSize.Int64),
				check.FormatBytes(row.TotalSize.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "toast-ratio",
		Name:     "TOAST Storage Ratio",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with high TOAST storage ratio (>50%%)", len(critical)+len(warning)),
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
			Severity: check.SeverityOK,
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
			Severity: check.SeverityOK,
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
			Severity: check.SeverityOK,
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

// checkCompressionAlgorithm identifies columns using suboptimal compression (pglz instead of lz4).
func checkCompressionAlgorithm(ctx context.Context, rows []db.ToastStorageRow, report *check.Report) {
	// Check PostgreSQL version - LZ4 compression is only available in PG14+
	meta := check.InstanceMetadataFromContext(ctx)
	pgVersion := 14 // Default assumption
	if meta != nil && meta.EngineVersion != "" {
		// Parse major version from "15.4" or "17.0"
		_, _ = fmt.Sscanf(meta.EngineVersion, "%d", &pgVersion)
	}

	// If PG < 14, skip this check
	if pgVersion < 14 {
		return
	}

	type compressionIssue struct {
		tableName          string
		columnName         string
		currentCompression string
		storageStrategy    string
		columnType         string
		recommendedAction  string
	}

	var suboptimalColumns []compressionIssue

	for _, row := range rows {
		tableName := fmt.Sprintf("%s.%s", row.SchemaName.String, row.TableName.String)

		for _, compInfo := range row.ColumnCompressionInfo {
			parts := strings.Split(compInfo, ":")
			if len(parts) != 4 {
				continue
			}

			colName := parts[0]
			compressionAlgo := parts[1]
			storageStrategy := parts[2]
			colType := parts[3]

			// Flag columns using default (pglz) or explicitly set to pglz
			// LZ4 is better for jsonb, text, and most use cases
			if compressionAlgo == "default" || compressionAlgo == "pglz" {
				action := "SET COMPRESSION lz4"
				if colType == "bytea" && storageStrategy == "EXTENDED" {
					// For binary data, consider EXTERNAL (no compression)
					action = "Consider SET STORAGE EXTERNAL (if pre-compressed data)"
				}

				suboptimalColumns = append(suboptimalColumns, compressionIssue{
					tableName:          tableName,
					columnName:         colName,
					currentCompression: compressionAlgo,
					storageStrategy:    storageStrategy,
					columnType:         colType,
					recommendedAction:  action,
				})
			}
		}
	}

	if len(suboptimalColumns) == 0 {
		report.AddFinding(check.Finding{
			ID:       "compression-algorithm",
			Name:     "TOAST Compression Algorithm",
			Severity: check.SeverityOK,
			Details:  "All columns are using optimal compression settings (LZ4 or appropriate strategy)",
		})
		return
	}

	headers := []string{"Table", "Column", "Type", "Current", "Storage", "Recommendation"}
	var tableRows []check.TableRow

	for _, issue := range suboptimalColumns {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				issue.tableName,
				issue.columnName,
				issue.columnType,
				issue.currentCompression,
				issue.storageStrategy,
				issue.recommendedAction,
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "compression-algorithm",
		Name:     "TOAST Compression Algorithm",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d column(s) using suboptimal compression (pglz instead of lz4)", len(suboptimalColumns)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
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
