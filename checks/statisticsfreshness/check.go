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

	// A NULL stats_reset is not evidence of a long window: an unclean shutdown, a
	// crash, or a rebuilt replica also zeroes the counters and none of them records
	// a timestamp. Report the fact without inferring maturity from it.
	if !row.StatsReset.Valid {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "Statistics: never reset",
			Severity: check.SeverityPass,
			Details:  "No reset recorded for this database.",
		})
		return report, nil
	}

	// The window goes in the title because renderers drop Details on a PASS finding,
	// so anything stated there is invisible in the case this check is usually in.
	ageSeconds := row.AgeSeconds.Int64
	window := check.FormatDurationSec(ageSeconds)

	if ageSeconds >= minStatsDaysForAccuracy*secondsPerDay {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     fmt.Sprintf("Statistics: %s since last reset", window),
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Counters have accumulated since %s.", row.StatsReset.Time.Format(time.RFC3339)),
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
		Name:     fmt.Sprintf("Statistics: %s since last reset", window),
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf("Counters cover %s, less than the %d days recommended.\n\nThis may affect the accuracy of usage-based checks:\n%s",
			window,
			minStatsDaysForAccuracy,
			strings.Join(affectedChecks, "\n")),
	})

	return report, nil
}
