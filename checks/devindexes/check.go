// Package devindexes identifies development indexes that should be cleaned up.
package devindexes

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
	usageThreshold = 1000
)

type DevIndexesQueries interface {
	DevIndexes(context.Context) ([]db.DevIndexesRow, error)
}

type checker struct {
	queries DevIndexesQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryIndexes,
		CheckID:     "dev-indexes",
		Name:        "Temporary Dev Indexes",
		Description: "Identifies temporary development indexes that should be promoted or dropped",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries DevIndexesQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.DevIndexes(ctx)
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

	var usedDevIndexes []string
	var unusedDevIndexes []string
	usedCount := 0
	unusedCount := 0

	for _, row := range rows {
		sizeMB := float64(row.IndexSizeBytes.Int64) / (1024 * 1024)
		indexInfo := fmt.Sprintf("%s on %s (scans: %d, size: %.1f MB)",
			row.IndexName.String, row.TableName.String, row.IdxScan.Int64, sizeMB)

		if row.IdxScan.Int64 >= usageThreshold {
			usedCount++
			if len(usedDevIndexes) < 10 {
				usedDevIndexes = append(usedDevIndexes, indexInfo)
			}
		} else {
			unusedCount++
			if len(unusedDevIndexes) < 10 {
				unusedDevIndexes = append(unusedDevIndexes, indexInfo)
			}
		}
	}

	if usedCount > 0 {
		details := fmt.Sprintf("Found %d development indexes with significant usage (promote to permanent):\n%s",
			usedCount,
			strings.Join(usedDevIndexes, "\n"),
		)
		if usedCount > len(usedDevIndexes) {
			details += fmt.Sprintf("\n... and %d more", usedCount-len(usedDevIndexes))
		}

		report.AddFinding(check.Finding{
			ID:       "used-dev-indexes",
			Name:     "Used Development Indexes",
			Severity: check.SeverityWarn,
			Details:  details,
		})
	}

	if unusedCount > 0 {
		details := fmt.Sprintf("Found %d development indexes with low/no usage (consider dropping):\n%s",
			unusedCount,
			strings.Join(unusedDevIndexes, "\n"),
		)
		if unusedCount > len(unusedDevIndexes) {
			details += fmt.Sprintf("\n... and %d more", unusedCount-len(unusedDevIndexes))
		}

		report.AddFinding(check.Finding{
			ID:       "unused-dev-indexes",
			Name:     "Unused Development Indexes",
			Severity: check.SeverityWarn,
			Details:  details,
		})
	}

	return report, nil
}
