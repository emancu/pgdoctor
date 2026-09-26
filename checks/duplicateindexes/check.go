// Package duplicateindexes implements checks for identifying duplicate and redundant indexes.
package duplicateindexes

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

const (
	prefixLargeSizeThresholdMB = 100
)

type DuplicateIndexesQueries interface {
	DuplicateIndexes(context.Context) ([]db.DuplicateIndexesRow, error)
}

type checker struct {
	queries DuplicateIndexesQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryIndexes,
		CheckID:     "duplicate-indexes",
		Name:        "Duplicate Indexes",
		Description: "Identifies exact and prefix duplicate indexes wasting disk space",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries DuplicateIndexesQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.DuplicateIndexes(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
		})
		return report, nil
	}

	checkExactDuplicates(rows, report)
	checkPrefixDuplicates(rows, report)

	return report, nil
}

func checkExactDuplicates(rows []db.DuplicateIndexesRow, report *check.Report) {
	var tableRows []check.TableRow

	for _, row := range rows {
		if row.DuplicateType.String != "exact" {
			continue
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.IndexNameA.String,
				row.IndexNameB.String,
				check.FormatBytes(row.SizeA.Int64 + row.SizeB.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "exact-duplicates",
			Name:     "Exact Duplicate Indexes",
			Severity: check.SeverityPass,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "exact-duplicates",
		Name:     "Exact Duplicate Indexes",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d exact duplicate index pairs", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Duplicate Of", "Total Size"},
			Rows:    tableRows,
		},
	})
}

func checkPrefixDuplicates(rows []db.DuplicateIndexesRow, report *check.Report) {
	var tableRows []check.TableRow

	for _, row := range rows {
		if row.DuplicateType.String != "prefix" {
			continue
		}

		sizeMB := float64(row.SizeA.Int64) / (1024 * 1024)
		rowSeverity := check.SeverityWarn
		if sizeMB > prefixLargeSizeThresholdMB {
			rowSeverity = check.SeverityFail
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.IndexNameA.String,
				row.IndexNameB.String,
				check.FormatBytes(row.SizeA.Int64),
			},
			Severity: rowSeverity,
		})
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "prefix-duplicates",
			Name:     "Prefix Duplicate Indexes",
			Severity: check.SeverityPass,
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "prefix-duplicates",
		Name:     "Prefix Duplicate Indexes",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d prefix duplicate indexes", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Prefix Of", "Size"},
			Rows:    tableRows,
		},
	})
}
