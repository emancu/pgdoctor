// Package indexusage implements checks for identifying unused and inefficient indexes.
package indexusage

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	unusedSizeFloorBytes   = 500 * check.MiB
	lowUsageSizeFloorBytes = 500 * check.MiB
	lowUsageWriteThreshold = 10000
	lowUsageMinWindowDays  = 30
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
			Severity: check.SeverityPass,
		})
		return report, nil
	}

	statsReset := rows[0].StatsReset
	checkUnusedIndexes(rows, statsReset, report)
	checkLowUsageIndexes(rows, statsReset, report)

	return report, nil
}

func checkUnusedIndexes(rows []db.IndexUsageStatsRow, statsReset pgtype.Timestamptz, report *check.Report) {
	var unused []db.IndexUsageStatsRow
	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}
		if row.IdxScan.Int64 == 0 && row.IndexSizeBytes.Int64 >= unusedSizeFloorBytes {
			unused = append(unused, row)
		}
	}

	if len(unused) == 0 {
		report.AddFinding(check.Finding{
			ID:       "unused-indexes",
			Name:     "Unused Indexes",
			Severity: check.SeverityPass,
		})
		return
	}

	tableRows := make([]check.TableRow, 0, len(unused))
	for _, row := range unused {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.IndexName.String,
				check.FormatBytes(row.IndexSizeBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	since := ""
	if statsReset.Valid {
		since = fmt.Sprintf(" since %s", statsReset.Time.Format("2006-01-02"))
	}

	report.AddFinding(check.Finding{
		ID:       "unused-indexes",
		Name:     "Unused Indexes",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d unused indexes (0 scans%s, >500MB)", len(unused), since),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Size"},
			Rows:    tableRows,
		},
	})
}

func checkLowUsageIndexes(rows []db.IndexUsageStatsRow, statsReset pgtype.Timestamptz, report *check.Report) {
	windowKnown := statsReset.Valid
	windowDays := 0
	if windowKnown {
		windowDays = int(time.Since(statsReset.Time).Hours() / 24)
	}

	// A NULL stats_reset means counters run since creation: an old window that
	// trivially clears the age gate and the read-rate gate.
	if windowKnown && windowDays < lowUsageMinWindowDays {
		reportLowUsage(nil, report)
		return
	}

	var lowUsage []db.IndexUsageStatsRow
	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}
		// Zero-scan indexes belong to unused-indexes; low-usage covers 1..low-rate.
		if row.IdxScan.Int64 == 0 {
			continue
		}
		if row.TableWrites.Int64 < lowUsageWriteThreshold {
			continue
		}
		if row.IndexSizeBytes.Int64 < lowUsageSizeFloorBytes {
			continue
		}
		if windowKnown && row.IdxScan.Int64*7 >= int64(windowDays) {
			continue
		}
		lowUsage = append(lowUsage, row)
	}

	reportLowUsage(lowUsage, report)
}

func reportLowUsage(lowUsage []db.IndexUsageStatsRow, report *check.Report) {
	if len(lowUsage) == 0 {
		report.AddFinding(check.Finding{
			ID:       "low-usage-indexes",
			Name:     "Low Usage Indexes",
			Severity: check.SeverityPass,
		})
		return
	}

	tableRows := make([]check.TableRow, 0, len(lowUsage))
	for _, row := range lowUsage {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.IndexName.String,
				check.FormatBytes(row.IndexSizeBytes.Int64),
				check.FormatNumber(row.IdxScan.Int64),
				check.FormatNumber(row.TableWrites.Int64),
			},
			Severity: check.SeverityInfo,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "low-usage-indexes",
		Name:     "Low Usage Indexes",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("Found %d indexes with sustained low read rates (>500MB, >=10k writes, <1 scan/week)", len(lowUsage)),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Size", "Scans", "Writes"},
			Rows:    tableRows,
		},
	})
}
