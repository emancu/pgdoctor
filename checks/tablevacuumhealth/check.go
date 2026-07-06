// Package tablevacuumhealth implements checks for per-table autovacuum configuration.
package tablevacuumhealth

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type TableVacuumHealthQueries interface {
	TableVacuumHealth(context.Context) ([]db.TableVacuumHealthRow, error)
}

type checker struct {
	queries TableVacuumHealthQueries
}

const (
	// Large table threshold.
	largeTableMinRows = 1_000_000 // 1M rows

	// Minimum rows for staleness checks (avoid noise from tiny tables).
	staleCheckMinRows = 1000

	// Analyze needed threshold (modifications since last analyze).
	analyzeNeededWarn = 100_000 // Warning at 100K modifications
)

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryVacuum,
		CheckID:     "table-vacuum-health",
		Name:        "Table Vacuum Health",
		Description: "Monitors per-table autovacuum configuration and activity",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TableVacuumHealthQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.TableVacuumHealth(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryVacuum, report.CheckID, err)
	}

	checkAutovacuumDisabled(rows, report)
	checkLargeTableDefaults(rows, report)
	checkAnalyzeNeeded(rows, report)

	return report, nil
}

func checkAutovacuumDisabled(rows []db.TableVacuumHealthRow, report *check.Report) {
	var tableNames []string
	for _, row := range rows {
		if hasAutovacuumDisabled(row.Reloptions.String) {
			tableNames = append(tableNames, row.TableName.String)
		}
	}

	if len(tableNames) == 0 {
		report.AddFinding(check.Finding{
			ID:       "autovacuum-disabled",
			Name:     "Autovacuum Disabled Tables",
			Severity: check.SeverityOK,
			Details:  "No tables found with autovacuum disabled",
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "autovacuum-disabled",
		Name:     "Autovacuum Disabled Tables",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with autovacuum disabled: %s", len(tableNames), strings.Join(tableNames, ", ")),
	})
}

func checkLargeTableDefaults(rows []db.TableVacuumHealthRow, report *check.Report) {
	var count int
	for _, row := range rows {
		if row.EstimatedRows.Int64 >= largeTableMinRows && isUsingDefaultSettings(row.Reloptions.String) {
			count++
		}
	}

	if count == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-table-defaults",
			Name:     "Large Table Vacuum Defaults",
			Severity: check.SeverityOK,
			Details:  "No large tables (>1M rows) found using default autovacuum settings",
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "large-table-defaults",
		Name:     "Large Table Vacuum Defaults",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("%d table(s) >1M rows use default autovacuum settings; consider per-table autovacuum_vacuum_scale_factor.", count),
	})
}

func checkAnalyzeNeeded(rows []db.TableVacuumHealthRow, report *check.Report) {
	var needsAnalyze []db.TableVacuumHealthRow
	for _, row := range rows {
		// Skip tiny tables to avoid noise.
		if row.EstimatedRows.Int64 < staleCheckMinRows {
			continue
		}

		if row.NModSinceAnalyze.Int64 >= analyzeNeededWarn {
			needsAnalyze = append(needsAnalyze, row)
		}
	}

	if len(needsAnalyze) == 0 {
		report.AddFinding(check.Finding{
			ID:       "analyze-needed",
			Name:     "Table Statistics Staleness",
			Severity: check.SeverityOK,
			Details:  "No tables found with excessive modifications since last analyze",
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range needsAnalyze {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				formatRowCount(row.EstimatedRows.Int64),
				formatRowCount(row.NModSinceAnalyze.Int64),
				fmt.Sprintf("%d", row.AutoanalyzeCount.Int64),
				formatTimeSince(getTimestamp(row.LastAnalyzeAny)),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "analyze-needed",
		Name:     "Table Statistics Staleness",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with stale statistics (many modifications since last ANALYZE)", len(needsAnalyze)),
		Table: &check.Table{
			Headers: []string{"Table", "Rows", "Mods Since Analyze", "Analyze Count", "Last Analyze"},
			Rows:    tableRows,
		},
	})
}

// Helper functions.

func hasAutovacuumDisabled(reloptions string) bool {
	return strings.Contains(strings.ToLower(reloptions), "autovacuum_enabled=false")
}

func isUsingDefaultSettings(reloptions string) bool {
	if reloptions == "" {
		return true
	}
	return !strings.Contains(strings.ToLower(reloptions), "autovacuum_vacuum_scale_factor")
}

func formatRowCount(count int64) string {
	if count >= 1_000_000_000 {
		return fmt.Sprintf("%.2fB", float64(count)/1_000_000_000)
	}
	if count >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(count)/1_000_000)
	}
	if count >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(count)/1_000)
	}
	return fmt.Sprintf("%d", count)
}

func getTimestamp(ts pgtype.Timestamptz) time.Time {
	if ts.Valid {
		return ts.Time
	}
	return time.Time{}
}

func formatTimeSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	since := time.Since(t)
	days := int(since.Hours() / 24)
	if days == 0 {
		hours := int(since.Hours())
		if hours == 0 {
			return "just now"
		}
		return fmt.Sprintf("%dh ago", hours)
	}
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}
