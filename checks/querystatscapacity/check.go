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
	// minWindowSeconds is the shortest counter window that supports a daily rate.
	// Below an hour the denominator is small enough that a single eviction event
	// extrapolates to a turnover figure that says nothing.
	minWindowSeconds = 3600

	secondsPerDay = 86400

	// deallocPercent and deallocMinBatch mirror entry_dealloc() in
	// pg_stat_statements.c, unchanged since the counter arrived in PostgreSQL 14:
	//
	//   nvictims = Max(10, pgss_max * USAGE_DEALLOC_PERCENT / 100);
	//
	// The floor is not decorative. pg_stat_statements.max bottoms out at 100,
	// where 5% is 5 and the floor of 10 doubles the real batch, so a fixed 0.05
	// would report half the true turnover and pass a saturated instance.
	deallocPercent  = 5
	deallocMinBatch = 10

	// turnoverWarnPerDay is the daily eviction volume, as a multiple of capacity,
	// at which the tracked set stops representing the workload. At half the table
	// recycled per day, eviction is least-used-first, so anything but the hottest
	// statements is gone within hours of running.
	turnoverWarnPerDay = 0.5

	// turnoverDisplayEpsilon absorbs binary representation error before the
	// displayed value is truncated. 0.7*10 is 6.999999999999999 in float64, which
	// would otherwise truncate to 0.6.
	turnoverDisplayEpsilon = 1e-9
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

	// The table is cluster-wide, so its absence from this database says nothing
	// about whether the shared hash is evicting. Nothing was inspected, and PASS
	// would claim a capacity we never measured.
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

	// AddFinding only raises severity and SKIP sorts below PASS, so a check that
	// graded nothing would otherwise report PASS. See internal/checktest.
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

// The finding carries its own id and name rather than reusing the check's: the
// renderer prints the report header and every finding, so a finding named after
// the check prints the check name twice.
const (
	entryUsageID   = "entry-usage"
	entryUsageName = "Entry Usage"
)

// reportEntryUsage states how much of the table is occupied, and grades nothing.
// Occupancy is not a defect: a stable workload larger than max sits pinned there
// indefinitely without losing anything, and below capacity there is headroom. Nor
// can it stand in for current churn - dealloc is cumulative, so "full and has
// evicted" stays true forever after the churn stops, and one snapshot cannot tell
// that from a spike an hour old. Recency needs two samples; see the README.
func reportEntryUsage(row db.QueryStatsCapacityRow, report *check.Report) {
	// max is what "full" is relative to; without it there is nothing to grade.
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

// reportEvictionRate grades the eviction volume as a multiple of capacity per
// day. It is a rate, never the raw dealloc count: that counter only grows, so a
// long-lived instance accumulates a large one from churn that stopped months ago.
func reportEvictionRate(row db.QueryStatsCapacityRow, report *check.Report) {
	const id = "statement-eviction-rate"
	const name = "Statement Eviction Rate"

	// The window is the only denominator available, and pg_stat_statements_info
	// carries no other clock. Skip rather than pass: a bare PASS reads as "nothing
	// is being evicted", which a window this short cannot establish. The same goes
	// for an unreadable max - the batch size and the capacity the turnover is a
	// multiple of both derive from it.
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

	// Grade the number that gets printed, not the raw one. Any display rounding can
	// otherwise land on the far side of the threshold from the severity, and the
	// finding then contradicts itself by a margin no reader can see.
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
	// Eviction degrades every other pg_stat_statements reader; it does not degrade
	// the database. WARN is the ceiling.
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

// displayedTurnover truncates to the tenth that will be printed. Rounding would let
// 0.498 print as "0.5x capacity/day" on a finding that passed at a 0.5 threshold.
// The epsilon absorbs binary representation error before truncating: 0.7*10 is
// 6.999999999999999 in float64 and would otherwise fall to 0.6.
func displayedTurnover(turnover float64) float64 {
	return math.Floor(turnover*10+turnoverDisplayEpsilon) / 10
}

func formatTurnover(turnover float64) string {
	if turnover < 0.1 {
		return "<0.1x capacity/day"
	}

	return fmt.Sprintf("%.1fx capacity/day", turnover)
}

// evictionDetails carries what the rate alone cannot: the totals behind it, and
// which other checks are reading the sample it has been thinning. Only reached
// once max is known, so the batch size is too.
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

// entriesPerEviction mirrors entry_dealloc() in pg_stat_statements.c:
//
//	nvictims = Max(10, pgss_max * USAGE_DEALLOC_PERCENT / 100);
//
// Integer arithmetic, so the deliberate truncation is reproduced here. Callers
// must have established max > 0.
func entriesPerEviction(maxEntries int64) int64 {
	batch := maxEntries * deallocPercent / 100
	if batch < deallocMinBatch {
		return deallocMinBatch
	}

	return batch
}

// turnoverPerDay converts eviction events into the share of capacity lost per
// day. The batch is a fixed 5% of max only above max = 200; below that the
// floor of 10 entries dominates and the share is larger than 5%.
func turnoverPerDay(events, maxEntries int64, window float64) float64 {
	if events <= 0 || maxEntries <= 0 || window <= 0 {
		return 0
	}

	entriesPerDay := float64(events*entriesPerEviction(maxEntries)) / (window / secondsPerDay)

	return entriesPerDay / float64(maxEntries)
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
