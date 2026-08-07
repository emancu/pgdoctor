// Package cacheefficiency implements checks for database-wide, per-index, and per-table buffer cache hit ratios.
package cacheefficiency

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	cacheLowThreshold = 60.0

	indexCacheRatioThreshold = 75.0
	indexCacheSizeFloorBytes = 500 * check.MiB

	tableCacheRatioThreshold = 75.0
	tableCacheSizeFloorBytes = 500 * check.MiB

	hotRankMax   = 20
	hotShareMin  = 0.01
	hotScanFloor = 10000
)

type CacheEfficiencyQueries interface {
	DatabaseCacheEfficiency(context.Context) (db.DatabaseCacheEfficiencyRow, error)
	IndexCacheEfficiency(context.Context) ([]db.IndexCacheEfficiencyRow, error)
	TableCacheEfficiency(context.Context) ([]db.TableCacheEfficiencyRow, error)
}

type checker struct {
	queries CacheEfficiencyQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "cache-efficiency",
		Name:        "Cache Efficiency",
		Description: "Analyzes database-wide, per-index, and per-table buffer cache hit ratios",
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

	tableRows, err := c.queries.TableCacheEfficiency(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	checkTableCacheRatio(tableRows, report)

	return report, nil
}

func checkCacheHitRatio(row db.DatabaseCacheEfficiencyRow, report *check.Report) {
	if !row.CacheHitRatio.Valid {
		report.AddFinding(check.Finding{
			ID:       "cache-hit-ratio",
			Name:     "Cache Hit Ratio",
			Severity: check.SeveritySkip,
			Details:  "No blocks read or hit yet, so there is no ratio to report",
		})
		return
	}

	ratio, _ := row.CacheHitRatio.Float64Value()
	cacheRatio := ratio.Float64

	if cacheRatio >= cacheLowThreshold {
		report.AddFinding(check.Finding{
			ID:       "cache-hit-ratio",
			Name:     fmt.Sprintf("Cache Hit Ratio: %.2f%%", cacheRatio),
			Severity: check.SeverityPass,
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
		share, _ := row.ScanShare.Float64Value()
		if !indexIsHot(row.IdxScan.Int64, row.ScanRank.Int64, share.Float64) {
			continue
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				check.FormatBytes(row.IndexSizeBytes.Int64),
				fmt.Sprintf("%.1f%%", cacheRatio),
				row.IndexName.String,
			},
			Severity: check.SeverityInfo,
		})
	}

	debug := topIndexesDebug(rows)

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "index-cache-ratio",
			Name:     "Index Cache Efficiency",
			Severity: check.SeverityPass,
			Debug:    debug,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "index-cache-ratio",
		Name:     "Index Cache Efficiency",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("Found %d hot indexes over 500MB with cache hit ratio below 75%%", len(tableRows)),
		Debug:    debug,
		Table: &check.Table{
			Headers: []string{"Size", "Hit %", "Index"},
			Rows:    tableRows,
		},
	})
}

// topIndexesDebug lists the top-20 scan ranking so the hot gate is verifiable.
func topIndexesDebug(rows []db.IndexCacheEfficiencyRow) string {
	top := make([]db.IndexCacheEfficiencyRow, 0, hotRankMax)
	for _, row := range rows {
		if row.ScanRank.Int64 <= hotRankMax {
			top = append(top, row)
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].ScanRank.Int64 < top[j].ScanRank.Int64 })

	var b strings.Builder
	b.WriteString("Top indexes by scans:")
	for _, row := range top {
		hit := "-"
		if row.CacheHitRatio.Valid {
			r, _ := row.CacheHitRatio.Float64Value()
			hit = fmt.Sprintf("%.1f%%", r.Float64)
		}
		share, _ := row.ScanShare.Float64Value()
		fmt.Fprintf(&b, "\n#%-2d hit %s  share %.1f%%  scans %s  %s",
			row.ScanRank.Int64, hit, share.Float64*100, check.FormatNumber(row.IdxScan.Int64), row.IndexName.String)
	}
	return b.String()
}

// indexIsHot gates on absolute scan volume plus a top-rank or traffic-share signal.
func indexIsHot(idxScan, rank int64, share float64) bool {
	if idxScan < hotScanFloor {
		return false
	}
	return rank <= hotRankMax || share >= hotShareMin
}

func checkTableCacheRatio(rows []db.TableCacheEfficiencyRow, report *check.Report) {
	var tableRows []check.TableRow
	for _, row := range rows {
		if !row.CacheHitRatio.Valid {
			continue
		}

		ratio, _ := row.CacheHitRatio.Float64Value()
		cacheRatio := ratio.Float64

		if cacheRatio >= tableCacheRatioThreshold {
			continue
		}
		if row.TableSizeBytes.Int64 < tableCacheSizeFloorBytes {
			continue
		}
		share, _ := row.ReadShare.Float64Value()
		if !indexIsHot(row.Reads.Int64, row.ReadRank.Int64, share.Float64) {
			continue
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				check.FormatBytes(row.TableSizeBytes.Int64),
				fmt.Sprintf("%.1f%%", cacheRatio),
				row.TableName.String,
			},
			Severity: check.SeverityInfo,
		})
	}

	debug := topTablesDebug(rows)

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "table-cache-ratio",
			Name:     "Table Cache Efficiency",
			Severity: check.SeverityPass,
			Debug:    debug,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "table-cache-ratio",
		Name:     "Table Cache Efficiency",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("Found %d hot tables over 500MB with heap cache hit ratio below 75%%", len(tableRows)),
		Debug:    debug,
		Table: &check.Table{
			Headers: []string{"Size", "Hit %", "Table"},
			Rows:    tableRows,
		},
	})
}

// topTablesDebug lists the top-20 read ranking so the hot gate is verifiable.
func topTablesDebug(rows []db.TableCacheEfficiencyRow) string {
	top := make([]db.TableCacheEfficiencyRow, 0, hotRankMax)
	for _, row := range rows {
		if row.ReadRank.Int64 <= hotRankMax {
			top = append(top, row)
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].ReadRank.Int64 < top[j].ReadRank.Int64 })

	var b strings.Builder
	b.WriteString("Top tables by reads:")
	for _, row := range top {
		hit := "-"
		if row.CacheHitRatio.Valid {
			r, _ := row.CacheHitRatio.Float64Value()
			hit = fmt.Sprintf("%.1f%%", r.Float64)
		}
		share, _ := row.ReadShare.Float64Value()
		fmt.Fprintf(&b, "\n#%-2d hit %s  share %.1f%%  reads %s  %s",
			row.ReadRank.Int64, hit, share.Float64*100, check.FormatNumber(row.Reads.Int64), row.TableName.String)
	}
	return b.String()
}
