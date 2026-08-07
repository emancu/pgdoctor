// Package tempusage implements checks for PostgreSQL temporary file creation.
package tempusage

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

// minWindowSeconds is the shortest counter window that yields a meaningful
// per-hour rate. Below an hour the denominator is small enough that a single
// query's temp file skews the rate into the FAIL band.
const minWindowSeconds = 3600

// TempUsageQueries defines the database queries needed by this check.
type TempUsageQueries interface {
	TempUsage(context.Context) (db.TempUsageRow, error)
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

	// Run all subchecks
	checkTempFileRate(row, report, maxSeverity)
	checkTempVolumeRate(row, report, maxSeverity)

	return report, nil
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

// checkTempFileRate identifies high temp file creation rates.
// Thresholds are tuned for production scale based on observed baselines (~0.3 files/hour).
// These catch regressions (query plan changes, work_mem resets) rather than absolute badness.
func checkTempFileRate(row db.TempUsageRow, report *check.Report, maxSeverity check.Severity) {
	rate := getTempFilesPerHour(row)

	// Threshold: 5 files/hour is ~20x typical production baseline
	// Indicates: New inefficient queries, query plan regression, or work_mem issues
	if rate < 5 {
		report.AddFinding(check.Finding{
			ID:       "temp-file-rate",
			Name:     fmt.Sprintf("Temp File Creation Rate: %.1f files/hour", rate),
			Severity: check.SeverityPass,
		})
		return
	}

	severity := check.SeverityWarn
	// Threshold: 20 files/hour is ~75x typical production baseline
	// Indicates: Serious regression or multiple problematic queries
	if rate >= 20 {
		severity = check.SeverityFail
	}
	severity = capSeverity(severity, maxSeverity)

	// Only the exact window is worth naming inline. When it is anchored to uptime
	// there is no date to give, and how that bound works belongs in the README.
	var statsResetInfo string
	if row.StatsReset.Valid {
		statsResetInfo = fmt.Sprintf(" (since %s)", row.StatsReset.Time.Format("2006-01-02"))
	}

	report.AddFinding(check.Finding{
		ID:       "temp-file-rate",
		Name:     "Temp File Creation Rate",
		Severity: severity,
		Details: fmt.Sprintf(
			"High temp file creation rate: %.1f files/hour%s\n\nTotal temp files: %d\nTotal temp data: %s",
			rate, statsResetInfo,
			row.TempFiles.Int64,
			check.FormatBytes(row.TempBytes.Int64),
		),
	})
}

// checkTempVolumeRate identifies high temp data volume.
// Thresholds are tuned for production scale based on observed baselines (~124MB/hour).
// These catch significant increases in disk spilling rather than absolute usage.
func checkTempVolumeRate(row db.TempUsageRow, report *check.Report, maxSeverity check.Severity) {
	const oneGB = float64(1024 * 1024 * 1024)
	const fiveGB = float64(5 * 1024 * 1024 * 1024)

	bytesPerHour := getTempBytesPerHour(row)

	// Threshold: 1GB/hour is ~8x typical production baseline
	// Indicates: Increased large sorts/hashes, possibly from new features or query changes
	if bytesPerHour < oneGB {
		report.AddFinding(check.Finding{
			ID:       "temp-volume-rate",
			Name:     fmt.Sprintf("Temp Data Volume Rate: %s/hour", check.FormatBytes(int64(bytesPerHour))),
			Severity: check.SeverityPass,
		})
		return
	}

	severity := check.SeverityWarn
	// Threshold: 5GB/hour is ~40x typical production baseline
	// Indicates: Major regression or multiple large queries spilling to disk
	if bytesPerHour >= fiveGB {
		severity = check.SeverityFail
	}
	severity = capSeverity(severity, maxSeverity)

	report.AddFinding(check.Finding{
		ID:       "temp-volume-rate",
		Name:     "Temp Data Volume Rate",
		Severity: severity,
		Details: fmt.Sprintf(
			"High temp data volume: %s/hour\n\nThis causes significant disk I/O and slows queries.",
			check.FormatBytes(int64(bytesPerHour)),
		),
	})
}
