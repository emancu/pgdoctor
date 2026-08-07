// Package tablevacuumhealth implements checks for per-table autovacuum configuration.
package tablevacuumhealth

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"sort"
	"strings"

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

	defaultVacuumScaleFactor = 0.2
	defaultVacuumThreshold   = 50

	secondsPerDay = 24 * 60 * 60
	secondsPerHr  = 60 * 60

	staleVacuumWarnSeconds = 7 * secondsPerDay
	staleVacuumFailSeconds = 25 * secondsPerDay

	pendingWorkWarnFloor = 250_000
	pendingWorkFailFloor = 500_000

	neverLabel = "never"
	noEstimate = "-"
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

// largeDefaultEntry carries the derived values needed to render and sort a row.
type largeDefaultEntry struct {
	row     db.TableVacuumHealthRow
	trigger int64
	pending int64
}

func checkLargeTableDefaults(rows []db.TableVacuumHealthRow, report *check.Report) {
	var entries []largeDefaultEntry
	for _, row := range rows {
		if row.EstimatedRows.Int64 >= largeTableMinRows && isUsingDefaultSettings(row.Reloptions.String) {
			entries = append(entries, largeDefaultEntry{
				row:     row,
				trigger: defaultVacuumTrigger(row.EstimatedRows.Int64),
				pending: row.NDeadTup.Int64 + row.NInsSinceVacuum.Int64,
			})
		}
	}

	if len(entries) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-table-defaults",
			Name:     "Large Table Vacuum Defaults",
			Severity: check.SeverityPass,
			Details:  "No large tables (>1M rows) found using default autovacuum settings",
		})
		return
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].pending > entries[j].pending
	})

	tableRows := make([]check.TableRow, 0, len(entries))
	for _, e := range entries {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				e.row.TableName.String,
				check.FormatNumber(e.row.EstimatedRows.Int64),
				check.FormatBytes(e.row.TableSizeBytes.Int64),
				check.FormatNumber(e.trigger),
				check.FormatNumber(e.pending),
				estNextVacuum(e.trigger, e.pending, e.row.LastVacuumAgeSeconds),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "large-table-defaults",
		Name:     "Large Table Vacuum Defaults",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d large table(s) using default autovacuum settings", len(entries)),
		Table: &check.Table{
			Headers: []string{"Table", "Rows", "Size", "Trigger At", "Pending", "Est. Next Vacuum"},
			Rows:    tableRows,
		},
	})
}

// defaultVacuumTrigger is the dead-tuple count default autovacuum waits for.
func defaultVacuumTrigger(estimatedRows int64) int64 {
	return int64(defaultVacuumScaleFactor*float64(estimatedRows)) + defaultVacuumThreshold
}

// estNextVacuum assumes dead tuples keep accumulating at their post-vacuum average
// rate. The elapsed time comes from the server, which measured it against the same
// clock that stamped the last vacuum.
func estNextVacuum(trigger, pending int64, lastVacuumAge pgtype.Int8) string {
	if pending == 0 {
		return noEstimate
	}
	if pending >= trigger {
		return "overdue"
	}
	if !lastVacuumAge.Valid || lastVacuumAge.Int64 <= 0 {
		return noEstimate
	}

	rate := float64(pending) / float64(lastVacuumAge.Int64)
	estSeconds := float64(trigger-pending) / rate

	if days := estSeconds / secondsPerDay; days >= 2 {
		return fmt.Sprintf("~%dd", int64(math.Round(days)))
	}
	if hours := estSeconds / secondsPerHr; hours >= 2 {
		return fmt.Sprintf("~%dh", int64(math.Round(hours)))
	}
	return "<1h"
}

// staleEntry is a row that tripped a staleness tier, carrying the values needed
// to render and sort it.
type staleEntry struct {
	row            db.TableVacuumHealthRow
	severity       check.Severity
	pendingWork    int64
	lastVacuumAge  pgtype.Int8
	lastAnalyzeAge pgtype.Int8
}

// checkVacuumStale lists tables that are both overdue AND carry real pending work,
// on either the vacuum arm (dead + inserts) or the analyze arm (mods since analyze).
// Staleness is judged on server-measured ages: comparing the server's timestamps
// against the CLI host's clock would let skew between the two decide the tier.
func checkVacuumStale(rows []db.TableVacuumHealthRow, report *check.Report) {
	var entries []staleEntry
	for _, row := range rows {
		vacuumWork := row.NDeadTup.Int64 + row.NInsSinceVacuum.Int64
		analyzeWork := row.NModSinceAnalyze.Int64

		severity := staleSeverity(vacuumWork, analyzeWork, row.LastVacuumAgeSeconds, row.LastAnalyzeAgeSeconds)
		if severity == check.SeverityPass {
			continue
		}

		entries = append(entries, staleEntry{
			row:            row,
			severity:       severity,
			pendingWork:    max(vacuumWork, analyzeWork),
			lastVacuumAge:  row.LastVacuumAgeSeconds,
			lastAnalyzeAge: row.LastAnalyzeAgeSeconds,
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
				formatActivity(e.lastVacuumAge, e.row.VacuumCount.Int64+e.row.AutovacuumCount.Int64),
				formatActivity(e.lastAnalyzeAge, e.row.AnalyzeCount.Int64+e.row.AutoanalyzeCount.Int64),
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
// and a table that was never vacuumed/analyzed is treated as infinitely stale.
func staleSeverity(vacuumWork, analyzeWork int64, lastVacuumAge, lastAnalyzeAge pgtype.Int8) check.Severity {
	vacuumAge := staleAge(lastVacuumAge)
	analyzeAge := staleAge(lastAnalyzeAge)

	if (vacuumAge > staleVacuumFailSeconds && vacuumWork >= pendingWorkFailFloor) ||
		(analyzeAge > staleVacuumFailSeconds && analyzeWork >= pendingWorkFailFloor) {
		return check.SeverityFail
	}
	if (vacuumAge > staleVacuumWarnSeconds && vacuumWork >= pendingWorkWarnFloor) ||
		(analyzeAge > staleVacuumWarnSeconds && analyzeWork >= pendingWorkWarnFloor) {
		return check.SeverityWarn
	}
	return check.SeverityPass
}

// staleAge reads a server-measured age, treating "never ran" as infinitely stale.
func staleAge(age pgtype.Int8) int64 {
	if !age.Valid {
		return math.MaxInt64
	}
	return age.Int64
}

// formatActivity renders "<age> (<lifetime count>)", or "never" when the action never ran.
func formatActivity(age pgtype.Int8, count int64) string {
	if !age.Valid {
		return neverLabel
	}
	return fmt.Sprintf("%s (%d)", formatAge(age.Int64), count)
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

// formatAge renders a server-measured age in whole days, falling back to hours.
func formatAge(seconds int64) string {
	days := seconds / secondsPerDay
	if days == 0 {
		hours := seconds / secondsPerHr
		if hours <= 0 {
			return "just now"
		}
		return fmt.Sprintf("%dh ago", hours)
	}
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}
