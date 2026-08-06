// Package querystatistics reports the window pg_stat_statements counters cover.
package querystatistics

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

type QueryStatisticsQueries interface {
	QueryStatisticsAvailability(context.Context) (db.QueryStatisticsAvailabilityRow, error)
	QueryStatisticsWindow(context.Context) (pgtype.Timestamptz, error)
}

type checker struct {
	queries QueryStatisticsQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "query-statistics",
		Name:        "Query Statistics",
		Description: "Reports how long pg_stat_statements counters have been accumulating",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries QueryStatisticsQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	availability, err := c.queries.QueryStatisticsAvailability(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	switch {
	case !availability.IsLoaded && !availability.IsInstalled:
		return c.report(report, "pg_stat_statements not installed", check.SeverityPass,
			"Query-level statistics are unavailable. partition-usage depends on them."), nil

	// CREATE EXTENSION succeeds without the library preloaded, but every read of the
	// views then raises an error, so this looks installed while producing nothing.
	case !availability.IsLoaded:
		return c.report(report, "pg_stat_statements not loaded", check.SeverityWarn,
			"The extension is created but its library is not in shared_preload_libraries, "+
				"so every read of pg_stat_statements fails and checks depending on it are skipped. "+
				"Add it to shared_preload_libraries and restart."), nil

	case !availability.IsInstalled:
		return c.report(report, "pg_stat_statements not created in this database", check.SeverityPass,
			"The library is preloaded but the extension has not been created here. "+
				"Run CREATE EXTENSION pg_stat_statements to enable query-level statistics."), nil
	}

	statsReset, err := c.queries.QueryStatisticsWindow(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if !statsReset.Valid {
		return c.report(report, "never reset", check.SeverityPass,
			"No reset recorded for pg_stat_statements."), nil
	}

	// Deliberately never warns on a short window. Unlike pg_stat_database, this clock
	// is reset routinely and on purpose: engineers call pg_stat_statements_reset()
	// while investigating, so a fresh window here is normal rather than a finding.
	window := check.FormatDurationSec(int64(time.Since(statsReset.Time).Seconds()))

	return c.report(report,
		fmt.Sprintf("%s since last reset", window),
		check.SeverityPass,
		fmt.Sprintf("Statement counters have accumulated since %s.", statsReset.Time.Format(time.RFC3339)),
	), nil
}

// report attaches the single finding. The state goes in the title because renderers
// drop Details on a PASS finding, and this check is almost always passing.
func (c *checker) report(report *check.Report, title string, severity check.Severity, details string) *check.Report {
	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     "Query statistics: " + title,
		Severity: severity,
		Details:  details,
	})

	return report
}
