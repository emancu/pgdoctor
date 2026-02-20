// Package pgversion implements a check for PostgreSQL version compliance and support status.
package pgversion

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type VersionQueries interface {
	PGVersion(context.Context) (db.PGVersionRow, error)
}

type checker struct {
	versioner VersionQueries
}

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryConfigs,
		CheckID:     "pg-version",
		Name:        "PostgreSQL Version",
		Description: "Checks if PostgreSQL version is supported and up to date",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(versioner VersionQueries) check.Checker {
	return &checker{
		versioner: versioner,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	version, err := c.versioner.PGVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryConfigs, report.CheckID, err)
	}

	if version.Major >= 15 {
		report.AddFinding(check.Finding{
			Name:     report.Name,
			ID:       report.CheckID,
			Severity: check.SeverityOK,
		})

		return report, nil
	}

	// PostgreSQL versions below 15 are approaching or have reached end of life.
	// See: https://www.postgresql.org/support/versioning/
	severity := check.SeverityWarn
	if version.Major < 14 {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: severity,
		Details:  fmt.Sprintf("Running PostgreSQL %d which is approaching end of life. Upgrade to version 17+ recommended.\n", version.Major),
	})

	return report, nil
}
