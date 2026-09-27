// Package pktypes validates primary key types for capacity and growth.
package pktypes

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

type PKTypesQueries interface {
	InvalidPrimaryKeyTypes(context.Context) ([]db.InvalidPrimaryKeyTypesRow, error)
}

type checker struct {
	queries          PKTypesQueries
	usageWarnPercent float64
	usageFailPercent float64
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategorySchema,
		CheckID:     "pk-types",
		Name:        "Primary Key Type Validation",
		Description: "Validates primary keys use bigint or UUID for sufficient growth capacity",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries PKTypesQueries, cfg ...check.Config) check.Checker {
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

	rows, err := c.queries.InvalidPrimaryKeyTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check primary key types: %w", err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
			Details:  "All tables use bigint or UUID primary keys",
		})
		return report, nil
	}

	var tableRows []check.TableRow
	maxSeverity := check.SeverityWarn
	criticalCount := 0
	warningCount := 0
	unreadableCount := 0

	for _, row := range rows {
		if row.SequenceUnreadable.Bool {
			unreadableCount++
		}

		entry := analyzeRow(row, c.usageFailPercent)
		if entry.usagePct < c.usageWarnPercent {
			continue
		}

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

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
			Details:  withUnreadableNote("All tables use bigint or UUID primary keys", unreadableCount),
		})
	} else {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: maxSeverity,
			Details:  withUnreadableNote(formatDetails(criticalCount, warningCount), unreadableCount),
			Table: &check.Table{
				Headers: []string{"Table", "Column", "Type", "Usage %", "Rows"},
				Rows:    tableRows,
			},
		})
	}

	return report, nil
}

func withUnreadableNote(details string, unreadableCount int) string {
	if unreadableCount == 0 {
		return details
	}
	return fmt.Sprintf("%s\n%d table(s) use the row estimate: role cannot read sequence values (needs SELECT on the sequences)",
		details, unreadableCount)
}

type tableEntry struct {
	cells    []string
	severity check.Severity
	usagePct float64
}

func analyzeRow(row db.InvalidPrimaryKeyTypesRow, usageFailPercent float64) tableEntry {
	usageStr, usagePct := calculateUsage(row)

	return tableEntry{
		cells: []string{
			row.TableName,
			row.ColumnName,
			row.ColumnType,
			usageStr,
			check.FormatNumber(row.EstimatedRows),
		},
		severity: determineSeverity(usagePct, usageFailPercent, row.SequenceCurrent.Valid),
		usagePct: usagePct,
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

// A row estimate says how many ids exist, not how close the next id is to the
// type limit, so it never reaches FAIL.
func determineSeverity(usagePct, usageFailPercent float64, fromSequence bool) check.Severity {
	if fromSequence && usagePct >= usageFailPercent {
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
