// Package querystatistics reports the window pg_stat_statements counters cover.
package querystatistics

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type QueryStatisticsQueries interface {
	QueryStatisticsAvailability(context.Context) (db.QueryStatisticsAvailabilityRow, error)
	QueryStatisticsWindow(context.Context) (db.QueryStatisticsWindowRow, error)
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

	if !availability.IsLoaded && !availability.IsInstalled {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "Query statistics: pg_stat_statements not installed",
			Severity: check.SeverityPass,
			Details:  "Query-level statistics are unavailable. partition-usage depends on them.",
		})

		return report, nil
	}

	// CREATE EXTENSION succeeds without the library preloaded, but every read of the
	// views then errors, so this looks installed while producing nothing.
	if !availability.IsLoaded {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "Query statistics: pg_stat_statements not loaded",
			Severity: check.SeverityWarn,
			Details: "The extension is created but its library is not in shared_preload_libraries, " +
				"so every read of pg_stat_statements fails and checks depending on it are skipped. " +
				"Add it to shared_preload_libraries and restart.",
		})

		return report, nil
	}

	if !availability.IsInstalled {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "Query statistics: pg_stat_statements not created in this database",
			Severity: check.SeverityPass,
			Details: "The library is preloaded but the extension has not been created here. " +
				"Run CREATE EXTENSION pg_stat_statements to enable query-level statistics.",
		})

		return report, nil
	}

	// Installed into a schema this connection cannot see. Reading the view would
	// raise 42P01 and skip the check on a raw error string, so name the cause here.
	// sqlc types the probe as nullable even though "IS NOT NULL" cannot be null; an
	// unreadable result falls through rather than claiming an unobserved problem.
	if availability.IsReachable.Valid && !availability.IsReachable.Bool {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "Query statistics: pg_stat_statements outside search_path",
			Severity: check.SeverityWarn,
			Details: "The extension is installed and loaded, but its schema is not in this " +
				"connection's search_path, so its views cannot be read. Add the schema to " +
				"the search_path of the role pgdoctor connects as.",
		})

		return report, nil
	}

	row, err := c.queries.QueryStatisticsWindow(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if !row.StatsReset.Valid {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     "Query statistics: never reset",
			Severity: check.SeverityPass,
			Details:  "No reset recorded for pg_stat_statements.",
		})

		return report, nil
	}

	// Deliberately never warns on a short window. Unlike pg_stat_database, this clock
	// is reset routinely and on purpose: engineers call pg_stat_statements_reset()
	// while investigating, so a fresh window here is normal rather than a finding.
	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     fmt.Sprintf("Query statistics: %s since last reset", check.FormatDurationSec(row.AgeSeconds.Int64)),
		Severity: check.SeverityPass,
		Details:  fmt.Sprintf("Statement counters have accumulated since %s.", row.StatsReset.Time.Format(time.RFC3339)),
	})

	return report, nil
}
