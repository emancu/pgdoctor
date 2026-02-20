// Package statisticsfreshness validates that PostgreSQL statistics are mature enough for accurate analysis.
package statisticsfreshness

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	minStatsDaysForAccuracy = 7
)

type StatisticsFreshnessQueries interface {
	StatisticsFreshness(context.Context) (db.StatisticsFreshnessRow, error)
}

type checker struct {
	queries StatisticsFreshnessQueries
}

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryConfigs,
		CheckID:     "statistics-freshness",
		Name:        "Statistics Freshness",
		Description: "Validates PostgreSQL statistics are mature enough for usage-based analysis",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries StatisticsFreshnessQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	row, err := c.queries.StatisticsFreshness(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if !row.StatsReset.Valid {
		// NULL stats_reset means statistics have NEVER been reset.
		// This is actually the ideal state - maximum data accumulation for accurate analysis.
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "Statistics have never been reset (optimal for usage-based analysis)",
		})
		return report, nil
	}

	ageDays := row.AgeDays.Int32

	if ageDays >= minStatsDaysForAccuracy {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Statistics are %d days old (mature enough for analysis)", ageDays),
		})
		return report, nil
	}

	affectedChecks := []string{
		"index-usage",
		"table-seq-scans",
		"cache-efficiency",
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf("Statistics were reset %d days ago (less than %d days recommended).\n\nThis may affect the accuracy of usage-based checks:\n%s",
			ageDays,
			minStatsDaysForAccuracy,
			strings.Join(affectedChecks, "\n")),
	})

	return report, nil
}
