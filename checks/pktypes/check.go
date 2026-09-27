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

type Config struct {
	UsageWarnPercent float64 `yaml:"usage_warn_percent"`
	UsageFailPercent float64 `yaml:"usage_fail_percent"`
}

func DefaultConfig() Config {
	return Config{UsageWarnPercent: 50, UsageFailPercent: 90}
}

func (c Config) Validate() error {
	if !(c.UsageWarnPercent > 0 && c.UsageWarnPercent <= 100) {
		return fmt.Errorf("usage_warn_percent: %v is not a percent in (0, 100]", c.UsageWarnPercent)
	}
	if !(c.UsageFailPercent > 0 && c.UsageFailPercent <= 100) {
		return fmt.Errorf("usage_fail_percent: %v is not a percent in (0, 100]", c.UsageFailPercent)
	}
	if c.UsageWarnPercent >= c.UsageFailPercent {
		return fmt.Errorf("usage_warn_percent %v must be lower than usage_fail_percent %v", c.UsageWarnPercent, c.UsageFailPercent)
	}
	return nil
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

func New(queries PKTypesQueries, cfg Config) check.Checker {
	return &checker{
		queries:          queries,
		usageWarnPercent: cfg.UsageWarnPercent,
		usageFailPercent: cfg.UsageFailPercent,
	}
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
