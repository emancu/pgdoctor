// Package uuiddefaults detects UUID columns using random UUIDs (v4) as defaults.
package uuiddefaults

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

type UUIDDefaultsQueries interface {
	UuidColumnDefaults(context.Context) ([]db.UuidColumnDefaultsRow, error)
}

type checker struct {
	queries UUIDDefaultsQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "uuid-defaults",
		Name:        "UUID Default Value Analysis",
		Description: "Detects UUID columns using random UUIDs (v4) as defaults which cause B-tree index bloat",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries UUIDDefaultsQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.UuidColumnDefaults(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check UUID column defaults: %w", err)
	}

	var indexedRandomUUIDs []db.UuidColumnDefaultsRow

	for _, row := range rows {
		if row.HasIndex.Bool && isRandomUUIDDefault(row.DefaultExpr.String) {
			indexedRandomUUIDs = append(indexedRandomUUIDs, row)
		}
	}

	if len(indexedRandomUUIDs) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No indexed UUID columns using random v4 defaults detected",
		})
		return report, nil
	}

	var tableRows []check.TableRow
	for _, row := range indexedRandomUUIDs {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.ColumnName.String,
				row.DefaultExpr.String,
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "random-uuid-indexed",
		Name:     "Indexed UUID Columns Using Random v4 Defaults",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d indexed UUID column(s) using random v4 defaults - may cause index bloat", len(indexedRandomUUIDs)),
		Table: &check.Table{
			Headers: []string{"Table", "Column", "Default"},
			Rows:    tableRows,
		},
	})

	return report, nil
}

func isRandomUUIDDefault(expr string) bool {
	expr = strings.ToLower(expr)
	return strings.Contains(expr, "gen_random_uuid") ||
		strings.Contains(expr, "uuid_generate_v4")
}
