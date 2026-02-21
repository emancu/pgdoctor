// Package invalidindexes implements a check for identifying PostgreSQL indexes in an invalid state.
package invalidindexes

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

type InvalidIndexesQueries interface {
	BrokenIndexes(context.Context) ([]db.BrokenIndexesRow, error)
}

type checker struct {
	queries InvalidIndexesQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryIndexes,
		CheckID:     "invalid-indexes",
		Name:        "Invalid Indexes",
		Description: "Identifies indexes in invalid state that need rebuilding",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries InvalidIndexesQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	invalidIndexes, err := c.queries.BrokenIndexes(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryIndexes, report.CheckID, err)
	}

	if len(invalidIndexes) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
		})
		return report, nil
	}

	lines := []string{}
	for _, index := range invalidIndexes {
		lines = append(lines, fmt.Sprintf("%s\t%s", index.TableName, index.IndexName))
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("There are %d invalid indexes.\n%s\n", len(invalidIndexes), strings.Join(lines, "\n")),
	})

	return report, nil
}
