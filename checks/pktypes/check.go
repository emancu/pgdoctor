// Package pktypes validates primary key types for capacity and growth.
package pktypes

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

type PKTypesQueries interface {
	InvalidPrimaryKeyTypes(context.Context) ([]db.InvalidPrimaryKeyTypesRow, error)
}

type checker struct {
	queries PKTypesQueries
}

const (
	usagePercentFail = 50.0 // FAIL: >=50% of capacity used (urgent migration needed)
)

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategorySchema,
		CheckID:     "pk-types",
		Name:        "Primary Key Type Validation",
		Description: "Validates primary keys use bigint or UUID for sufficient growth capacity",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries PKTypesQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.InvalidPrimaryKeyTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check primary key types: %w", err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "All tables use bigint or UUID primary keys",
		})
		return report, nil
	}

	var tableRows []check.TableRow
	maxSeverity := check.SeverityWarn
	criticalCount := 0
	warningCount := 0

	for _, row := range rows {
		entry := analyzeRow(row)

		tableRows = append(tableRows, check.TableRow{
			Cells:    entry.cells,
			Severity: entry.severity,
		})

		switch entry.severity {
		case check.SeverityFail:
			criticalCount++
			maxSeverity = entry.severity
		case check.SeverityWarn:
			warningCount++
		}
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: maxSeverity,
		Details:  formatDetails(criticalCount, warningCount),
		Table: &check.Table{
			Headers: []string{"Table", "Column", "Type", "Usage %", "Rows"},
			Rows:    tableRows,
		},
	})

	return report, nil
}

type tableEntry struct {
	cells    []string
	severity check.Severity
}

func analyzeRow(row db.InvalidPrimaryKeyTypesRow) tableEntry {
	usageStr, usagePct := calculateUsage(row)

	return tableEntry{
		cells: []string{
			row.TableName.String,
			row.ColumnName.String,
			row.ColumnType.String,
			usageStr,
			check.FormatNumber(row.EstimatedRows.Int64),
		},
		severity: determineSeverity(usagePct, row.EstimatedRows.Int64),
	}
}

func calculateUsage(row db.InvalidPrimaryKeyTypesRow) (string, float64) {
	usagePct, err := row.UsagePct.Float64Value()
	if err != nil {
		return "-", 0.0
	}

	pct := usagePct.Float64 * 100

	return fmt.Sprintf("~%.1f%%", pct), pct
}

func determineSeverity(usagePct float64, estRows int64) check.Severity {
	if usagePct >= usagePercentFail {
		return check.SeverityFail
	}
	return check.SeverityWarn
}

func formatDetails(criticalCount, warningCount int) string {
	total := criticalCount + warningCount
	if criticalCount > 0 && warningCount > 0 {
		return fmt.Sprintf("Found %d table(s) with non-bigint/UUID primary keys: %d CRITICAL, %d WARNING",
			total, criticalCount, warningCount)
	}
	if criticalCount > 0 {
		return fmt.Sprintf("Found %d CRITICAL table(s) with non-bigint/UUID primary keys", criticalCount)
	}
	return fmt.Sprintf("Found %d WARNING table(s) with non-bigint/UUID primary keys", warningCount)
}
