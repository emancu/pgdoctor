// Package uuidtypes validates UUID columns use native uuid type.
package uuidtypes

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

type UUIDTypesQueries interface {
	UuidColumnsAsString(context.Context) ([]db.UuidColumnsAsStringRow, error)
}

type checker struct {
	queries UUIDTypesQueries
}

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategorySchema,
		CheckID:     "uuid-types",
		Name:        "UUID Type Validation",
		Description: "Validates UUID columns use native uuid type instead of varchar/text",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries UUIDTypesQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.UuidColumnsAsString(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check UUID column types: %w", err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No UUID columns stored as string types detected",
		})
		return report, nil
	}

	var tableRows []check.TableRow
	for _, row := range rows {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName,
				row.ColumnName,
				row.ColumnType,
				check.FormatBytes(row.TableSizeBytes),
			},
			Severity: check.SeverityFail,
		})
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d UUID column(s) stored as string types", len(rows)),
		Table: &check.Table{
			Headers: []string{"Table", "Column", "Type", "Size"},
			Rows:    tableRows,
		},
	})

	return report, nil
}
