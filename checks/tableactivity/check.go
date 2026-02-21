// Package tableactivity implements checks for table write activity patterns.
package tableactivity

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

type TableActivityQueries interface {
	TableActivity(context.Context) ([]db.TableActivityRow, error)
}

type checker struct {
	queries TableActivityQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "table-activity",
		Name:        "Table Activity",
		Description: "Analyzes table write activity to identify high-churn tables and HOT update efficiency issues",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TableActivityQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.TableActivity(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryPerformance, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No table activity data available",
		})
		return report, nil
	}

	checkHighChurnTables(rows, report)
	checkLowHOTRatio(rows, report)

	return report, nil
}

// checkHighChurnTables identifies tables with excessive write activity.
func checkHighChurnTables(rows []db.TableActivityRow, report *check.Report) {
	const highChurnThreshold = int64(1_000_000) // 1M writes

	var highChurn []db.TableActivityRow
	for _, row := range rows {
		totalWrites := check.Int8ToInt64(row.NTupIns) + check.Int8ToInt64(row.NTupUpd) + check.Int8ToInt64(row.NTupDel)
		if totalWrites > highChurnThreshold {
			highChurn = append(highChurn, row)
		}
	}

	if len(highChurn) == 0 {
		report.AddFinding(check.Finding{
			ID:       "high-churn-tables",
			Name:     "High Churn Tables",
			Severity: check.SeverityOK,
			Details:  "No tables with excessive write activity (>1M writes) detected",
		})
		return
	}

	headers := []string{"Schema", "Table", "Inserts", "Updates", "Deletes", "Total Writes", "Size"}
	var tableRows []check.TableRow

	for _, row := range highChurn {
		totalWrites := check.Int8ToInt64(row.NTupIns) + check.Int8ToInt64(row.NTupUpd) + check.Int8ToInt64(row.NTupDel)
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.Schemaname.String,
				row.Relname.String,
				check.FormatNumber(check.Int8ToInt64(row.NTupIns)),
				check.FormatNumber(check.Int8ToInt64(row.NTupUpd)),
				check.FormatNumber(check.Int8ToInt64(row.NTupDel)),
				check.FormatNumber(totalWrites),
				check.FormatBytes(check.Int8ToInt64(row.TableSizeBytes)),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "high-churn-tables",
		Name:     "High Churn Tables",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with high write activity (>1M writes)", len(highChurn)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// checkLowHOTRatio identifies tables with poor HOT update efficiency.
func checkLowHOTRatio(rows []db.TableActivityRow, report *check.Report) {
	const (
		minRows     = int64(1_000_000) // Only check tables with >1M rows
		minUpdates  = int64(1000)      // Need meaningful update count
		lowHOTRatio = float64(50)      // Below 50% is concerning
	)

	var lowHOT []db.TableActivityRow
	for _, row := range rows {
		liveTup := check.Int8ToInt64(row.NLiveTup)
		nTupUpd := check.Int8ToInt64(row.NTupUpd)

		if liveTup < minRows || nTupUpd < minUpdates {
			continue
		}

		hotRatio := calculateHOTRatio(row)
		if hotRatio < lowHOTRatio {
			lowHOT = append(lowHOT, row)
		}
	}

	if len(lowHOT) == 0 {
		report.AddFinding(check.Finding{
			ID:       "low-hot-ratio",
			Name:     "HOT Update Efficiency",
			Severity: check.SeverityOK,
			Details:  "All large tables have acceptable HOT update ratio (>50%)",
		})
		return
	}

	headers := []string{"Schema", "Table", "HOT Ratio", "Updates", "HOT Updates", "Live Rows"}
	var tableRows []check.TableRow

	for _, row := range lowHOT {
		hotRatio := calculateHOTRatio(row)
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.Schemaname.String,
				row.Relname.String,
				fmt.Sprintf("%.1f%%", hotRatio),
				check.FormatNumber(check.Int8ToInt64(row.NTupUpd)),
				check.FormatNumber(check.Int8ToInt64(row.NTupHotUpd)),
				check.FormatNumber(check.Int8ToInt64(row.NLiveTup)),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "low-hot-ratio",
		Name:     "HOT Update Efficiency",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d large table(s) with low HOT update ratio (<50%%)", len(lowHOT)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

func calculateHOTRatio(row db.TableActivityRow) float64 {
	nTupUpd := check.Int8ToInt64(row.NTupUpd)
	if nTupUpd == 0 {
		return 100.0 // No updates = perfect ratio
	}
	return float64(check.Int8ToInt64(row.NTupHotUpd)) / float64(nTupUpd) * 100
}
