// Package tableseqscans implements checks for identifying tables with excessive sequential scan activity.
package tableseqscans

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
			Severity: check.SeverityOK,
		})
		return report, nil
	}

	checkHighSeqScans(rows, report)

	return report, nil
}

func checkHighSeqScans(rows []db.HighSeqScanTablesRow, report *check.Report) {
	var failTables []string
	var warnTables []string
	failCount := 0
	warnCount := 0

	for _, row := range rows {
		if row.IndexCount.Int64 == 0 {
			continue
		}

		var ratio float64
		if row.SeqToIdxRatio.Valid {
			r, _ := row.SeqToIdxRatio.Float64Value()
			ratio = r.Float64
		} else {
			ratio = 999999
		}

		sizeMB := float64(row.TableSizeBytes.Int64) / (1024 * 1024)

		if row.EstimatedRows.Int64 >= failRowThreshold && ratio >= failRatioThreshold {
			failCount++
			if len(failTables) < 10 {
				failTables = append(failTables, fmt.Sprintf("%s (seq: %d, idx: %d, ratio: %.1f, rows: %d, size: %.1f MB)",
					row.TableName.String, row.SeqScan.Int64, row.IdxScan.Int64, ratio, row.EstimatedRows.Int64, sizeMB))
			}
		} else if row.EstimatedRows.Int64 >= warnRowThreshold && ratio >= warnRatioThreshold {
			warnCount++
			if len(warnTables) < 10 {
				warnTables = append(warnTables, fmt.Sprintf("%s (seq: %d, idx: %d, ratio: %.1f, rows: %d, size: %.1f MB)",
					row.TableName.String, row.SeqScan.Int64, row.IdxScan.Int64, ratio, row.EstimatedRows.Int64, sizeMB))
			}
		}
	}

	if failCount > 0 {
		details := fmt.Sprintf("Found %d tables with very high sequential scan ratios:\n%s",
			failCount,
			strings.Join(failTables, "\n"),
		)
		if failCount > len(failTables) {
			details += fmt.Sprintf("\n... and %d more", failCount-len(failTables))
		}

		report.AddFinding(check.Finding{
			ID:       "high-seq-scans",
			Name:     "High Sequential Scans",
			Severity: check.SeverityFail,
			Details:  details,
		})
	}

	if warnCount > 0 {
		details := fmt.Sprintf("Found %d tables with elevated sequential scan ratios:\n%s",
			warnCount,
			strings.Join(warnTables, "\n"),
		)
		if warnCount > len(warnTables) {
			details += fmt.Sprintf("\n... and %d more", warnCount-len(warnTables))
		}

		report.AddFinding(check.Finding{
			ID:       "moderate-seq-scans",
			Name:     "Moderate Sequential Scans",
			Severity: check.SeverityWarn,
			Details:  details,
		})
	}

	if failCount == 0 && warnCount == 0 {
		report.AddFinding(check.Finding{
			ID:       "high-seq-scans",
			Name:     "High Sequential Scans",
			Severity: check.SeverityOK,
		})
	}
}
