// Package tablebloat implements checks for PostgreSQL table bloat from dead tuples.
package tablebloat

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type TableBloatQueries interface {
	TableBloat(context.Context) ([]db.TableBloatRow, error)
}

type checker struct {
	queries TableBloatQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryVacuum,
		CheckID:     "table-bloat",
		Name:        "Table Bloat",
		Description: "Identifies tables with high dead tuple percentages indicating vacuum issues",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TableBloatQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.TableBloat(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryVacuum, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
			Details:  "No tables with significant dead tuples found",
		})
		return report, nil
	}

	checkHighDeadTuples(rows, report)
	checkLargeBloatedTables(rows, report)

	return report, nil
}

// maxRowSeverity floors at Warn: these findings only exist once a row warrants attention.
func maxRowSeverity(rows []check.TableRow) check.Severity {
	severity := check.SeverityWarn
	for _, row := range rows {
		if row.Severity > severity {
			severity = row.Severity
		}
	}
	return severity
}

func getDeadTuplePercent(row db.TableBloatRow) float64 {
	if !row.DeadTuplePercent.Valid {
		return 0
	}
	f, _ := row.DeadTuplePercent.Float64Value()
	return f.Float64
}

// checkHighDeadTuples identifies tables with >20% dead tuples (WARN-only: degrades, never stops the DB).
func checkHighDeadTuples(rows []db.TableBloatRow, report *check.Report) {
	var bloated []db.TableBloatRow // >20%

	for _, row := range rows {
		if getDeadTuplePercent(row) >= 20 {
			bloated = append(bloated, row)
		}
	}

	if len(bloated) == 0 {
		report.AddFinding(check.Finding{
			ID:       "high-dead-tuples",
			Name:     "Dead Tuple Percentage",
			Severity: check.SeverityPass,
			Details:  "All tables have acceptable dead tuple percentages (<20%)",
		})
		return
	}

	headers := []string{"Table", "Dead %", "Dead Tuples", "Live Tuples", "Size"}
	tableRows := make([]check.TableRow, 0, len(bloated))

	for _, row := range bloated {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				fmt.Sprintf("%.1f%%", getDeadTuplePercent(row)),
				formatNumber(row.DeadTuples.Int64),
				formatNumber(row.LiveTuples.Int64),
				check.FormatBytes(row.TotalSizeBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "high-dead-tuples",
		Name:     "Dead Tuple Percentage",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with high dead tuple percentage (>20%%)", len(bloated)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// checkLargeBloatedTables identifies large tables with notable bloat.
func checkLargeBloatedTables(rows []db.TableBloatRow, report *check.Report) {
	const oneGB = int64(1024 * 1024 * 1024)
	const tenGB = int64(10 * 1024 * 1024 * 1024)

	var critical []db.TableBloatRow // >10GB with >20%
	var warning []db.TableBloatRow  // >1GB with >10%

	for _, row := range rows {
		size := row.TotalSizeBytes.Int64
		pct := getDeadTuplePercent(row)

		if size >= tenGB && pct >= 20 {
			critical = append(critical, row)
		} else if size >= oneGB && pct >= 10 {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-bloated-tables",
			Name:     "Large Table Bloat",
			Severity: check.SeverityPass,
			Details:  "No large tables with significant bloat detected",
		})
		return
	}

	headers := []string{"Table", "Size", "Dead %", "Wasted Space (est)"}
	var tableRows []check.TableRow

	for _, row := range critical {
		pct := getDeadTuplePercent(row)
		wastedBytes := int64(float64(row.TotalSizeBytes.Int64) * pct / 100)
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				check.FormatBytes(row.TotalSizeBytes.Int64),
				fmt.Sprintf("%.1f%%", pct),
				check.FormatBytes(wastedBytes),
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		pct := getDeadTuplePercent(row)
		wastedBytes := int64(float64(row.TotalSizeBytes.Int64) * pct / 100)
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				check.FormatBytes(row.TotalSizeBytes.Int64),
				fmt.Sprintf("%.1f%%", pct),
				check.FormatBytes(wastedBytes),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "large-bloated-tables",
		Name:     "Large Table Bloat",
		Severity: maxRowSeverity(tableRows),
		Details:  fmt.Sprintf("Found %d large table(s) with significant bloat, wasting disk space", len(critical)+len(warning)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

func formatNumber(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
