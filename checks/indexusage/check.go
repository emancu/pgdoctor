// Package indexusage implements checks for identifying unused and inefficient indexes.
package indexusage

import (
	"context"
	_ "embed"
	"fmt"
	"sort"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	unusedSizeThresholdMB  = 10
	lowUsageScanThreshold  = 1000
	lowUsageWriteThreshold = 10000

	// replicaCaveat warns that idx_scan is a per-instance, reset-prone counter:
	// an index idle on one node may be hot on another, so a drop decision must be
	// validated across every replica and against the stats-reset timestamp.
	replicaCaveat = "idx_scan is per-instance and resets on failover/restart — " +
		"verify usage on EVERY replica (and check pg_stat_database.stats_reset) before dropping."
)

type IndexUsageQueries interface {
	IndexUsageStats(context.Context) ([]db.IndexUsageStatsRow, error)
}

type checker struct {
	queries IndexUsageQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryIndexes,
		CheckID:     "index-usage",
		Name:        "Index Usage",
		Description: "Identifies unused and inefficient indexes based on usage statistics",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries IndexUsageQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.IndexUsageStats(ctx)
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

	checkUnusedIndexes(rows, report)
	checkLowUsageIndexes(rows, report)

	return report, nil
}

func indexSizeMB(row db.IndexUsageStatsRow) float64 {
	return float64(row.IndexSizeBytes.Int64) / (1024 * 1024)
}

func checkUnusedIndexes(rows []db.IndexUsageStatsRow, report *check.Report) {
	var matches []db.IndexUsageStatsRow

	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}

		if row.IdxScan.Int64 == 0 && indexSizeMB(row) > unusedSizeThresholdMB {
			matches = append(matches, row)
		}
	}

	if len(matches) == 0 {
		report.AddFinding(check.Finding{
			ID:       "unused-indexes",
			Name:     "Unused Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].IndexSizeBytes.Int64 > matches[j].IndexSizeBytes.Int64
	})

	tableRows := make([]check.TableRow, 0, len(matches))
	for _, row := range matches {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.IndexName.String,
				check.FormatBytes(row.IndexSizeBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "unused-indexes",
		Name:     "Unused Indexes",
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf("Found %d unused indexes (0 scans, size > %d MB). %s",
			len(tableRows), unusedSizeThresholdMB, replicaCaveat),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Size"},
			Rows:    tableRows,
		},
	})
}

func checkLowUsageIndexes(rows []db.IndexUsageStatsRow, report *check.Report) {
	var matches []db.IndexUsageStatsRow

	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}

		if row.IdxScan.Int64 > 0 && row.IdxScan.Int64 < lowUsageScanThreshold && row.TableWrites.Int64 > lowUsageWriteThreshold {
			matches = append(matches, row)
		}
	}

	if len(matches) == 0 {
		report.AddFinding(check.Finding{
			ID:       "low-usage-indexes",
			Name:     "Low Usage Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].IndexSizeBytes.Int64 > matches[j].IndexSizeBytes.Int64
	})

	tableRows := make([]check.TableRow, 0, len(matches))
	for _, row := range matches {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.IndexName.String,
				check.FormatBytes(row.IndexSizeBytes.Int64),
				check.FormatNumber(row.IdxScan.Int64),
				check.FormatNumber(row.TableWrites.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "low-usage-indexes",
		Name:     "Low Usage Indexes",
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf("Found %d indexes with low read usage but high write cost. %s",
			len(tableRows), replicaCaveat),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Size", "Scans", "Table Writes"},
			Rows:    tableRows,
		},
	})
}
