// Package tablevacuumhealth implements checks for per-table autovacuum configuration.
package tablevacuumhealth

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
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
	largeTableMinRows = 1_000_000
	veryLargeTableMin = 10_000_000

	staleVacuumWarnDays = 7
	staleVacuumFailDays = 25

	pendingWorkWarnFloor = 250_000
	pendingWorkFailFloor = 500_000

	neverLabel = "never"
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
	checkVacuumStale(rows, report)

	return report, nil
}

// maxRowSeverity floors at Warn: this finding only exists once a row warrants attention.
func maxRowSeverity(rows []check.TableRow) check.Severity {
	severity := check.SeverityWarn
	for _, row := range rows {
		if row.Severity > severity {
			severity = row.Severity
		}
	}
	return severity
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
			Severity: check.SeverityPass,
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
			Severity: check.SeverityPass,
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

// staleEntry is a row that tripped a staleness tier, carrying the values needed
// to render and sort it.
type staleEntry struct {
	row         db.TableVacuumHealthRow
	severity    check.Severity
	pendingWork int64
	lastVacuum  time.Time
	lastAnalyze time.Time
}

// checkVacuumStale lists tables that are both overdue AND carry real pending work,
// on either the vacuum arm (dead + inserts) or the analyze arm (mods since analyze).
func checkVacuumStale(rows []db.TableVacuumHealthRow, report *check.Report) {
	now := time.Now()
	warnCutoff := now.Add(-staleVacuumWarnDays * 24 * time.Hour)
	failCutoff := now.Add(-staleVacuumFailDays * 24 * time.Hour)

	var entries []staleEntry
	for _, row := range rows {
		vacuumWork := row.NDeadTup.Int64 + row.NInsSinceVacuum.Int64
		analyzeWork := row.NModSinceAnalyze.Int64
		lastVacuum := getTimestamp(row.LastVacuumAny)
		lastAnalyze := getTimestamp(row.LastAnalyzeAny)

		severity := staleSeverity(vacuumWork, analyzeWork, lastVacuum, lastAnalyze, warnCutoff, failCutoff)
		if severity == check.SeverityPass {
			continue
		}

		entries = append(entries, staleEntry{
			row:         row,
			severity:    severity,
			pendingWork: max(vacuumWork, analyzeWork),
			lastVacuum:  lastVacuum,
			lastAnalyze: lastAnalyze,
		})
	}

	if len(entries) == 0 {
		report.AddFinding(check.Finding{
			ID:       "vacuum-stale",
			Name:     "Stale Vacuum Activity",
			Severity: check.SeverityPass,
			Details:  "No tables are overdue for vacuum or analyze with significant pending work",
		})
		return
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].severity != entries[j].severity {
			return entries[i].severity > entries[j].severity
		}
		return entries[i].pendingWork > entries[j].pendingWork
	})

	tableRows := make([]check.TableRow, 0, len(entries))
	for _, e := range entries {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				e.row.TableName.String,
				check.FormatNumber(e.row.EstimatedRows.Int64),
				check.FormatBytes(e.row.TableSizeBytes.Int64),
				check.FormatNumber(e.pendingWork),
				formatActivity(e.lastVacuum, e.row.VacuumCount.Int64+e.row.AutovacuumCount.Int64),
				formatActivity(e.lastAnalyze, e.row.AnalyzeCount.Int64+e.row.AutoanalyzeCount.Int64),
			},
			Severity: e.severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "vacuum-stale",
		Name:     "Stale Vacuum Activity",
		Severity: maxRowSeverity(tableRows),
		Details:  fmt.Sprintf("Found %d table(s) overdue for vacuum or analyze with significant pending work", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Rows", "Size", "Pending Work", "Last Vacuum", "Last Analyze"},
			Rows:    tableRows,
		},
	})
}

// staleSeverity evaluates FAIL then WARN; either arm at a tier trips that tier,
// and a zero timestamp (never vacuumed/analyzed) is treated as infinitely stale.
func staleSeverity(vacuumWork, analyzeWork int64, lastVacuum, lastAnalyze, warnCutoff, failCutoff time.Time) check.Severity {
	if (lastVacuum.Before(failCutoff) && vacuumWork >= pendingWorkFailFloor) ||
		(lastAnalyze.Before(failCutoff) && analyzeWork >= pendingWorkFailFloor) {
		return check.SeverityFail
	}
	if (lastVacuum.Before(warnCutoff) && vacuumWork >= pendingWorkWarnFloor) ||
		(lastAnalyze.Before(warnCutoff) && analyzeWork >= pendingWorkWarnFloor) {
		return check.SeverityWarn
	}
	return check.SeverityPass
}

// formatActivity renders "<age> (<lifetime count>)", or "never" when no timestamp exists.
func formatActivity(t time.Time, count int64) string {
	if t.IsZero() {
		return neverLabel
	}
	return fmt.Sprintf("%s (%d)", formatTimeSince(t), count)
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
	return neverLabel
}

func getTimestamp(ts pgtype.Timestamptz) time.Time {
	if ts.Valid {
		return ts.Time
	}
	return time.Time{}
}

func formatTimeSince(t time.Time) string {
	if t.IsZero() {
		return neverLabel
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
