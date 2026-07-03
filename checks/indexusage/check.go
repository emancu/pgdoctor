// Package indexusage implements checks for identifying unused and inefficient indexes.
package indexusage

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
	unusedSizeThresholdMB  = 10
	lowUsageScanThreshold  = 1000
	lowUsageWriteThreshold = 10000

	// Cache-hit ratio is only judged on hot, large, well-exercised indexes: the
	// metric is confounded by the OS page cache and cumulative-since-stats-reset
	// counters, so cold or tiny indexes produce lifetime-ratio noise.
	cacheHitThreshold  = 90.0
	cacheMinScans      = 1000
	cacheMinSizeMB     = 100
	cacheMinBlockReads = 100000 // idx_blks_hit + idx_blks_read
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
	checkIndexCacheRatio(rows, report)

	return report, nil
}

func indexSizeMB(row db.IndexUsageStatsRow) float64 {
	return float64(row.IndexSizeBytes.Int64) / (1024 * 1024)
}

func checkUnusedIndexes(rows []db.IndexUsageStatsRow, report *check.Report) {
	var tableRows []check.TableRow

	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}

		if row.IdxScan.Int64 == 0 && indexSizeMB(row) > unusedSizeThresholdMB {
			tableRows = append(tableRows, check.TableRow{
				Cells: []string{
					row.IndexName.String,
					row.TableName.String,
					check.FormatBytes(row.IndexSizeBytes.Int64),
					row.Indexdef.String,
				},
				Severity: check.SeverityWarn,
			})
		}
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "unused-indexes",
			Name:     "Unused Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "unused-indexes",
		Name:     "Unused Indexes",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d unused indexes (0 scans, size > %d MB)", len(tableRows), unusedSizeThresholdMB),
		Table: &check.Table{
			Headers: []string{"Index", "Table", "Size", "Definition"},
			Rows:    tableRows,
		},
	})
}

func checkLowUsageIndexes(rows []db.IndexUsageStatsRow, report *check.Report) {
	var tableRows []check.TableRow

	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}

		if row.IdxScan.Int64 > 0 && row.IdxScan.Int64 < lowUsageScanThreshold && row.TableWrites.Int64 > lowUsageWriteThreshold {
			tableRows = append(tableRows, check.TableRow{
				Cells: []string{
					row.IndexName.String,
					row.TableName.String,
					check.FormatBytes(row.IndexSizeBytes.Int64),
					check.FormatNumber(row.IdxScan.Int64),
					check.FormatNumber(row.TableWrites.Int64),
				},
				Severity: check.SeverityWarn,
			})
		}
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "low-usage-indexes",
			Name:     "Low Usage Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "low-usage-indexes",
		Name:     "Low Usage Indexes",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d indexes with low read usage but high write cost", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Index", "Table", "Size", "Scans", "Table Writes"},
			Rows:    tableRows,
		},
	})
}

func checkIndexCacheRatio(rows []db.IndexUsageStatsRow, report *check.Report) {
	var tableRows []check.TableRow

	for _, row := range rows {
		if !row.CacheHitRatio.Valid {
			continue
		}

		cacheRatio := check.NumericToFloat64(row.CacheHitRatio)
		blocksTouched := row.IdxBlksHit.Int64 + row.IdxBlksRead.Int64

		if row.IdxScan.Int64 >= cacheMinScans &&
			blocksTouched >= cacheMinBlockReads &&
			indexSizeMB(row) > cacheMinSizeMB &&
			cacheRatio < cacheHitThreshold {
			tableRows = append(tableRows, check.TableRow{
				Cells: []string{
					row.IndexName.String,
					row.TableName.String,
					check.FormatBytes(row.IndexSizeBytes.Int64),
					check.FormatNumber(row.IdxScan.Int64),
					fmt.Sprintf("%.2f", cacheRatio),
					check.FormatNumber(row.IdxBlksRead.Int64),
				},
				Severity: check.SeverityWarn,
			})
		}
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "index-cache-ratio",
			Name:     "Index Cache Efficiency",
			Severity: check.SeverityOK,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "index-cache-ratio",
		Name:     "Index Cache Efficiency",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d hot, large indexes with cache hit ratio < %.0f%%", len(tableRows), cacheHitThreshold),
		Table: &check.Table{
			Headers: []string{"Index", "Table", "Size", "Scans", "Cache Hit %", "Blocks Read"},
			Rows:    tableRows,
		},
	})
}
