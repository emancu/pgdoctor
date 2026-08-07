// Package statisticsfreshness validates that PostgreSQL statistics are mature enough for accurate analysis.
package statisticsfreshness

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	minStatsDaysForAccuracy = 7
	secondsPerDay           = 24 * 60 * 60
)

type StatisticsFreshnessQueries interface {
	StatisticsFreshness(context.Context) (db.StatisticsFreshnessRow, error)
}

type checker struct {
	queries StatisticsFreshnessQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "statistics-freshness",
		Name:        "Statistics Freshness",
		Description: "Validates PostgreSQL statistics are mature enough for usage-based analysis",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries StatisticsFreshnessQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	row, err := c.queries.StatisticsFreshness(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	window, exact := statsWindow(row)

	// An unrecorded reset is the dangerous case, not the safe one: a crash, unclean
	// shutdown or rebuilt replica zeroes the counters and records nothing, so a short
	// uptime means they may be far younger than they look. Judging only explicit
	// resets warns about the one event an operator already knows about and stays
	// quiet about the ones they do not.
	if window >= minStatsDaysForAccuracy*secondsPerDay {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     windowTitle(window, exact),
			Severity: check.SeverityPass,
			Details:  windowDetails(row, window, exact),
		})
		return report, nil
	}

	affectedChecks := []string{
		"index-usage",
		"table-seq-scans",
		"cache-efficiency",
		"temp-usage",
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     windowTitle(window, exact),
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf("%s\n\nThat is less than the %d days recommended, which may affect the accuracy of usage-based checks:\n%s",
			windowDetails(row, window, exact),
			minStatsDaysForAccuracy,
			strings.Join(affectedChecks, "\n")),
	})

	return report, nil
}

// statsWindow returns how far back the counters reach, and whether that is exact.
// Only pg_stat_reset() records a timestamp, so without one the best that can be said
// is that they reach back at least as far as the server start.
func statsWindow(row db.StatisticsFreshnessRow) (seconds int64, exact bool) {
	if row.StatsReset.Valid {
		return row.AgeSeconds.Int64, true
	}

	return row.UptimeSeconds.Int64, false
}

// windowTitle puts the window in the finding's own name. A passing check drops its
// Details, so this is the only place the figure stays visible when nothing is wrong.
func windowTitle(window int64, exact bool) string {
	if exact {
		return fmt.Sprintf("Statistics: %s since last reset", check.FormatDurationSec(window))
	}

	return fmt.Sprintf("Statistics: at least %s, no reset recorded", check.FormatDurationSec(window))
}

func windowDetails(row db.StatisticsFreshnessRow, window int64, exact bool) string {
	if exact {
		return fmt.Sprintf("Counters cover %s, since %s.",
			check.FormatDurationSec(window), row.StatsReset.Time.Format(time.RFC3339))
	}

	return fmt.Sprintf(
		"No reset recorded, and the server started %s ago, so the counters cover at least that. A crash, unclean shutdown or rebuilt replica zeroes them without recording a reset, so they may cover no more.",
		check.FormatDurationSec(window))
}
