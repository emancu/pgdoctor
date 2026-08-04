// Package cacheefficiency implements checks for database-wide and per-index buffer cache hit ratios.
package cacheefficiency

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
	cacheLowThreshold = 60.0

	indexCacheRatioThreshold    = 75.0
	indexCacheSizeFloorBytes    = 500 * check.MiB
	indexCacheMinScansPerDay    = 100.0
	indexCacheLifetimeScanFloor = 10000
)

type CacheEfficiencyQueries interface {
	DatabaseCacheEfficiency(context.Context) (db.DatabaseCacheEfficiencyRow, error)
	IndexCacheEfficiency(context.Context) ([]db.IndexCacheEfficiencyRow, error)
}

type checker struct {
	queries CacheEfficiencyQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "cache-efficiency",
		Name:        "Cache Efficiency",
		Description: "Analyzes database-wide and per-index buffer cache hit ratios",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries CacheEfficiencyQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	row, err := c.queries.DatabaseCacheEfficiency(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	checkCacheHitRatio(row, report)

	indexRows, err := c.queries.IndexCacheEfficiency(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	checkIndexCacheRatio(indexRows, report)

	return report, nil
}

func checkCacheHitRatio(row db.DatabaseCacheEfficiencyRow, report *check.Report) {
	if !row.CacheHitRatio.Valid {
		report.AddFinding(check.Finding{
			ID:       "cache-hit-ratio",
			Name:     "Cache Hit Ratio",
			Severity: check.SeverityPass,
			Details:  "Insufficient cache activity data (no blocks read or hit)",
		})
		return
	}

	ratio, _ := row.CacheHitRatio.Float64Value()
	cacheRatio := ratio.Float64

	if cacheRatio >= cacheLowThreshold {
		report.AddFinding(check.Finding{
			ID:       "cache-hit-ratio",
			Name:     "Cache Hit Ratio",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Cache hit ratio: %.2f%% (healthy)", cacheRatio),
		})
		return
	}

	details := fmt.Sprintf("Cache hit ratio: %.2f%% (below threshold)\nBlocks hit: %d\nBlocks read from disk: %d",
		cacheRatio, row.BlksHit.Int64, row.BlksRead.Int64)

	report.AddFinding(check.Finding{
		ID:       "cache-hit-ratio",
		Name:     "Cache Hit Ratio",
		Severity: check.SeverityInfo,
		Details:  details,
	})
}

func checkIndexCacheRatio(rows []db.IndexCacheEfficiencyRow, report *check.Report) {
	var tableRows []check.TableRow
	for _, row := range rows {
		if !row.CacheHitRatio.Valid {
			continue
		}

		ratio, _ := row.CacheHitRatio.Float64Value()
		cacheRatio := ratio.Float64

		if cacheRatio >= indexCacheRatioThreshold {
			continue
		}
		if row.IndexSizeBytes.Int64 < indexCacheSizeFloorBytes {
			continue
		}
		if !indexFrequentlyUsed(row.IdxScan.Int64, row.StatsReset) {
			continue
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.IndexName.String,
				check.FormatBytes(row.IndexSizeBytes.Int64),
				fmt.Sprintf("%.1f%%", cacheRatio),
			},
			Severity: check.SeverityInfo,
		})
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "index-cache-ratio",
			Name:     "Index Cache Efficiency",
			Severity: check.SeverityPass,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "index-cache-ratio",
		Name:     "Index Cache Efficiency",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("Found %d frequently-scanned indexes over 500MB with cache hit ratio below 75%%", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Index", "Size", "Hit %"},
			Rows:    tableRows,
		},
	})
}

// indexFrequentlyUsed gates on scan rate over the stats window; a NULL or
// sub-day window falls back to the lifetime scan count.
func indexFrequentlyUsed(idxScan int64, statsReset pgtype.Timestamptz) bool {
	if statsReset.Valid {
		if windowDays := time.Since(statsReset.Time).Hours() / 24; windowDays >= 1 {
			return float64(idxScan)/windowDays >= indexCacheMinScansPerDay
		}
	}
	return idxScan >= indexCacheLifetimeScanFloor
}
