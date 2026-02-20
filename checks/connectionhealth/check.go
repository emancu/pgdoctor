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

	idleRatioWarnPercent = 50.0
	idleRatioFailPercent = 75.0
	// Skip idle ratio check below this threshold to avoid false positives
	// in low-traffic databases.
	minConnectionsForIdleCheck = int64(20)

	longIdleWarnCount = 10
	longIdleFailCount = 50

	// Pool pressure thresholds - detect when queries may be waiting for connections.
	poolPressureActivePercent = 90.0 // Warn when >90% of connections are active
	poolPressureMinIdleWarn   = 3    // AND fewer than 3 idle connections
	poolPressureMinIdleFail   = 1    // Critical when only 0-1 idle connections
	poolPressureMinTotalConns = 10   // Skip check if fewer than 10 total connections
)

const (
	// Fallback timeout when idle_in_transaction_session_timeout is disabled (0).
	idleTxnDefaultTimeoutSeconds = int64(300) // 5 minutes
)

type ConnectionHealthQueries interface {
	ConnectionStats(context.Context) (db.ConnectionStatsRow, error)
	IdleInTransaction(context.Context) ([]db.IdleInTransactionRow, error)
	LongIdleConnections(context.Context) ([]db.LongIdleConnectionsRow, error)
}

type checker struct {
	queries ConnectionHealthQueries
}

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryConfigs,
		CheckID:     "connection-health",
		Name:        "Connection Health",
		Description: "Monitors connection pool saturation, idle ratios, and stuck transactions",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries ConnectionHealthQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	meta := check.InstanceMetadataFromContext(ctx)
	// EMANCU: Should I check metadata here, or guarantee that it panics if no metadata available?
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

	// Add connection overview first (informational)
	// TODO: Move this table at the parent/report. Only one table shold be displayed
	// Or to `dba` command.
	addConnectionOverview(stats, report)

	checkConnectionSaturation(meta, stats, report)
	checkPoolPressure(meta, stats, report)
	checkIdleRatio(meta, stats, report)
	checkIdleInTransaction(idleTxns, report)
	checkLongIdleConnections(longIdle, report)

	return report, nil
}

// addConnectionOverview adds an informational finding showing key connection metrics.
// EMANCU: I dont like this, I would like to have it in verbose only or something like
// that for the parent check only!
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
		Severity: check.SeverityOK,
		Details:  details,
	})
}

// checkConnectionSaturation checks if we're running out of available connections.
func checkConnectionSaturation(meta *check.InstanceMetadata, stats db.ConnectionStatsRow, report *check.Report) {
	maxConns := stats.MaxConnections.Int32
	reserved := stats.ReservedConnections.Int32
	available := maxConns - reserved
	used := stats.TotalConnections.Int64

	saturationPercent := float64(used) / float64(available) * 100

	if saturationPercent < saturationWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "connection-saturation",
			Name:     "Connection Saturation",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Connection usage at %.1f%% (%d/%d available)", saturationPercent, used, available),
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
func checkPoolPressure(meta *check.InstanceMetadata, stats db.ConnectionStatsRow, report *check.Report) {
	total := stats.TotalConnections.Int64
	active := stats.ActiveConnections.Int64
	idle := stats.IdleConnections.Int64

	// Skip check if too few connections to be meaningful
	if total < poolPressureMinTotalConns {
		report.AddFinding(check.Finding{
			ID:       "pool-pressure",
			Name:     "Connection Pool Pressure",
			Severity: check.SeverityOK,
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
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Pool has capacity: %d active (%.1f%%), %d idle connections available", active, activePercent, idle),
		})
		return
	}

	// Determine severity based on how few idle connections remain
	severity := check.SeverityWarn
	if idle <= poolPressureMinIdleFail {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "pool-pressure",
		Name:     "Connection Pool Pressure",
		Severity: severity,
		Details:  fmt.Sprintf("Pool under pressure: %d active (%.1f%%), only %d idle - new queries may wait", active, activePercent, idle),
	})
}

// checkIdleRatio detects when too many connections are idle (potential pool misconfiguration).
func checkIdleRatio(meta *check.InstanceMetadata, stats db.ConnectionStatsRow, report *check.Report) {
	total := stats.TotalConnections.Int64
	idle := stats.IdleConnections.Int64

	// Skip check if too few connections to be meaningful.
	if total < minConnectionsForIdleCheck {
		report.AddFinding(check.Finding{
			ID:       "idle-ratio",
			Name:     "Idle Connection Ratio",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Only %d total connections, idle ratio check skipped", total),
		})
		return
	}

	idlePercent := float64(idle) / float64(total) * 100

	if idlePercent < idleRatioWarnPercent {
		report.AddFinding(check.Finding{
			ID:       "idle-ratio",
			Name:     "Idle Connection Ratio",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("Idle ratio at %.1f%% (%d/%d connections idle)", idlePercent, idle, total),
		})
		return
	}

	severity := check.SeverityWarn
	if idlePercent >= idleRatioFailPercent {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "idle-ratio",
		Name:     "Idle Connection Ratio",
		Severity: severity,
		Details:  fmt.Sprintf("High idle ratio: %.1f%% of connections (%d/%d) are idle", idlePercent, idle, total),
	})
}

func checkIdleInTransaction(rows []db.IdleInTransactionRow, report *check.Report) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "idle-in-transaction",
			Name:     "Idle In Transaction",
			Severity: check.SeverityOK,
			Details:  "No connections stuck in 'idle in transaction' state",
		})
		return
	}

	// Get timeout from first row (same for all rows). If disabled (0), use 5 minute default.
	timeoutSeconds := rows[0].TimeoutMs.Int64 / 1000
	if timeoutSeconds == 0 {
		timeoutSeconds = idleTxnDefaultTimeoutSeconds
	}
	warnThreshold := timeoutSeconds / 2
	failThreshold := timeoutSeconds

	// Filter rows that meet the warn threshold.
	var problematic []db.IdleInTransactionRow
	for _, row := range rows {
		if row.TransactionDurationSeconds.Int64 >= warnThreshold {
			problematic = append(problematic, row)
		}
	}

	if len(problematic) == 0 {
		report.AddFinding(check.Finding{
			ID:       "idle-in-transaction",
			Name:     "Idle In Transaction",
			Severity: check.SeverityOK,
			Details:  "No connections stuck in 'idle in transaction' state",
		})
		return
	}

	var tableRows []check.TableRow
	severity := check.SeverityWarn

	for _, row := range problematic {
		duration := row.TransactionDurationSeconds.Int64
		rowSeverity := check.SeverityWarn
		if duration >= failThreshold {
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
			Headers: []string{"PID", "User", "Database", "Duration", "Query"},
			Rows:    tableRows,
		},
	})
}

// checkLongIdleConnections detects connections idle for >30 minutes (potential connection leak).
func checkLongIdleConnections(longIdle []db.LongIdleConnectionsRow, report *check.Report) {
	count := len(longIdle)

	if count < longIdleWarnCount {
		report.AddFinding(check.Finding{
			ID:       "long-idle",
			Name:     "Long Idle Connections",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("%d connections idle >30 minutes (threshold: %d)", count, longIdleWarnCount),
		})
		return
	}

	severity := check.SeverityWarn
	if count >= longIdleFailCount {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "long-idle",
		Name:     "Long Idle Connections",
		Severity: severity,
		Details:  fmt.Sprintf("%d connections idle >30 minutes (potential connection leak)", count),
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
