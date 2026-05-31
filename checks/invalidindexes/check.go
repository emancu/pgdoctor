// Package invalidindexes implements a check for identifying PostgreSQL indexes in an invalid state.
package invalidindexes

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

func New(queries InvalidIndexesQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.BrokenIndexes(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryIndexes, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
		})
		return report, nil
	}

	// One finding, all WARN: a broken index and an abandoned _ccnew/_ccold
	// leftover are both "clean this up" work, not a 3am page. The Type column
	// preserves the distinction; the fix for each lives in the README and
	// `explain` output rather than inline, to keep the run summary terse.
	var broken, leftover int
	tableRows := make([]check.TableRow, 0, len(rows))
	for _, row := range rows {
		kind := "broken"
		if row.IsLeftover {
			kind = "leftover"
			leftover++
		} else {
			broken++
		}
		tableRows = append(tableRows, check.TableRow{
			Cells:    []string{row.SchemaName, row.TableName, row.IndexName, kind},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("%s (%d broken, %d leftover)", pluralIndexes(len(rows)), broken, leftover),
		Table: &check.Table{
			Headers: []string{"Schema", "Table", "Index", "Type"},
			Rows:    tableRows,
		},
	})

	return report, nil
}

func pluralIndexes(n int) string {
	if n == 1 {
		return "1 invalid index"
	}
	return fmt.Sprintf("%d invalid indexes", n)
}
