// Package connectionhealth implements checks for PostgreSQL connection pool health.
package connectionhealth

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

const (
	saturationWarnPercent = 70.0
	saturationFailPercent = 85.0

	// Idle ratio is advisory only — connection-saturation already guards real
	// connection exhaustion, so this check can only ever be OK or WARN.
	idleRatioWarnPercent = 90.0
	// Skip idle ratio check below this threshold to avoid false positives
	// in low-traffic databases.
	minConnectionsForIdleCheck = int64(20)

	defaultLongIdleWarnCount = 100

	// Pool pressure thresholds - detect when queries may be waiting for connections.
	poolPressureActivePercent = 90.0 // Warn when >90% of connections are active
	poolPressureMinIdleWarn   = 3    // AND fewer than 3 idle connections
	poolPressureMinTotalConns = 10   // Skip check if fewer than 10 total connections

	idleTxnWarnSeconds = int64(300)  // 5 minutes
	idleTxnFailSeconds = int64(3600) // 1 hour
)

type ConnectionHealthQueries interface {
	ConnectionStats(context.Context) (db.ConnectionStatsRow, error)
	IdleInTransaction(context.Context) ([]db.IdleInTransactionRow, error)
	LongIdleConnections(context.Context) ([]db.LongIdleConnectionsRow, error)
}

type Config struct {
	LongIdleWarnCount int64 `yaml:"long_idle_warn_count"`
}

func DefaultConfig() Config {
	return Config{LongIdleWarnCount: defaultLongIdleWarnCount}
}

func (c Config) Validate() error {
	if c.LongIdleWarnCount <= 0 {
		return fmt.Errorf("long_idle_warn_count: %d is not a positive integer", c.LongIdleWarnCount)
	}
	return nil
}

type checker struct {
	queries           ConnectionHealthQueries
	longIdleWarnCount int64
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "connection-health",
		Name:        "Connection Health",
		Description: "Monitors connection pool saturation, idle ratios, and stuck transactions",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries ConnectionHealthQueries, cfg Config) check.Checker {
	return &checker{queries: queries, longIdleWarnCount: cfg.LongIdleWarnCount}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	stats, err := c.queries.ConnectionStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (stats): %w", check.CategoryConfigs, report.CheckID, err)
	}

	idleTxns, err := c.queries.IdleInTransaction(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (idle-txn): %w", check.CategoryConfigs, report.CheckID, err)
	}

	longIdle, err := c.queries.LongIdleConnections(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (long-idle): %w", check.CategoryConfigs, report.CheckID, err)
	}

	if stats.HiddenConnections.Int64 > 0 {
		checkConnectionSaturation(stats, report)
		report.AddFinding(check.Finding{
			ID:       "stats-restricted",
			Name:     "Connection State Not Visible",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf(
				"%d connections from other roles hide their state from the current role, so pool-pressure and idle-ratio cannot be evaluated and idle-in-transaction and long-idle cover only visible connections. Grant pg_read_all_stats to see every connection.",
				stats.HiddenConnections.Int64),
		})

		// Visible sessions still prove a problem, but their PASS would claim the hidden ones are healthy.
		visible := check.NewReport(Metadata())
		checkIdleInTransaction(idleTxns, visible)
		checkLongIdleConnections(longIdle, c.longIdleWarnCount, visible)
		for _, finding := range visible.Results {
			if finding.Severity > check.SeverityPass {
				report.AddFinding(finding)
			}
		}
		return report, nil
	}

	addConnectionOverview(stats, report)

	checkConnectionSaturation(stats, report)
	checkPoolPressure(stats, report)
	checkIdleRatio(stats, report)
	checkIdleInTransaction(idleTxns, report)
	checkLongIdleConnections(longIdle, c.longIdleWarnCount, report)

	return report, nil
}

// addConnectionOverview adds an informational finding showing key connection metrics.
func addConnectionOverview(stats db.ConnectionStatsRow, report *check.Report) {
	maxConns := stats.MaxConnections.Int32
	reserved := stats.ReservedConnections.Int32
	available := maxConns - reserved
	total := stats.TotalConnections.Int64
	active := stats.ActiveConnections.Int64
	idle := stats.IdleConnections.Int64
	idleInTxn := stats.IdleInTransaction.Int64
	waiting := stats.WaitingConnections.Int64

	// Format as a single-line summary with key metrics.
	details := fmt.Sprintf(
		"Connections: %d/%d available | Active: %d | Idle: %d | Idle-in-txn: %d | Waiting: %d",
		total, available, active, idle, idleInTxn, waiting,
	)

	report.AddFinding(check.Finding{
		ID:       "connection-overview",
		Name:     "Connection Overview",
		Severity: check.SeverityPass,
		Details:  details,
	})
}

// checkConnectionSaturation checks if we're running out of available connections.
func checkConnectionSaturation(stats db.ConnectionStatsRow, report *check.Report) {
	maxConns := stats.MaxConnections.Int32
	reserved := stats.ReservedConnections.Int32
	available := maxConns - reserved
	used := stats.TotalConnections.Int64

	saturationPercent := float64(used) / float64(available) * 100

	if saturationPercent < saturationWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "connection-saturation",
			Name:     fmt.Sprintf("Connection Saturation: %.1f%% (%d/%d available)", saturationPercent, used, available),
			Severity: check.SeverityPass,
		})
		return
	}

	severity := check.SeverityWarn
	if saturationPercent >= saturationFailPercent {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "connection-saturation",
		Name:     "Connection Saturation",
		Severity: severity,
		Details:  fmt.Sprintf("Connection usage at %.1f%% (%d/%d available)", saturationPercent, used, available),
	})
}

// checkPoolPressure detects when the pool has minimal idle capacity and new queries may queue.
// This is different from saturation (approaching max_connections) - pool pressure means
// all available connections are busy even if we haven't hit the limit.
func checkPoolPressure(stats db.ConnectionStatsRow, report *check.Report) {
	total := stats.TotalConnections.Int64
	active := stats.ActiveConnections.Int64
	idle := stats.IdleConnections.Int64

	// Skip check if too few connections to be meaningful
	if total < poolPressureMinTotalConns {
		report.AddFinding(check.Finding{
			ID:       "pool-pressure",
			Name:     "Connection Pool Pressure",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Only %d connections, pool pressure check skipped", total),
		})
		return
	}

	activePercent := float64(active) / float64(total) * 100

	// Check if we're under pressure: high active ratio AND very few idle connections
	if activePercent <= poolPressureActivePercent || idle >= poolPressureMinIdleWarn {
		report.AddFinding(check.Finding{
			ID:       "pool-pressure",
			Name:     "Connection Pool Pressure",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Pool has capacity: %d active (%.1f%%), %d idle connections available", active, activePercent, idle),
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "pool-pressure",
		Name:     "Connection Pool Pressure",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Pool under pressure: %d active (%.1f%%), only %d idle - new queries may wait", active, activePercent, idle),
	})
}

// checkIdleRatio detects when too many connections are idle (potential pool misconfiguration).
func checkIdleRatio(stats db.ConnectionStatsRow, report *check.Report) {
	total := stats.TotalConnections.Int64
	idle := stats.IdleConnections.Int64

	// Skip check if too few connections to be meaningful.
	if total < minConnectionsForIdleCheck {
		report.AddFinding(check.Finding{
			ID:       "idle-ratio",
			Name:     "Idle Connection Ratio",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Only %d total connections, idle ratio check skipped", total),
		})
		return
	}

	idlePercent := float64(idle) / float64(total) * 100

	if idlePercent < idleRatioWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "idle-ratio",
			Name:     "Idle Connection Ratio",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("Idle ratio at %.1f%% (%d/%d connections idle)", idlePercent, idle, total),
		})
		return
	}

	report.AddFinding(check.Finding{
		ID:       "idle-ratio",
		Name:     "Idle Connection Ratio",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("High idle ratio: %.1f%% of connections (%d/%d) are idle", idlePercent, idle, total),
	})
}

func checkIdleInTransaction(rows []db.IdleInTransactionRow, report *check.Report) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "idle-in-transaction",
			Name:     "Idle In Transaction",
			Severity: check.SeverityPass,
			Details:  "No connections stuck in 'idle in transaction' state",
		})
		return
	}

	var problematic []db.IdleInTransactionRow
	for _, row := range rows {
		if row.IdleDurationSeconds.Int64 >= idleTxnWarnSeconds {
			problematic = append(problematic, row)
		}
	}

	if len(problematic) == 0 {
		report.AddFinding(check.Finding{
			ID:       "idle-in-transaction",
			Name:     "Idle In Transaction",
			Severity: check.SeverityPass,
			Details:  "No connections stuck in 'idle in transaction' state",
		})
		return
	}

	var tableRows []check.TableRow
	severity := check.SeverityWarn

	for _, row := range problematic {
		duration := row.IdleDurationSeconds.Int64
		rowSeverity := check.SeverityWarn
		if duration >= idleTxnFailSeconds {
			rowSeverity = check.SeverityFail
			severity = check.SeverityFail
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%d", row.Pid.Int32),
				row.Username.String,
				row.DatabaseName.String,
				formatDuration(duration),
				truncateString(row.QueryPreview.String, 50),
			},
			Severity: rowSeverity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "idle-in-transaction",
		Name:     "Idle In Transaction",
		Severity: severity,
		Details:  fmt.Sprintf("Found %d connection(s) stuck in 'idle in transaction' state", len(problematic)),
		Table: &check.Table{
			Headers: []string{"PID", "User", "Database", "Idle Duration", "Query"},
			Rows:    tableRows,
		},
	})
}

func checkLongIdleConnections(longIdle []db.LongIdleConnectionsRow, warnCount int64, report *check.Report) {
	count := len(longIdle)

	severity := check.SeverityPass
	if int64(count) > warnCount {
		severity = check.SeverityWarn
	}

	report.AddFinding(check.Finding{
		ID:       "long-idle",
		Name:     "Long Idle Connections",
		Severity: severity,
		Details:  fmt.Sprintf("%d connections idle >1h", count),
	})
}

func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
