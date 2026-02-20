// Package tablevacuumhealth implements checks for per-table autovacuum configuration.
package tablevacuumhealth

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
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
	largeTableMinRows = 1_000_000  // 1M rows
	veryLargeTableMin = 10_000_000 // 10M rows

	// Stale vacuum thresholds.
	staleVacuumWarnDays = 7  // Warning after 7 days without vacuum/analyze
	staleVacuumFailDays = 25 // Error after 25 days without vacuum/analyze

	// Minimum rows for staleness checks (avoid noise from tiny tables).
	staleCheckMinRows = 1000

	// Analyze needed thresholds (modifications since last analyze).
	analyzeNeededWarn = 100_000 // Warning at 100K modifications
	analyzeNeededFail = 500_000 // Fail at 500K modifications
)

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryVacuum,
		CheckID:     "table-vacuum-health",
		Name:        "Table Vacuum Health",
		Description: "Monitors per-table autovacuum configuration and activity",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TableVacuumHealthQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
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
	checkVacuumStale(rows, report)
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
	var tablesUsingDefaults []db.TableVacuumHealthRow
	for _, row := range rows {
		if row.EstimatedRows.Int64 >= largeTableMinRows && isUsingDefaultSettings(row.Reloptions.String) {
			tablesUsingDefaults = append(tablesUsingDefaults, row)
		}
	}

	if len(tablesUsingDefaults) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-table-defaults",
			Name:     "Large Table Vacuum Defaults",
			Severity: check.SeverityOK,
			Details:  "No large tables (>1M rows) found using default autovacuum settings",
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range tablesUsingDefaults {
		severity := check.SeverityWarn
		if row.EstimatedRows.Int64 >= veryLargeTableMin {
			severity = check.SeverityFail
		}

		// Pending work = dead tuples + inserts since vacuum (PG14+)
		pendingWork := row.NDeadTup.Int64 + row.NInsSinceVacuum.Int64

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				formatRowCount(row.EstimatedRows.Int64),
				check.FormatBytes(row.TableSizeBytes.Int64),
				formatRowCount(pendingWork),
				formatTimestamp(row.LastAutovacuum),
				fmt.Sprintf("%d", row.AutovacuumCount.Int64),
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "large-table-defaults",
		Name:     "Large Table Vacuum Defaults",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d large table(s) using default autovacuum settings", len(tablesUsingDefaults)),
		Table: &check.Table{
			Headers: []string{"Table", "Rows", "Size", "Pending Work", "Last Autovacuum", "Vacuum Count"},
			Rows:    tableRows,
		},
	})
}

func checkVacuumStale(rows []db.TableVacuumHealthRow, report *check.Report) {
	now := time.Now()
	warnThreshold := now.Add(-time.Duration(staleVacuumWarnDays) * 24 * time.Hour)
	failThreshold := now.Add(-time.Duration(staleVacuumFailDays) * 24 * time.Hour)

	var staleTables []db.TableVacuumHealthRow
	for _, row := range rows {
		// Skip tiny tables to avoid noise.
		if row.EstimatedRows.Int64 < staleCheckMinRows {
			continue
		}

		lastVacuum := getTimestamp(row.LastVacuumAny)
		lastAnalyze := getTimestamp(row.LastAnalyzeAny)

		// Consider stale if either vacuum or analyze is old.
		if lastVacuum.Before(warnThreshold) || lastAnalyze.Before(warnThreshold) {
			staleTables = append(staleTables, row)
		}
	}

	if len(staleTables) == 0 {
		report.AddFinding(check.Finding{
			ID:       "vacuum-stale",
			Name:     "Stale Vacuum Activity",
			Severity: check.SeverityOK,
			Details:  "All tables have been vacuumed and analyzed within the last 7 days",
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range staleTables {
		lastVacuum := getTimestamp(row.LastVacuumAny)
		lastAnalyze := getTimestamp(row.LastAnalyzeAny)

		// Oldest activity determines severity.
		oldestActivity := lastVacuum
		if lastAnalyze.Before(oldestActivity) {
			oldestActivity = lastAnalyze
		}

		severity := check.SeverityWarn
		if oldestActivity.Before(failThreshold) {
			severity = check.SeverityFail
		}

		// Pending work = dead tuples + inserts since vacuum (PG14+)
		pendingWork := row.NDeadTup.Int64 + row.NInsSinceVacuum.Int64

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				formatRowCount(row.EstimatedRows.Int64),
				check.FormatBytes(row.TableSizeBytes.Int64),
				formatRowCount(pendingWork),
				formatTimeSince(lastVacuum),
				formatTimeSince(lastAnalyze),
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "vacuum-stale",
		Name:     "Stale Vacuum Activity",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d table(s) with stale vacuum or analyze activity", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Rows", "Size", "Pending Work", "Last Vacuum", "Last Analyze"},
			Rows:    tableRows,
		},
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
		severity := check.SeverityWarn
		if row.NModSinceAnalyze.Int64 >= analyzeNeededFail {
			severity = check.SeverityFail
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				formatRowCount(row.EstimatedRows.Int64),
				formatRowCount(row.NModSinceAnalyze.Int64),
				fmt.Sprintf("%d", row.AutoanalyzeCount.Int64),
				formatTimeSince(getTimestamp(row.LastAnalyzeAny)),
			},
			Severity: severity,
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

func formatTimestamp(ts pgtype.Timestamptz) string {
	if ts.Valid {
		return ts.Time.Format("2006-01-02 15:04")
	}
	return "never"
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
