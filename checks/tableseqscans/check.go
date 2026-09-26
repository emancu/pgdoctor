// Package tableseqscans implements checks for identifying tables with excessive sequential scan activity.
package tableseqscans

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

const (
	warnRowThreshold   = 10000
	warnRatioThreshold = 10.0
	failRowThreshold   = 50000
	failRatioThreshold = 50.0
)

type TableSeqScansQueries interface {
	HighSeqScanTables(context.Context) ([]db.HighSeqScanTablesRow, error)
}

type checker struct {
	queries TableSeqScansQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "table-seq-scans",
		Name:        "Table Sequential Scans",
		Description: "Identifies tables with excessive sequential scans that may benefit from indexes",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TableSeqScansQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.HighSeqScanTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
		})
		return report, nil
	}

	checkHighSeqScans(rows, report)

	return report, nil
}

func checkHighSeqScans(rows []db.HighSeqScanTablesRow, report *check.Report) {
	var failRows []check.TableRow
	var warnRows []check.TableRow

	for _, row := range rows {
		if row.IndexCount.Int64 == 0 {
			continue
		}

		var ratio float64
		ratioCell := "-"
		if row.SeqToIdxRatio.Valid {
			r, _ := row.SeqToIdxRatio.Float64Value()
			ratio = r.Float64
			ratioCell = fmt.Sprintf("%.1f", ratio)
		} else {
			ratio = 999999
		}

		cells := []string{
			row.TableName.String,
			check.FormatNumber(row.SeqScan.Int64),
			check.FormatNumber(row.IdxScan.Int64),
			ratioCell,
			check.FormatNumber(row.EstimatedRows.Int64),
			check.FormatBytes(row.TableSizeBytes.Int64),
		}

		if row.EstimatedRows.Int64 >= failRowThreshold && ratio >= failRatioThreshold {
			failRows = append(failRows, check.TableRow{Cells: cells, Severity: check.SeverityFail})
		} else if row.EstimatedRows.Int64 >= warnRowThreshold && ratio >= warnRatioThreshold {
			warnRows = append(warnRows, check.TableRow{Cells: cells, Severity: check.SeverityWarn})
		}
	}

	headers := []string{"Table", "Seq Scans", "Idx Scans", "Ratio", "Rows", "Size"}

	if len(failRows) > 0 {
		report.AddFinding(check.Finding{
			ID:       "high-seq-scans",
			Name:     "High Sequential Scans",
			Severity: check.SeverityFail,
			Details:  fmt.Sprintf("Found %d tables with very high sequential scan ratios", len(failRows)),
			Table:    &check.Table{Headers: headers, Rows: failRows},
		})
	}

	if len(warnRows) > 0 {
		report.AddFinding(check.Finding{
			ID:       "moderate-seq-scans",
			Name:     "Moderate Sequential Scans",
			Severity: check.SeverityWarn,
			Details:  fmt.Sprintf("Found %d tables with elevated sequential scan ratios", len(warnRows)),
			Table:    &check.Table{Headers: headers, Rows: warnRows},
		})
	}

	if len(failRows) == 0 && len(warnRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "high-seq-scans",
			Name:     "High Sequential Scans",
			Severity: check.SeverityPass,
		})
	}
}
