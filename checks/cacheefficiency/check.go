// Package cacheefficiency implements checks for database buffer cache hit ratio.
package cacheefficiency

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
	cacheLowThreshold  = 90.0
	cacheWarnThreshold = 95.0
)

type CacheEfficiencyQueries interface {
	DatabaseCacheEfficiency(context.Context) (db.DatabaseCacheEfficiencyRow, error)
}

type checker struct {
	queries CacheEfficiencyQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "cache-efficiency",
		Name:        "Cache Efficiency",
		Description: "Analyzes database-wide buffer cache hit ratio",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries CacheEfficiencyQueries) check.Checker {
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

	return report, nil
}

func checkCacheHitRatio(row db.DatabaseCacheEfficiencyRow, report *check.Report) {
	if !row.CacheHitRatio.Valid {
		report.AddFinding(check.Finding{
			ID:       "cache-hit-ratio",
			Name:     "Cache Hit Ratio",
			Severity: check.SeverityOK,
			Details:  "Insufficient cache activity data (no blocks read or hit)",
		})
		return
	}

	ratio, _ := row.CacheHitRatio.Float64Value()
	cacheRatio := ratio.Float64

	if cacheRatio >= cacheWarnThreshold {
		report.AddFinding(check.Finding{
			ID:       "cache-hit-ratio",
			Name:     "Cache Hit Ratio",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Cache hit ratio: %.2f%% (healthy)", cacheRatio),
		})
		return
	}

	severity := check.SeverityWarn
	if cacheRatio < cacheLowThreshold {
		severity = check.SeverityFail
	}

	details := fmt.Sprintf("Cache hit ratio: %.2f%% (below threshold)\nBlocks hit: %d\nBlocks read from disk: %d",
		cacheRatio, row.BlksHit.Int64, row.BlksRead.Int64)

	report.AddFinding(check.Finding{
		ID:       "cache-hit-ratio",
		Name:     "Cache Hit Ratio",
		Severity: severity,
		Details:  details,
	})
}
