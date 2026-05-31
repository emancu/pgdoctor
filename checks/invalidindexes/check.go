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

	var broken, leftovers []db.BrokenIndexesRow
	for _, row := range rows {
		if row.IsLeftover {
			leftovers = append(leftovers, row)
		} else {
			broken = append(broken, row)
		}
	}

	addBrokenFinding(report, broken)
	addLeftoverFinding(report, leftovers)

	return report, nil
}

// addBrokenFinding reports genuinely-broken indexes (is_leftover = false).
// These are urgent: an invalid index is dead weight the planner ignores, so it
// is a FAIL when any exist.
func addBrokenFinding(report *check.Report, rows []db.BrokenIndexesRow) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "broken-indexes",
			Name:     "Broken Indexes",
			Severity: check.SeverityOK,
			Details:  "No broken indexes found",
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "broken-indexes",
		Name:     "Broken Indexes",
		Severity: check.SeverityFail,
		Details: fmt.Sprintf(
			"Found %s. Rebuild with REINDEX INDEX CONCURRENTLY <schema>.<index>; "+
				"or remove with DROP INDEX CONCURRENTLY <schema>.<index>;.",
			pluralIndexes(len(rows)),
		),
		Table: indexTable(rows, check.SeverityFail),
	})
}

// addLeftoverFinding reports abandoned REINDEX CONCURRENTLY transients
// (_ccnew/_ccold, is_leftover = true). The original index is still valid, so
// these are non-urgent clutter: a WARN when any exist.
func addLeftoverFinding(report *check.Report, rows []db.BrokenIndexesRow) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "abandoned-leftovers",
			Name:     "Abandoned REINDEX Leftovers",
			Severity: check.SeverityOK,
			Details:  "No abandoned REINDEX CONCURRENTLY leftovers found",
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "abandoned-leftovers",
		Name:     "Abandoned REINDEX Leftovers",
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf(
			"Found %s left behind by a failed or cancelled REINDEX CONCURRENTLY. "+
				"The original index is still valid; drop the leftover with "+
				"DROP INDEX CONCURRENTLY <schema>.<index>;.",
			pluralLeftovers(len(rows)),
		),
		Table: indexTable(rows, check.SeverityWarn),
	})
}

func indexTable(rows []db.BrokenIndexesRow, severity check.Severity) *check.Table {
	tableRows := make([]check.TableRow, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, check.TableRow{
			Cells:    []string{row.SchemaName, row.TableName, row.IndexName},
			Severity: severity,
		})
	}

	return &check.Table{
		Headers: []string{"Schema", "Table", "Index"},
		Rows:    tableRows,
	}
}

func pluralIndexes(n int) string {
	if n == 1 {
		return "1 broken index"
	}
	return fmt.Sprintf("%d broken indexes", n)
}

func pluralLeftovers(n int) string {
	if n == 1 {
		return "1 abandoned leftover index"
	}
	return fmt.Sprintf("%d abandoned leftover indexes", n)
}
