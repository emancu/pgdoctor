// Package sequencehealth implements checks for PostgreSQL sequence capacity and type safety.
package sequencehealth

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"

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
	queries          SequenceHealthQueries
	usageWarnPercent float64
	usageFailPercent float64
}

const (
	unreadableReason = "role cannot read sequence values (needs SELECT on the sequences)"

	bigintUsageWarnPercent = 75.0
	bigintUsageFailPercent = 90.0
)

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

func New(queries SequenceHealthQueries, cfg ...check.Config) check.Checker {
	c := &checker{
		queries:          queries,
		usageWarnPercent: 50,
		usageFailPercent: 90,
	}
	if len(cfg) > 0 && cfg[0] != nil {
		warn, fail := c.usageWarnPercent, c.usageFailPercent
		if v, err := parsePercent(cfg[0][Metadata().CheckID]["usage_warn_percent"]); err == nil {
			warn = v
		}
		if v, err := parsePercent(cfg[0][Metadata().CheckID]["usage_fail_percent"]); err == nil {
			fail = v
		}
		if warn < fail {
			c.usageWarnPercent, c.usageFailPercent = warn, fail
		}
	}
	return c
}

func ValidateSetting(key, value string) error {
	if key != "usage_warn_percent" && key != "usage_fail_percent" {
		return fmt.Errorf("unknown key %q", key)
	}
	if _, err := parsePercent(value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func parsePercent(value string) (float64, error) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil || !(v > 0 && v <= 100) {
		return 0, fmt.Errorf("%q is not a percent in (0, 100]", value)
	}
	return v, nil
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
			Severity: check.SeverityPass,
			Details:  "No sequences found; expected for a schema using UUID primary keys",
		})
		return report, nil
	}

	var readable []db.SequenceHealthRow
	for _, row := range rows {
		if !row.IsUnreadable.Bool {
			readable = append(readable, row)
		}
	}

	if len(readable) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeveritySkip,
			Details:  unreadableReason,
		})
		report.Severity = check.SeveritySkip
		return report, nil
	}

	checkNearExhaustion(readable, c.usageWarnPercent, c.usageFailPercent, report)
	checkIntegerShouldBeBigint(readable, c.usageWarnPercent, c.usageFailPercent, report)
	checkSequenceTypeMismatch(rows, c.usageWarnPercent, c.usageFailPercent, report)

	if unreadable := len(rows) - len(readable); unreadable > 0 {
		report.AddFinding(check.Finding{
			ID:       "unreadable-sequences",
			Name:     "Unreadable Sequences",
			Severity: check.SeverityInfo,
			Details:  fmt.Sprintf("%d sequence(s) not evaluated: %s", unreadable, unreadableReason),
		})
	}

	return report, nil
}

func getUsagePercent(row db.SequenceHealthRow) float64 {
	if !row.UsagePercent.Valid {
		return 0
	}
	f, _ := row.UsagePercent.Float64Value()
	return f.Float64
}

func getColumnUsagePercent(row db.SequenceHealthRow) float64 {
	f, _ := row.ColumnUsagePercent.Float64Value()
	return f.Float64
}

func usageSeverity(usage, warnPercent, failPercent float64) check.Severity {
	switch {
	case usage >= failPercent:
		return check.SeverityFail
	case usage >= warnPercent:
		return check.SeverityWarn
	}
	return check.SeverityInfo
}

func checkNearExhaustion(rows []db.SequenceHealthRow, usageWarnPercent, usageFailPercent float64, report *check.Report) {
	var critical []db.SequenceHealthRow
	var warning []db.SequenceHealthRow

	for _, row := range rows {
		if row.IsCyclic.Bool {
			continue // Cyclic sequences wrap around safely
		}
		warnPercent, failPercent := usageWarnPercent, usageFailPercent
		if row.SeqDataType.String == "bigint" {
			warnPercent, failPercent = bigintUsageWarnPercent, bigintUsageFailPercent
		}
		switch usageSeverity(getUsagePercent(row), warnPercent, failPercent) {
		case check.SeverityFail:
			critical = append(critical, row)
		case check.SeverityWarn:
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "near-exhaustion",
			Name:     "Sequence Exhaustion",
			Severity: check.SeverityPass,
			Details:  "All sequences have sufficient headroom",
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
		details = fmt.Sprintf("CRITICAL: %d sequence(s) near exhaustion! %d more nearing it", len(critical), len(warning))
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

func checkIntegerShouldBeBigint(rows []db.SequenceHealthRow, usageWarnPercent, usageFailPercent float64, report *check.Report) {
	var needsMigration []db.SequenceHealthRow

	for _, row := range rows {
		if row.ColumnUsagePercent.Valid && getColumnUsagePercent(row) >= usageWarnPercent {
			needsMigration = append(needsMigration, row)
		}
	}

	if len(needsMigration) == 0 {
		report.AddFinding(check.Finding{
			ID:       "integer-columns",
			Name:     "Integer Column Safety",
			Severity: check.SeverityPass,
			Details:  "No integer columns near their type limit",
		})
		return
	}

	headers := []string{"Table", "Column", "Type", "Usage", "Current Value", "FKs"}
	var tableRows []check.TableRow
	severity := check.SeverityWarn

	for _, row := range needsMigration {
		usage := getColumnUsagePercent(row)
		rowSeverity := usageSeverity(usage, usageWarnPercent, usageFailPercent)
		severity = max(severity, rowSeverity)

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.ColumnName.String,
				row.ColumnType.String,
				fmt.Sprintf("%.1f%%", usage),
				check.FormatNumber(row.CurrentValue.Int64),
				check.FormatNumber(row.FkReferenceCount.Int64),
			},
			Severity: rowSeverity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "integer-columns",
		Name:     "Integer Column Safety",
		Severity: severity,
		Details:  fmt.Sprintf("Found %d integer column(s) at >=%g%% of their type limit that should be migrated to bigint", len(needsMigration), usageWarnPercent),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

func checkSequenceTypeMismatch(rows []db.SequenceHealthRow, usageWarnPercent, usageFailPercent float64, report *check.Report) {
	var mismatched []db.SequenceHealthRow

	for _, row := range rows {
		if exceedsColumn(row) {
			mismatched = append(mismatched, row)
		}
	}

	if len(mismatched) == 0 {
		report.AddFinding(check.Finding{
			ID:       "type-mismatch",
			Name:     "Sequence Type Mismatch",
			Severity: check.SeverityPass,
			Details:  "All sequences are properly bounded by their column types",
		})
		return
	}

	headers := []string{"Sequence", "Table.Column", "Column Type", "Seq Min", "Seq Max", "Column Max", "Column Usage"}
	var tableRows []check.TableRow
	severity := check.SeverityInfo

	for _, row := range mismatched {
		rowSeverity := check.SeverityInfo
		usage := "-"
		if row.ColumnUsagePercent.Valid {
			rowSeverity = usageSeverity(getColumnUsagePercent(row), usageWarnPercent, usageFailPercent)
			usage = fmt.Sprintf("%.1f%%", getColumnUsagePercent(row))
		}
		severity = max(severity, rowSeverity)

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.SequenceName.String,
				formatTableColumn(row.TableName.String, row.ColumnName.String),
				row.ColumnType.String,
				check.FormatNumber(row.MinValue.Int64),
				check.FormatNumber(row.MaxValue.Int64),
				check.FormatNumber(row.ColumnMaxValue.Int64),
				usage,
			},
			Severity: rowSeverity,
		})
	}

	details := fmt.Sprintf("Found %d sequence(s) that can generate values exceeding their column's capacity", len(mismatched))

	report.AddFinding(check.Finding{
		ID:       "type-mismatch",
		Name:     "Sequence Type Mismatch",
		Severity: severity,
		Details:  details,
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// Helper functions

func exceedsColumn(row db.SequenceHealthRow) bool {
	return row.SequenceExceedsColumn.Bool && row.ColumnType.String != ""
}

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
