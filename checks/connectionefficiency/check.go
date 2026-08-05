// Package connectionefficiency implements checks for PostgreSQL connection pool efficiency.
package connectionefficiency

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

const (
	// Termination rate thresholds (as percentage of total sessions).
	terminationWarnPercent = 1.0 // >1% abnormal terminations = warning
	abandonedWarnPercent   = 7.0 // >7% abandoned sessions = warning
	terminationFailPercent = 5.0 // >5% abnormal terminations = critical
)

type ConnectionEfficiencyQueries interface {
	SessionStatistics(context.Context) (db.SessionStatisticsRow, error)
}

type checker struct {
	queryer ConnectionEfficiencyQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "connection-efficiency",
		Name:        "Connection Efficiency",
		Description: "Analyzes PostgreSQL 14+ session statistics for connection pool efficiency",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queryer ConnectionEfficiencyQueries, _ ...check.Config) check.Checker {
	return &checker{
		queryer: queryer,
	}
}

func (c *checker) Metadata() check.Metadata {
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
			Severity: check.SeverityPass,
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
			Severity: check.SeverityPass,
			Details:  "No session statistics available yet (stats may have been recently reset)",
		})
		return report, nil
	}

	// Run individual subchecks
	checkSessionsAbandoned(stats, totalSessions, report)
	checkSessionsFatal(stats, totalSessions, report)
	checkSessionsKilled(stats, totalSessions, report)

	return report, nil
}

func checkSessionsAbandoned(stats db.SessionStatisticsRow, totalSessions int64, report *check.Report) {
	sessionsAbandoned := getInt64(stats.SessionsAbandoned)
	abandonedPercent := float64(sessionsAbandoned) / float64(totalSessions) * 100

	if abandonedPercent <= abandonedWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "sessions-abandoned",
			Name:     "Abandoned Sessions",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("%d abandoned sessions (%.1f%% of total) is within normal range", sessionsAbandoned, abandonedPercent),
		})
		return
	}

	// Cumulative ratio of closed sessions: a connection-handling bug, but it cannot exhaust max_connections, so caps at WARN.
	report.AddFinding(check.Finding{
		ID:       "sessions-abandoned",
		Name:     "Abandoned Sessions",
		Severity: check.SeverityWarn,
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
			Severity: check.SeverityPass,
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
			Severity: check.SeverityPass,
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

// getInt64 safely extracts an int64 from pgtype.Int8, returning 0 if invalid.
func getInt64(i pgtype.Int8) int64 {
	if !i.Valid {
		return 0
	}
	return i.Int64
}
