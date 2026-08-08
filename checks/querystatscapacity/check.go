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
	// The threshold as hours to recycle the table once. query.sql states it as
	// turnover per day, 0.5, when it sizes the minimum window.
	warnRecycleHours = 48

	usageID   = "entry-usage"
	usageName = "Entry Usage"
	rateID    = "statement-eviction-rate"
	rateName  = "Statement Eviction Rate"
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
			ID:       usageID,
			Name:     usageName + ": pg_stat_statements not available",
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

// reportEntryUsage grades nothing: occupancy is not a defect, and dealloc is
// cumulative so it cannot stand in for current churn either. See the README.
func reportEntryUsage(row db.QueryStatsCapacityRow, report *check.Report) {
	if row.UsageSkipReason.Valid {
		report.AddFinding(check.Finding{
			ID:       usageID,
			Name:     fmt.Sprintf("%s: %s entries, capacity unreadable", usageName, check.FormatNumber(row.Entries.Int64)),
			Severity: check.SeveritySkip,
			Details:  row.UsageSkipReason.String,
		})

		return
	}

	report.AddFinding(check.Finding{
		ID: usageID,
		Name: fmt.Sprintf("%s: %s/%s entries",
			usageName, check.FormatNumber(row.Entries.Int64), check.FormatNumber(row.MaxEntries.Int64)),
		Severity: check.SeverityPass,
	})
}

// reportEvictionRate grades how long the table takes to recycle once. The query
// decides whether that is computable at all.
func reportEvictionRate(row db.QueryStatsCapacityRow, report *check.Report) {
	if row.RateSkipReason.Valid {
		report.AddFinding(check.Finding{
			ID:       rateID,
			Name:     rateName,
			Severity: check.SeveritySkip,
			Details:  row.RateSkipReason.String,
		})

		return
	}

	if !row.RecycleHours.Valid {
		report.AddFinding(check.Finding{
			ID:       rateID,
			Name:     rateName + ": no evictions",
			Severity: check.SeverityPass,
		})

		return
	}

	// Grade the printed figure so rounding cannot put text and severity on
	// opposite sides of the threshold.
	hours := displayedRecycleHours(row.RecycleHours.Float64)

	severity, details := check.SeverityPass, ""
	if hours <= warnRecycleHours {
		severity = check.SeverityWarn
		details = evictionDetails(row)
	}

	report.AddFinding(check.Finding{
		ID: rateID,
		Name: fmt.Sprintf("%s: %s (%s average)", rateName, formatRecycle(hours),
			check.FormatDurationSec(int64(row.WindowSeconds.Float64))),
		Severity: severity,
		Details:  details,
	})
}

// displayedRecycleHours rounds to the precision that gets printed.
func displayedRecycleHours(hours float64) float64 {
	switch {
	case hours < 1:
		return math.Round(hours*60) / 60
	case hours < 48:
		return math.Round(hours*10) / 10
	default:
		return math.Round(hours/24*10) / 10 * 24
	}
}

// formatRecycle states how long the table takes to recycle rather than how many
// times a day it does. Both are the same figure; only one reads without arithmetic.
func formatRecycle(hours float64) string {
	switch {
	case hours < 1:
		return fmt.Sprintf("table recycled every %.0fm", hours*60)
	case hours < 48:
		return fmt.Sprintf("table recycled every %.1fh", hours)
	default:
		return fmt.Sprintf("table recycled every %.1fd", hours/24)
	}
}

func evictionDetails(row db.QueryStatsCapacityRow) string {
	return fmt.Sprintf(
		"%s entries discarded since %s from a table holding %s.\npartition-usage and temp-usage read this table, so a PASS from either covers only the statements that were not evicted.",
		check.FormatNumber(row.EntriesDiscarded.Int64),
		row.StatsReset.Time.Format("2006-01-02"),
		check.FormatNumber(row.MaxEntries.Int64))
}
