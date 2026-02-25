// Package sequencehealth implements checks for PostgreSQL sequence capacity and type safety.
package sequencehealth

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

type SequenceHealthQueries interface {
	SequenceHealth(context.Context) ([]db.SequenceHealthRow, error)
}

type checker struct {
	queries SequenceHealthQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategorySchema,
		CheckID:     "sequence-health",
		Name:        "Sequence Health",
		Description: "Identifies sequences approaching exhaustion and integer columns needing bigint migration",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries SequenceHealthQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())
	rows, err := c.queries.SequenceHealth(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check sequence health: %w", err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityWarn,
			Details:  "No sequences found to check",
		})
		return report, nil
	}

	checkNearExhaustion(rows, report)
	checkIntegerShouldBeBigint(rows, report)
	checkSequenceTypeMismatch(rows, report)

	return report, nil
}

func getUsagePercent(row db.SequenceHealthRow) float64 {
	if !row.UsagePercent.Valid {
		return 0
	}
	f, _ := row.UsagePercent.Float64Value()
	return f.Float64
}

func checkNearExhaustion(rows []db.SequenceHealthRow, report *check.Report) {
	var critical []db.SequenceHealthRow // >90%
	var warning []db.SequenceHealthRow  // >75%

	for _, row := range rows {
		usage := getUsagePercent(row)
		if row.IsCyclic.Bool {
			continue // Cyclic sequences wrap around safely
		}
		if usage >= 90 {
			critical = append(critical, row)
		} else if usage >= 75 {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "near-exhaustion",
			Name:     "Sequence Exhaustion",
			Severity: check.SeverityOK,
			Details:  "All sequences have sufficient headroom (<75% used)",
		})
		return
	}

	headers := []string{"Sequence", "Table.Column", "Usage", "Remaining", "Type"}
	var tableRows []check.TableRow

	for _, row := range critical {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.SequenceName.String,
				formatTableColumn(row.TableName.String, row.ColumnName.String),
				fmt.Sprintf("%.1f%%", getUsagePercent(row)),
				check.FormatNumber(formatRemaining(row.RemainingValues.Int64)),
				row.SeqDataType.String,
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.SequenceName.String,
				formatTableColumn(row.TableName.String, row.ColumnName.String),
				fmt.Sprintf("%.1f%%", getUsagePercent(row)),
				check.FormatNumber(formatRemaining(row.RemainingValues.Int64)),
				row.SeqDataType.String,
			},
			Severity: check.SeverityWarn,
		})
	}

	severity := check.SeverityWarn
	details := fmt.Sprintf("Found %d sequence(s) nearing exhaustion", len(critical)+len(warning))
	if len(critical) > 0 {
		severity = check.SeverityFail
		details = fmt.Sprintf("CRITICAL: %d sequence(s) at >90%% capacity! %d more at >75%%", len(critical), len(warning))
	}

	report.AddFinding(check.Finding{
		ID:       "near-exhaustion",
		Name:     "Sequence Exhaustion",
		Severity: severity,
		Details:  details,
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

func checkIntegerShouldBeBigint(rows []db.SequenceHealthRow, report *check.Report) {
	var needsMigration []db.SequenceHealthRow

	for _, row := range rows {
		if row.ShouldBeBigint.Bool {
			needsMigration = append(needsMigration, row)
		}
	}

	if len(needsMigration) == 0 {
		report.AddFinding(check.Finding{
			ID:       "integer-columns",
			Name:     "Integer Column Safety",
			Severity: check.SeverityOK,
			Details:  "No integer columns with high sequence usage detected",
		})
		return
	}

	headers := []string{"Table", "Column", "Type", "Usage", "Current Value"}
	var tableRows []check.TableRow
	severity := check.SeverityWarn

	for _, row := range needsMigration {
		usage := getUsagePercent(row)
		rowSeverity := check.SeverityWarn
		if usage >= 75 {
			rowSeverity = check.SeverityFail
			severity = check.SeverityFail
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.ColumnName.String,
				row.ColumnType.String,
				fmt.Sprintf("%.1f%%", usage),
				check.FormatNumber(row.CurrentValue.Int64),
			},
			Severity: rowSeverity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "integer-columns",
		Name:     "Integer Column Safety",
		Severity: severity,
		Details:  fmt.Sprintf("Found %d integer column(s) with >50%% sequence usage that should be migrated to bigint", len(needsMigration)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

func checkSequenceTypeMismatch(rows []db.SequenceHealthRow, report *check.Report) {
	var mismatched []db.SequenceHealthRow

	for _, row := range rows {
		if row.SequenceExceedsColumn.Bool && row.ColumnType.String != "" {
			mismatched = append(mismatched, row)
		}
	}

	if len(mismatched) == 0 {
		report.AddFinding(check.Finding{
			ID:       "type-mismatch",
			Name:     "Sequence Type Mismatch",
			Severity: check.SeverityOK,
			Details:  "All sequences are properly bounded by their column types",
		})
		return
	}

	headers := []string{"Sequence", "Table.Column", "Column Type", "Seq Max", "Column Max"}
	var tableRows []check.TableRow

	for _, row := range mismatched {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.SequenceName.String,
				formatTableColumn(row.TableName.String, row.ColumnName.String),
				row.ColumnType.String,
				check.FormatNumber(row.MaxValue.Int64),
				check.FormatNumber(row.ColumnMaxValue.Int64),
			},
			Severity: check.SeverityFail,
		})
	}

	details := fmt.Sprintf("Found %d sequence(s) that can generate values exceeding their column's capacity", len(mismatched))

	report.AddFinding(check.Finding{
		ID:       "type-mismatch",
		Name:     "Sequence Type Mismatch",
		Severity: check.SeverityFail,
		Details:  details,
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// Helper functions

func formatTableColumn(table, column string) string {
	if table == "" || column == "" {
		return "-"
	}
	return fmt.Sprintf("%s.%s", table, column)
}

func formatRemaining(remaining int64) int64 {
	if remaining < 0 {
		return 0
	}
	return remaining
}
