// Package tempusage implements checks for PostgreSQL temporary file creation.
package tempusage

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

// minWindowSeconds is the shortest counter window that yields a meaningful
// per-hour rate. Below an hour the denominator is small enough that a single
// query's temp file skews the rate into the FAIL band.
const minWindowSeconds = 3600

// TempUsageQueries defines the database queries needed by this check.
// HasPgStatStatements is generated from partition-usage's query.sql - sqlc query
// names are global to the shared db package, so it is declared, not redefined.
type TempUsageQueries interface {
	TempUsage(context.Context) (db.TempUsageRow, error)
	HasPgStatStatements(context.Context) (pgtype.Bool, error)
	TempUsageByStatement(context.Context) ([]db.TempUsageByStatementRow, error)
}

type checker struct {
	queries TempUsageQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "temp-usage",
		Name:        "Temporary File Usage",
		Description: "Monitors temporary file creation indicating work_mem exhaustion",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TempUsageQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	row, err := c.queries.TempUsage(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryConfigs, report.CheckID, err)
	}

	// Both findings below are rates over the counter window, so too short a window
	// leaves nothing meaningful to divide by. Skip rather than pass: a bare PASS
	// reads as "no temp file problem", which is not what we know.
	window, windowKnown := statsWindowSeconds(row)
	switch {
	case !windowKnown:
		return skip(report, "Counter window unknown; temp file rates cannot be computed."), nil
	case window < minWindowSeconds:
		return skip(report, fmt.Sprintf(
			"Counters cover only %s; need at least 1h of data to compute temp file rates.",
			check.FormatDurationSec(int64(window)),
		)), nil
	}

	// Without a recorded reset the window is anchored to server uptime, which is a
	// lower bound on the real one, so the rates are upper bounds. A rate under the
	// threshold is still conclusive (the true one is smaller), but one above it may
	// just be a long history divided by a short uptime — not worth a FAIL.
	maxSeverity := check.SeverityFail
	if row.WindowIsLowerBound.Bool {
		maxSeverity = check.SeverityWarn
	}

	fileSeverity := tempFileRateSeverity(row, maxSeverity)
	volumeSeverity := tempVolumeRateSeverity(row, maxSeverity)
	rateSeverity := worst(fileSeverity, volumeSeverity)

	// Naming the offenders only helps once there is something to chase, and reading
	// pg_stat_statements materialises the whole query-text corpus into a work_mem
	// tuplestore, so a healthy database does not pay for it.
	attributed := false
	if rateSeverity > check.SeverityPass {
		var err error
		if attributed, err = c.reportTopStatements(ctx, report, rateSeverity); err != nil {
			return nil, err
		}
	}

	// A rate over its threshold demands action, which is not what INFO means, so both
	// rates keep their grade whether or not the offenders could be named. The
	// statement list carries the same grade: it is the actionable one, and burying it
	// under INFO was what made this check hard to read.
	reportTempFileRate(row, report, fileSeverity)
	reportTempVolumeRate(row, report, volumeSeverity, attributed)

	return report, nil
}

func worst(a, b check.Severity) check.Severity {
	if a > b {
		return a
	}

	return b
}

// reportTopStatements attributes the temp writes to individual statements. The
// figures rank offenders and must not be summed or compared against the rates above:
// pg_stat_database measures the disk footprint of each temp file, while
// pg_stat_statements counts write I/O, which a multi-pass external sort repeats.
func (c *checker) reportTopStatements(ctx context.Context, report *check.Report, severity check.Severity) (bool, error) {
	available, err := c.queries.HasPgStatStatements(ctx)
	if err != nil {
		return false, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if !available.Bool {
		return false, nil
	}

	statements, err := c.queries.TempUsageByStatement(ctx)
	if err != nil {
		return false, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if len(statements) == 0 {
		return false, nil
	}

	rows := make([]check.TableRow, 0, len(statements))
	for _, statement := range statements {
		rows = append(rows, check.TableRow{
			Cells: []string{
				check.FormatBytes(statement.TempBytesWritten.Int64),
				check.FormatNumber(statement.Calls.Int64),
				entrySince(statement.EntrySince),
				fmt.Sprintf("%d", statement.Queryid.Int64),
				clipQueryText(statement.QueryText.String),
			},
			Severity: check.SeverityInfo,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "temp-file-sources",
		Name:     "Temp File Sources",
		Severity: severity,
		Details: fmt.Sprintf(
			"Top %d by temp write volume, which counts rewrites and so will not sum to the totals above.",
			len(rows)),
		Table: &check.Table{
			Headers: []string{"Temp Written", "Calls", "Tracked Since", "Query ID", "Query"},
			Rows:    rows,
		},
	})

	return true, nil
}

// entrySince renders when pg_stat_statements started tracking an entry. It is not a
// last-execution time - no such column exists in any version - but it bounds how much
// history a figure covers. Absent before PostgreSQL 17.
func entrySince(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return "-"
	}

	return ts.Time.Format("2006-01-02")
}

// clipQueryText shortens a normalized statement to one terminal line, counting runes
// so a multi-byte identifier is never split mid-character.
func clipQueryText(text string) string {
	const maxLen = 60

	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}

	return string(runes[:maxLen-1]) + "…"
}

// capSeverity holds a rate-derived severity to what the window can actually support.
func capSeverity(severity, ceiling check.Severity) check.Severity {
	if severity > ceiling {
		return ceiling
	}

	return severity
}

// skip marks the whole report unrunnable. The runner injects SeveritySkip when
// Check returns an error, but an unusable stats window is not an error: the query
// succeeded and simply carried nothing to measure over. Setting it here keeps our
// own wording instead of surfacing a raw error string. AddFinding only raises
// severity, so the assignment has to follow it.
func skip(report *check.Report, reason string) *check.Report {
	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: check.SeveritySkip,
		Details:  reason,
	})
	report.Severity = check.SeveritySkip

	return report
}

// statsWindowSeconds reports how long the counters have been accumulating, and
// whether that period is known at all. A NULL stats_reset does not mean "since the
// beginning of time": an unclean shutdown or a fresh replica also zeroes the
// counters, and neither records a timestamp.
func statsWindowSeconds(row db.TempUsageRow) (float64, bool) {
	if !row.SecondsSinceReset.Valid {
		return 0, false
	}

	f, err := row.SecondsSinceReset.Float64Value()
	if err != nil || !f.Valid {
		return 0, false
	}

	return f.Float64, true
}

func getTempFilesPerHour(row db.TempUsageRow) float64 {
	if !row.TempFilesPerHour.Valid {
		return 0
	}
	f, _ := row.TempFilesPerHour.Float64Value()
	return f.Float64
}

func getTempBytesPerHour(row db.TempUsageRow) float64 {
	if !row.TempBytesPerHour.Valid {
		return 0
	}
	f, _ := row.TempBytesPerHour.Float64Value()
	return f.Float64
}

// tempFileRateSeverity grades the temp file creation rate.
// Thresholds are tuned for production scale based on observed baselines (~0.3 files/hour).
// These catch regressions (query plan changes, work_mem resets) rather than absolute badness.
func tempFileRateSeverity(row db.TempUsageRow, maxSeverity check.Severity) check.Severity {
	rate := getTempFilesPerHour(row)

	// Threshold: 5 files/hour is ~20x typical production baseline
	// Indicates: New inefficient queries, query plan regression, or work_mem issues
	if rate < 5 {
		return check.SeverityPass
	}

	severity := check.SeverityWarn
	// Threshold: 20 files/hour is ~75x typical production baseline
	// Indicates: Serious regression or multiple problematic queries
	if rate >= 20 {
		severity = check.SeverityFail
	}

	return capSeverity(severity, maxSeverity)
}

func reportTempFileRate(row db.TempUsageRow, report *check.Report, severity check.Severity) {
	rate := getTempFilesPerHour(row)

	finding := check.Finding{
		ID:       "temp-file-rate",
		Name:     fmt.Sprintf("Temp File Creation Rate: %.1f files/hour", rate),
		Severity: severity,
	}

	if severity != check.SeverityPass {
		var since string
		if row.StatsReset.Valid {
			since = fmt.Sprintf(" (since %s)", row.StatsReset.Time.Format("2006-01-02"))
		}

		finding.Details = fmt.Sprintf("%d files totalling %s%s.",
			row.TempFiles.Int64, check.FormatBytes(row.TempBytes.Int64), since)
	}

	report.AddFinding(finding)
}

// tempVolumeRateSeverity grades the temp data volume.
// Thresholds are tuned for production scale based on observed baselines (~124MB/hour).
// These catch significant increases in disk spilling rather than absolute usage.
func tempVolumeRateSeverity(row db.TempUsageRow, maxSeverity check.Severity) check.Severity {
	const oneGB = float64(1024 * 1024 * 1024)
	const fiveGB = float64(5 * 1024 * 1024 * 1024)

	bytesPerHour := getTempBytesPerHour(row)

	// Threshold: 1GB/hour is ~8x typical production baseline
	// Indicates: Increased large sorts/hashes, possibly from new features or query changes
	if bytesPerHour < oneGB {
		return check.SeverityPass
	}

	severity := check.SeverityWarn
	// Threshold: 5GB/hour is ~40x typical production baseline
	// Indicates: Major regression or multiple large queries spilling to disk
	if bytesPerHour >= fiveGB {
		severity = check.SeverityFail
	}

	return capSeverity(severity, maxSeverity)
}

func reportTempVolumeRate(row db.TempUsageRow, report *check.Report, severity check.Severity, attributed bool) {
	finding := check.Finding{
		ID:       "temp-volume-rate",
		Name:     fmt.Sprintf("Temp Data Volume Rate: %s/hour", check.FormatBytes(int64(getTempBytesPerHour(row)))),
		Severity: severity,
	}

	// Only worth saying when nothing named the offenders: pg_stat_statements records
	// at ExecutorEnd, so a statement killed by statement_timeout never appears there
	// even though its temp file is still counted here.
	if severity > check.SeverityPass && !attributed {
		finding.Details = "No statement accounts for this; check the server log (log_temp_files) - killed queries are never recorded in pg_stat_statements."
	}

	report.AddFinding(finding)
}
