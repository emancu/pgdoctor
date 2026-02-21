// Package duplicateindexes implements checks for identifying duplicate and redundant indexes.
package duplicateindexes

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

func New(queries DuplicateIndexesQueries) check.Checker {
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
			Severity: check.SeverityOK,
		})
		return report, nil
	}

	checkExactDuplicates(rows, report)
	checkPrefixDuplicates(rows, report)

	return report, nil
}

func checkExactDuplicates(rows []db.DuplicateIndexesRow, report *check.Report) {
	var exactDuplicates []string
	exactCount := 0

	for _, row := range rows {
		if row.DuplicateType.String != "exact" {
			continue
		}

		exactCount++
		if len(exactDuplicates) < 10 {
			sizeMB := float64(row.SizeA.Int64+row.SizeB.Int64) / (1024 * 1024)
			exactDuplicates = append(exactDuplicates, fmt.Sprintf("%s: %s <-> %s (%.1f MB total)",
				row.TableName.String, row.IndexNameA.String, row.IndexNameB.String, sizeMB))
		}
	}

	if exactCount == 0 {
		report.AddFinding(check.Finding{
			ID:       "exact-duplicates",
			Name:     "Exact Duplicate Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	details := fmt.Sprintf("Found %d exact duplicate index pairs:\n%s",
		exactCount,
		strings.Join(exactDuplicates, "\n"),
	)
	if exactCount > len(exactDuplicates) {
		details += fmt.Sprintf("\n... and %d more", exactCount-len(exactDuplicates))
	}

	report.AddFinding(check.Finding{
		ID:       "exact-duplicates",
		Name:     "Exact Duplicate Indexes",
		Severity: check.SeverityWarn,
		Details:  details,
	})
}

func checkPrefixDuplicates(rows []db.DuplicateIndexesRow, report *check.Report) {
	var prefixDuplicates []string
	failCount := 0
	warnCount := 0

	for _, row := range rows {
		if row.DuplicateType.String != "prefix" {
			continue
		}

		sizeMB := float64(row.SizeA.Int64) / (1024 * 1024)
		isLarge := sizeMB > prefixLargeSizeThresholdMB

		if isLarge {
			failCount++
		} else {
			warnCount++
		}

		if len(prefixDuplicates) < 10 {
			prefixDuplicates = append(prefixDuplicates, fmt.Sprintf("%s: %s is prefix of %s (%.1f MB)",
				row.TableName.String, row.IndexNameA.String, row.IndexNameB.String, sizeMB))
		}
	}

	totalIssues := failCount + warnCount
	if totalIssues == 0 {
		report.AddFinding(check.Finding{
			ID:       "prefix-duplicates",
			Name:     "Prefix Duplicate Indexes",
			Severity: check.SeverityOK,
		})
		return
	}

	details := fmt.Sprintf("Found %d prefix duplicate indexes:\n%s",
		totalIssues,
		strings.Join(prefixDuplicates, "\n"),
	)
	if totalIssues > len(prefixDuplicates) {
		details += fmt.Sprintf("\n... and %d more", totalIssues-len(prefixDuplicates))
	}

	report.AddFinding(check.Finding{
		ID:       "prefix-duplicates",
		Name:     "Prefix Duplicate Indexes",
		Severity: check.SeverityWarn,
		Details:  details,
	})
}
