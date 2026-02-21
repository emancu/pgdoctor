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

func New(queries TempUsageQueries) check.Checker {
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

	// Check if we have enough data (at least 1 hour since stats reset)
	secondsSinceReset := getSecondsSinceReset(row)
	if secondsSinceReset < 3600 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Statistics reset too recently (%.0f minutes ago). Need at least 1 hour of data.", secondsSinceReset/60),
		})
		return report, nil
	}

	// Run all subchecks
	checkTempFileRate(row, report)
	checkTempVolumeRate(row, report)

	return report, nil
}

func getSecondsSinceReset(row db.TempUsageRow) float64 {
	if !row.SecondsSinceReset.Valid {
		return 0
	}
	f, _ := row.SecondsSinceReset.Float64Value()
	return f.Float64
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
func checkTempFileRate(row db.TempUsageRow, report *check.Report) {
	rate := getTempFilesPerHour(row)

	// Threshold: 5 files/hour is ~20x typical production baseline
	// Indicates: New inefficient queries, query plan regression, or work_mem issues
	if rate < 5 {
		report.AddFinding(check.Finding{
			ID:       "temp-file-rate",
			Name:     "Temp File Creation Rate",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Temp file creation rate is acceptable: %.1f files/hour", rate),
		})
		return
	}

	severity := check.SeverityWarn
	// Threshold: 20 files/hour is ~75x typical production baseline
	// Indicates: Serious regression or multiple problematic queries
	if rate >= 20 {
		severity = check.SeverityFail
	}

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
func checkTempVolumeRate(row db.TempUsageRow, report *check.Report) {
	const oneGB = float64(1024 * 1024 * 1024)
	const fiveGB = float64(5 * 1024 * 1024 * 1024)

	bytesPerHour := getTempBytesPerHour(row)

	// Threshold: 1GB/hour is ~8x typical production baseline
	// Indicates: Increased large sorts/hashes, possibly from new features or query changes
	if bytesPerHour < oneGB {
		report.AddFinding(check.Finding{
			ID:       "temp-volume-rate",
			Name:     "Temp Data Volume Rate",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Temp data volume is acceptable: %s/hour", check.FormatBytes(int64(bytesPerHour))),
		})
		return
	}

	severity := check.SeverityWarn
	// Threshold: 5GB/hour is ~40x typical production baseline
	// Indicates: Major regression or multiple large queries spilling to disk
	if bytesPerHour >= fiveGB {
		severity = check.SeverityFail
	}

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
