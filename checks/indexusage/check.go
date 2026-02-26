// Package indexusage implements checks for identifying unused and inefficient indexes.
package indexusage

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
	unusedSizeThresholdMB  = 10
	lowUsageScanThreshold  = 1000
	lowUsageWriteThreshold = 10000
	cacheLowThreshold      = 90.0
	cacheWarnThreshold     = 95.0
	cacheMinSizeMB         = 10
	cacheFailSizeMB        = 100
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

func checkUnusedIndexes(rows []db.IndexUsageStatsRow, report *check.Report) {
	var unusedIndexes []string
	unusedCount := 0

	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}

		sizeBytes := row.IndexSizeBytes
		sizeMB := float64(sizeBytes.Int64) / (1024 * 1024)

		if row.IdxScan.Int64 == 0 && sizeMB > unusedSizeThresholdMB {
			unusedCount++
			if len(unusedIndexes) < 10 {
				unusedIndexes = append(unusedIndexes, fmt.Sprintf("%s.%s (%.1f MB)", row.TableName.String, row.IndexName.String, sizeMB))
			}
		}
	}

	if unusedCount == 0 {
		report.AddFinding(check.Finding{
			ID:       "unused-indexes",
			Name:     "Unused Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	details := fmt.Sprintf("Found %d unused indexes (0 scans, size > %d MB):\n%s",
		unusedCount,
		unusedSizeThresholdMB,
		strings.Join(unusedIndexes, "\n"),
	)
	if unusedCount > len(unusedIndexes) {
		details += fmt.Sprintf("\n... and %d more", unusedCount-len(unusedIndexes))
	}

	report.AddFinding(check.Finding{
		ID:       "unused-indexes",
		Name:     "Unused Indexes",
		Severity: check.SeverityWarn,
		Details:  details,
	})
}

func checkLowUsageIndexes(rows []db.IndexUsageStatsRow, report *check.Report) {
	var lowUsageIndexes []string
	lowUsageCount := 0

	for _, row := range rows {
		if row.IsPrimary || row.IsUnique {
			continue
		}

		if row.IdxScan.Int64 > 0 && row.IdxScan.Int64 < lowUsageScanThreshold && row.TableWrites.Int64 > lowUsageWriteThreshold {
			lowUsageCount++
			if len(lowUsageIndexes) < 10 {
				lowUsageIndexes = append(lowUsageIndexes, fmt.Sprintf("%s.%s (scans: %d, writes: %d)",
					row.TableName.String, row.IndexName.String, row.IdxScan.Int64, row.TableWrites.Int64))
			}
		}
	}

	if lowUsageCount == 0 {
		report.AddFinding(check.Finding{
			ID:       "low-usage-indexes",
			Name:     "Low Usage Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	details := fmt.Sprintf("Found %d indexes with low read usage but high write cost:\n%s",
		lowUsageCount,
		strings.Join(lowUsageIndexes, "\n"),
	)
	if lowUsageCount > len(lowUsageIndexes) {
		details += fmt.Sprintf("\n... and %d more", lowUsageCount-len(lowUsageIndexes))
	}

	report.AddFinding(check.Finding{
		ID:       "low-usage-indexes",
		Name:     "Low Usage Indexes",
		Severity: check.SeverityWarn,
		Details:  details,
	})
}

func checkIndexCacheRatio(rows []db.IndexUsageStatsRow, report *check.Report) {
	var lowCacheIndexes []string
	failCount := 0
	warnCount := 0

	for _, row := range rows {
		if !row.CacheHitRatio.Valid {
			continue
		}

		cacheRatio, _ := row.CacheHitRatio.Float64Value()
		sizeBytes := row.IndexSizeBytes
		sizeMB := float64(sizeBytes.Int64) / (1024 * 1024)

		if cacheRatio.Float64 < cacheLowThreshold && sizeMB > cacheFailSizeMB {
			failCount++
			if len(lowCacheIndexes) < 10 {
				lowCacheIndexes = append(lowCacheIndexes, fmt.Sprintf("%s.%s (%.1f%%, %.1f MB)",
					row.TableName.String, row.IndexName.String, cacheRatio.Float64, sizeMB))
			}
		} else if cacheRatio.Float64 < cacheWarnThreshold && sizeMB > cacheMinSizeMB {
			warnCount++
			if len(lowCacheIndexes) < 10 {
				lowCacheIndexes = append(lowCacheIndexes, fmt.Sprintf("%s.%s (%.1f%%, %.1f MB)",
					row.TableName.String, row.IndexName.String, cacheRatio.Float64, sizeMB))
			}
		}
	}

	totalIssues := failCount + warnCount
	if totalIssues == 0 {
		report.AddFinding(check.Finding{
			ID:       "index-cache-ratio",
			Name:     "Index Cache Efficiency",
			Severity: check.SeverityOK,
		})
		return
	}

	details := fmt.Sprintf("Found %d indexes with low cache hit ratios:\n%s",
		totalIssues,
		strings.Join(lowCacheIndexes, "\n"),
	)
	if totalIssues > len(lowCacheIndexes) {
		details += fmt.Sprintf("\n... and %d more", totalIssues-len(lowCacheIndexes))
	}

	report.AddFinding(check.Finding{
		ID:       "index-cache-ratio",
		Name:     "Index Cache Efficiency",
		Severity: check.SeverityWarn,
		Details:  details,
	})
}
