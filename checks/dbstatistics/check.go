// Package dbstatistics reports how long the database's cumulative statistics have
// been accumulating, since every usage-based check measures over that window.
package dbstatistics

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

// minMatureWindowSeconds is the shortest window that reflects a full weekly cycle.
// Below it, usage-based checks see a partial workload: batch jobs and end-of-week
// reporting may not have run at all.
const minMatureWindowSeconds = 7 * 24 * 60 * 60

// dependentChecks read pg_stat_database-backed counters and therefore measure over
// the window this check reports.
var dependentChecks = []string{
	"index-usage",
	"table-seq-scans",
	"cache-efficiency",
	"temp-usage",
}

type DBStatisticsQueries interface {
	DBStatistics(context.Context) (db.DBStatisticsRow, error)
}

type checker struct {
	queries DBStatisticsQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "db-statistics",
		Name:        "DB Statistics",
		Description: "Reports how long database counters have been accumulating for usage-based analysis",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries DBStatisticsQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	row, err := c.queries.DBStatistics(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	// A NULL stats_reset is not evidence of a long window. Counters are also zeroed
	// by an unclean shutdown, a crash, or a freshly built replica, and none of those
	// records a timestamp — so the period is unknown, not infinite. Report the fact
	// without inferring maturity from it.
	if !row.StatsReset.Valid {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "DB statistics: never reset",
			Severity: check.SeverityPass,
			Details:  "No reset recorded for this database. Counters are also zeroed without a timestamp by an unclean shutdown or a rebuilt replica, so the period they cover cannot be determined from here.",
		})

		return report, nil
	}

	// age_seconds is measured against the server's clock and is not truncated to
	// whole days, so a reset an hour ago reads "1h" rather than the "0 days" the
	// old day count produced.
	age := row.AgeSeconds.Int64
	window := check.FormatDurationSec(age)

	if age >= minMatureWindowSeconds {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     fmt.Sprintf("DB statistics: %s since last reset", window),
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Counters have accumulated since %s.", row.StatsReset.Time.Format(time.RFC3339)),
		})

		return report, nil
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     fmt.Sprintf("DB statistics: %s since last reset", window),
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf(
			"Counters cover %s, less than the %d days needed to reflect a full weekly cycle.\n\nUsage-based checks measure over this window and may under-report:\n%s",
			window,
			minMatureWindowSeconds/(24*60*60),
			strings.Join(dependentChecks, "\n"),
		),
	})

	return report, nil
}
