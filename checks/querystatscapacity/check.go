// Package querystatscapacity reports how much of the pg_stat_statements entry
// table is in use and how fast entries are being evicted.
package querystatscapacity

import (
	"context"
	_ "embed"
	"fmt"
	"math"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	// Below an hour a single eviction event extrapolates to a meaningless rate.
	minWindowSeconds = 3600

	secondsPerDay = 86400

	// entry_dealloc(): nvictims = Max(10, pgss_max * USAGE_DEALLOC_PERCENT / 100).
	// The floor doubles the batch at max=100, the minimum the GUC allows.
	deallocPercent  = 5
	deallocMinBatch = 10

	// Eviction is least-used-first, so at half the table per day only the hottest
	// statements survive.
	turnoverWarnPerDay = 0.5

	// 0.7*10 is 6.999999999999999 in float64 and would truncate to 0.6.
	turnoverDisplayEpsilon = 1e-9
)

// QueryStatsCapacityQueries defines the database queries needed by this check.
// HasPgStatStatements is generated from partition-usage's query.sql: sqlc query
// names are global to the shared db package, so it is declared, not redefined.
type QueryStatsCapacityQueries interface {
	HasPgStatStatements(context.Context) (pgtype.Bool, error)
	QueryStatsCapacity(context.Context) (db.QueryStatsCapacityRow, error)
}

type checker struct {
	queries QueryStatsCapacityQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "query-stats-capacity",
		Name:        "Query Stats Capacity",
		Description: "Reports pg_stat_statements entry usage and how fast entries are evicted",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries QueryStatsCapacityQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	available, err := c.queries.HasPgStatStatements(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	// Cluster-wide, so absence here says nothing about the shared hash.
	if !available.Bool {
		report.AddFinding(check.Finding{
			ID:       entryUsageID,
			Name:     entryUsageName + ": pg_stat_statements not available",
			Severity: check.SeveritySkip,
			Details:  "pg_stat_statements is not available, so its capacity cannot be inspected.",
		})
		report.Severity = check.SeveritySkip

		return report, nil
	}

	row, err := c.queries.QueryStatsCapacity(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	reportEntryUsage(row, report)
	reportEvictionRate(row, report)

	// AddFinding only raises, and SKIP sorts below PASS.
	if allSkipped(report) {
		report.Severity = check.SeveritySkip
	}

	return report, nil
}

func allSkipped(report *check.Report) bool {
	for _, result := range report.Results {
		if result.Severity != check.SeveritySkip {
			return false
		}
	}

	return len(report.Results) > 0
}

const (
	entryUsageID   = "entry-usage"
	entryUsageName = "Entry Usage"
)

// reportEntryUsage grades nothing: occupancy is not a defect, and dealloc is
// cumulative so it cannot stand in for current churn either. See the README.
func reportEntryUsage(row db.QueryStatsCapacityRow, report *check.Report) {
	if !row.MaxEntries.Valid || row.MaxEntries.Int64 <= 0 {
		report.AddFinding(check.Finding{
			ID: entryUsageID,
			Name: fmt.Sprintf("%s: %s entries, capacity unreadable",
				entryUsageName, check.FormatNumber(row.Entries.Int64)),
			Severity: check.SeveritySkip,
			Debug:    capacityDebug(row),
		})

		return
	}

	name := fmt.Sprintf("%s: %s/%s entries",
		entryUsageName, check.FormatNumber(row.Entries.Int64), check.FormatNumber(row.MaxEntries.Int64))

	report.AddFinding(check.Finding{
		ID:       entryUsageID,
		Name:     name,
		Severity: check.SeverityPass,
		Debug:    capacityDebug(row),
	})
}

func capacityDebug(row db.QueryStatsCapacityRow) string {
	return fmt.Sprintf("entries=%d max=%d dealloc=%d batch=%d window=%s",
		row.Entries.Int64, row.MaxEntries.Int64, row.EvictionEvents.Int64,
		entriesPerEviction(row.MaxEntries.Int64),
		check.FormatDurationSec(int64(row.SecondsSinceReset.Float64)))
}

// reportEvictionRate grades a rate, never the raw dealloc count: that only grows.
func reportEvictionRate(row db.QueryStatsCapacityRow, report *check.Report) {
	const id = "statement-eviction-rate"
	const name = "Statement Eviction Rate"

	// Skip rather than pass: PASS would read as "nothing is being evicted", which
	// none of these states can establish.
	window, ok := windowSeconds(row)
	switch {
	case !ok:
		report.AddFinding(check.Finding{
			ID:       id,
			Name:     name,
			Severity: check.SeveritySkip,
			Details:  "Counter window unknown; the eviction rate cannot be computed.",
		})

		return
	case window < minWindowSeconds:
		report.AddFinding(check.Finding{
			ID:       id,
			Name:     name,
			Severity: check.SeveritySkip,
			Details: fmt.Sprintf("Counters cover only %s; need at least 1h to compute an eviction rate.",
				check.FormatDurationSec(int64(window))),
		})

		return
	case !row.MaxEntries.Valid || row.MaxEntries.Int64 <= 0:
		report.AddFinding(check.Finding{
			ID:       id,
			Name:     name,
			Severity: check.SeveritySkip,
			Details:  "pg_stat_statements.max is unreadable; turnover has no capacity to be a share of.",
		})

		return
	}

	// Grade the printed number so rounding cannot put text and severity on
	// opposite sides of the threshold.
	turnover := displayedTurnover(turnoverPerDay(row.EvictionEvents.Int64, row.MaxEntries.Int64, window))
	if row.EvictionEvents.Int64 == 0 {
		report.AddFinding(check.Finding{
			ID:       id,
			Name:     name + ": no evictions",
			Severity: check.SeverityPass,
		})

		return
	}

	severity, details := check.SeverityPass, ""
	// Degrades observability, not the database. WARN is the ceiling.
	if turnover >= turnoverWarnPerDay {
		severity = check.SeverityWarn
		details = evictionDetails(row)
	}

	report.AddFinding(check.Finding{
		ID: id,
		Name: fmt.Sprintf("%s: %s averaged over %s",
			name, formatTurnover(turnover), check.FormatDurationSec(int64(window))),
		Severity: severity,
		Details:  details,
	})
}

// displayedTurnover truncates to the printed tenth; rounding would let 0.498 show
// as "0.5x" on a finding that passed at 0.5.
func displayedTurnover(turnover float64) float64 {
	return math.Floor(turnover*10+turnoverDisplayEpsilon) / 10
}

func formatTurnover(turnover float64) string {
	if turnover < 0.1 {
		return "<0.1x capacity/day"
	}

	return fmt.Sprintf("%.1fx capacity/day", turnover)
}

// evictionDetails carries the totals behind the rate. Only reached once max is known.
func evictionDetails(row db.QueryStatsCapacityRow) string {
	discarded := row.EvictionEvents.Int64 * entriesPerEviction(row.MaxEntries.Int64)

	return fmt.Sprintf(
		"%s eviction events since %s discarded ~%s entries against a capacity of %s."+
			"\npartition-usage and temp-usage read what is left.",
		check.FormatNumber(row.EvictionEvents.Int64),
		row.StatsReset.Time.Format("2006-01-02"),
		check.FormatNumber(discarded),
		check.FormatNumber(row.MaxEntries.Int64))
}

// entriesPerEviction mirrors entry_dealloc(), integer truncation included.
// Callers must have established max > 0.
func entriesPerEviction(maxEntries int64) int64 {
	batch := maxEntries * deallocPercent / 100
	if batch < deallocMinBatch {
		return deallocMinBatch
	}

	return batch
}

// turnoverPerDay converts eviction events into the share of capacity lost per day.
func turnoverPerDay(events, maxEntries int64, window float64) float64 {
	if events <= 0 || maxEntries <= 0 || window <= 0 {
		return 0
	}

	entriesPerDay := float64(events*entriesPerEviction(maxEntries)) / (window / secondsPerDay)

	return entriesPerDay / float64(maxEntries)
}

// windowSeconds reports the accumulation period, and whether it is known at all.
func windowSeconds(row db.QueryStatsCapacityRow) (float64, bool) {
	if !row.SecondsSinceReset.Valid || !row.StatsReset.Valid {
		return 0, false
	}

	return row.SecondsSinceReset.Float64, true
}
