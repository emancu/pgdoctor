// Package connectionefficiency implements checks for PostgreSQL connection pool efficiency.
package connectionefficiency

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	// Busy ratio threshold - only check for underutilization.
	// High utilization (even 100%) is fine as long as queries aren't queuing.
	// Pool pressure is detected by connection-health check via real-time metrics.
	busyRatioLowerPercent = 20.0 // Below 20% indicates oversized pool

	// Termination rate thresholds (as percentage of total sessions).
	terminationWarnPercent = 1.0 // >1% abnormal terminations = warning
	terminationFailPercent = 5.0 // >5% abnormal terminations = critical
)

type ConnectionEfficiencyQueries interface {
	SessionStatistics(context.Context) (db.SessionStatisticsRow, error)
}

type checker struct {
	queryer ConnectionEfficiencyQueries
}

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryConfigs,
		CheckID:     "connection-efficiency",
		Name:        "Connection Efficiency",
		Description: "Analyzes PostgreSQL 14+ session statistics for connection pool efficiency",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queryer ConnectionEfficiencyQueries) check.Checker {
	return &checker{
		queryer: queryer,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	meta := check.InstanceMetadataFromContext(ctx)

	// Skip for PostgreSQL < 14 (session statistics don't exist).
	if meta == nil || meta.EngineVersionMajor < 14 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "Does not support session statistics (requires PG14+)",
		})
		return report, nil
	}

	stats, err := c.queryer.SessionStatistics(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	totalSessions := getInt64(stats.TotalSessions)

	// Skip if no session data available yet
	if totalSessions == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No session statistics available yet (stats may have been recently reset)",
		})
		return report, nil
	}

	// Run individual subchecks
	checkBusyRatio(stats, totalSessions, report)
	checkSessionsAbandoned(stats, totalSessions, report)
	checkSessionsFatal(stats, totalSessions, report)
	checkSessionsKilled(stats, totalSessions, report)

	return report, nil
}

func checkBusyRatio(stats db.SessionStatisticsRow, totalSessions int64, report *check.Report) {
	busyRatio := getFloat64(stats.SessionBusyRatioPercent)

	if busyRatio < busyRatioLowerPercent {
		report.AddFinding(check.Finding{
			ID:       "busy-ratio",
			Name:     "Session Busy Ratio",
			Severity: check.SeverityWarn,
			Details:  fmt.Sprintf("Low busy ratio (%.1f%%) indicates oversized connection pool", busyRatio),
		})
		return
	}

	// High utilization is not a problem by itself - it means connections are well-used.
	// Real-time pool pressure (queries waiting for connections) is detected by
	// the connection-health check's pool-pressure subcheck.
	report.AddFinding(check.Finding{
		ID:       "busy-ratio",
		Name:     "Session Busy Ratio",
		Severity: check.SeverityOK,
		Details:  fmt.Sprintf("Session busy ratio at %.1f%% (healthy: above %.0f%%)", busyRatio, busyRatioLowerPercent),
	})
}

func checkSessionsAbandoned(stats db.SessionStatisticsRow, totalSessions int64, report *check.Report) {
	sessionsAbandoned := getInt64(stats.SessionsAbandoned)
	abandonedPercent := float64(sessionsAbandoned) / float64(totalSessions) * 100

	if abandonedPercent <= terminationWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "sessions-abandoned",
			Name:     "Abandoned Sessions",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("%d abandoned sessions (%.1f%% of total) is within normal range", sessionsAbandoned, abandonedPercent),
		})
		return
	}

	severity := check.SeverityWarn
	if abandonedPercent > terminationFailPercent {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "sessions-abandoned",
		Name:     "Abandoned Sessions",
		Severity: severity,
		Details:  fmt.Sprintf("High abandonment rate: %.1f%% (%d/%d sessions)", abandonedPercent, sessionsAbandoned, totalSessions),
	})
}

func checkSessionsFatal(stats db.SessionStatisticsRow, totalSessions int64, report *check.Report) {
	sessionsFatal := getInt64(stats.SessionsFatal)
	fatalPercent := float64(sessionsFatal) / float64(totalSessions) * 100

	if fatalPercent <= terminationWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "sessions-fatal",
			Name:     "Fatal Session Terminations",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("%d fatal terminations (%.1f%% of total) is within normal range", sessionsFatal, fatalPercent),
		})
		return
	}

	// High fatal termination rate
	severity := check.SeverityWarn
	if fatalPercent > terminationFailPercent {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "sessions-fatal",
		Name:     "Fatal Session Terminations",
		Severity: severity,
		Details:  fmt.Sprintf("High fatal rate: %.1f%% (%d/%d sessions) ended with server errors", fatalPercent, sessionsFatal, totalSessions),
	})
}

// checkSessionsKilled detects high rates of manually killed sessions.
func checkSessionsKilled(stats db.SessionStatisticsRow, totalSessions int64, report *check.Report) {
	sessionsKilled := getInt64(stats.SessionsKilled)
	killedPercent := float64(sessionsKilled) / float64(totalSessions) * 100

	// No issues
	if killedPercent <= terminationWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "sessions-killed",
			Name:     "Killed Sessions",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("%d killed sessions (%.1f%% of total) is within normal range", sessionsKilled, killedPercent),
		})
		return
	}

	severity := check.SeverityWarn
	if killedPercent > terminationFailPercent {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "sessions-killed",
		Name:     "Killed Sessions",
		Severity: severity,
		Details:  fmt.Sprintf("High kill rate: %.1f%% (%d/%d sessions) were manually terminated", killedPercent, sessionsKilled, totalSessions),
	})
}

// getFloat64 safely extracts a float64 from pgtype.Float8, returning 0 if invalid.
func getFloat64(f pgtype.Float8) float64 {
	if !f.Valid {
		return 0
	}
	return f.Float64
}

// getInt64 safely extracts an int64 from pgtype.Int8, returning 0 if invalid.
func getInt64(i pgtype.Int8) int64 {
	if !i.Valid {
		return 0
	}
	return i.Int64
}
