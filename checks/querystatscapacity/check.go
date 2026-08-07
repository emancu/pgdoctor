// Package querystatscapacity reports how much of the pg_stat_statements entry
// table is in use and how fast entries are being evicted.
package querystatscapacity

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	// minWindowSeconds is the shortest counter window that supports a daily rate.
	// Below an hour the denominator is small enough that a single eviction event
	// extrapolates to a turnover figure that says nothing.
	minWindowSeconds = 3600

	secondsPerDay = 86400

	// deallocFraction is the share of pg_stat_statements.max discarded by one
	// eviction event: USAGE_DEALLOC_PERCENT in pg_stat_statements.c, unchanged
	// since the counter was introduced in PostgreSQL 14.
	deallocFraction = 0.05

	// turnoverWarnPerDay is the daily eviction volume, as a multiple of capacity,
	// at which the tracked set stops representing the workload. At half the table
	// recycled per day, eviction is least-used-first, so anything but the hottest
	// statements is gone within hours of running.
	turnoverWarnPerDay = 0.5
)

// QueryStatsCapacityQueries defines the database queries needed by this check.
// HasPgStatStatements is generated from partition-usage's query.sql - sqlc query
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

	// Nothing reads pg_stat_statements when it is absent, so no check is working
	// from a truncated sample and there is no capacity to report on.
	if !available.Bool {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
			Details:  "pg_stat_statements is not available.",
		})

		return report, nil
	}

	row, err := c.queries.QueryStatsCapacity(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	reportEntryCapacity(row, report)
	reportEvictionRate(row, report)

	return report, nil
}

// reportEntryCapacity states how much of the table is occupied. A full table is
// not a defect on its own - a stable workload larger than max sits pinned at max
// forever without losing anything - so this finding grades nothing. The eviction
// rate below is what says whether entries are actually being lost.
func reportEntryCapacity(row db.QueryStatsCapacityRow, report *check.Report) {
	name := fmt.Sprintf("%s: %s entries", report.Name, check.FormatNumber(row.Entries.Int64))
	if row.MaxEntries.Valid && row.MaxEntries.Int64 > 0 {
		name = fmt.Sprintf("%s: %s/%s entries",
			report.Name, check.FormatNumber(row.Entries.Int64), check.FormatNumber(row.MaxEntries.Int64))
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     name,
		Severity: check.SeverityPass,
		Debug:    capacityDebug(row),
	})
}

func capacityDebug(row db.QueryStatsCapacityRow) string {
	return fmt.Sprintf("entries=%d max=%d dealloc=%d window=%s",
		row.Entries.Int64, row.MaxEntries.Int64, row.EvictionEvents.Int64,
		check.FormatDurationSec(int64(row.SecondsSinceReset.Float64)))
}

// reportEvictionRate grades the eviction volume as a multiple of capacity per
// day. It is a rate, never the raw dealloc count: that counter only grows, so a
// long-lived instance accumulates a large one from churn that stopped months ago.
func reportEvictionRate(row db.QueryStatsCapacityRow, report *check.Report) {
	const id = "statement-eviction-rate"
	const name = "Statement Eviction Rate"

	// The window is the only denominator available, and pg_stat_statements_info
	// carries no other clock. Skip rather than pass: a bare PASS reads as "nothing
	// is being evicted", which a window this short cannot establish.
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
	}

	turnover := turnoverPerDay(row.EvictionEvents.Int64, window)
	if turnover == 0 {
		report.AddFinding(check.Finding{
			ID:       id,
			Name:     name + ": no evictions",
			Severity: check.SeverityPass,
		})

		return
	}

	severity, details := check.SeverityPass, ""
	// Eviction degrades every other pg_stat_statements reader; it does not degrade
	// the database. WARN is the ceiling.
	if turnover >= turnoverWarnPerDay {
		severity = check.SeverityWarn
		details = evictionDetails(row)
	}

	report.AddFinding(check.Finding{
		ID:       id,
		Name:     name + ": " + formatTurnover(turnover),
		Severity: severity,
		Details:  details,
	})
}

// formatTurnover renders the daily turnover. Values under the display floor
// collapse to "0.0" at one decimal, which reads as no eviction at all when some
// did happen.
func formatTurnover(turnover float64) string {
	if turnover < 0.1 {
		return "<0.1x capacity/day"
	}

	return fmt.Sprintf("%.1fx capacity/day", turnover)
}

// evictionDetails carries what the rate alone cannot: the totals behind it, and
// which other checks are reading the sample it has been thinning.
func evictionDetails(row db.QueryStatsCapacityRow) string {
	since := row.StatsReset.Time.Format("2006-01-02")
	events := check.FormatNumber(row.EvictionEvents.Int64)

	totals := fmt.Sprintf("%s eviction events since %s.", events, since)
	if row.MaxEntries.Valid && row.MaxEntries.Int64 > 0 {
		discarded := int64(float64(row.EvictionEvents.Int64) * deallocFraction * float64(row.MaxEntries.Int64))
		totals = fmt.Sprintf("%s eviction events since %s discarded ~%s entries against a capacity of %s.",
			events, since, check.FormatNumber(discarded), check.FormatNumber(row.MaxEntries.Int64))
	}

	return totals + "\npartition-usage and temp-usage read what is left."
}

// turnoverPerDay converts eviction events into the fraction of capacity lost per
// day. Each event drops a fixed 5% of max, so the multiple of capacity is
// independent of max and holds on instances where max is unreadable.
func turnoverPerDay(events int64, window float64) float64 {
	if events <= 0 || window <= 0 {
		return 0
	}

	return float64(events) * deallocFraction / (window / secondsPerDay)
}

// windowSeconds reports how long the counters have been accumulating, and whether
// that period is known at all. A NULL stats_reset is rare here - pg_stat_statements
// stamps it at shared-memory init - but the column is nullable and a rate over an
// unknown period is not a rate.
func windowSeconds(row db.QueryStatsCapacityRow) (float64, bool) {
	if !row.SecondsSinceReset.Valid || !row.StatsReset.Valid {
		return 0, false
	}

	return row.SecondsSinceReset.Float64, true
}
