package connectionefficiency_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/connectionefficiency"
	"github.com/emancu/pgdoctor/db"
)

// mockQueries implements ConnectionEfficiencyQueries for testing.
type mockQueries struct {
	stats db.SessionStatisticsRow
	err   error
}

func (m *mockQueries) SessionStatistics(context.Context) (db.SessionStatisticsRow, error) {
	return m.stats, m.err
}

// ctxWithPgVersion creates a context with instance metadata containing the specified PG version.
func ctxWithPgVersion(major int) context.Context {
	return check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion:      fmt.Sprintf("%d.0", major),
		EngineVersionMajor: major,
		EngineVersionMinor: 0,
	})
}

func float64Val(v float64) pgtype.Float8 {
	return pgtype.Float8{Float64: v, Valid: true}
}

func int64Val(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// healthyStats returns session statistics indicating healthy operation.
func healthyStats() db.SessionStatisticsRow {
	return db.SessionStatisticsRow{
		TotalSessionTimeMs:      float64Val(3600000), // 1 hour total
		TotalActiveTimeMs:       float64Val(1080000), // 30% busy (healthy)
		TotalIdleInTxnTimeMs:    float64Val(36000),   // 1% idle in txn
		TotalSessions:           int64Val(1000),
		SessionsAbandoned:       int64Val(5), // 0.5% (healthy)
		SessionsFatal:           int64Val(2), // 0.2% (healthy)
		SessionsKilled:          int64Val(3), // 0.3% (healthy)
		SessionBusyRatioPercent: float64Val(30.0),
	}
}

// hasResult checks if a finding with the given ID and severity exists.
func hasResult(results []check.Finding, id string, severity check.Severity) bool {
	for _, r := range results {
		if r.ID == id && r.Severity == severity {
			return true
		}
	}
	return false
}

func Test_ConnectionEfficiency_Metadata(t *testing.T) {
	t.Parallel()

	meta := connectionefficiency.Metadata()

	require.Equal(t, "connection-efficiency", meta.CheckID)
	require.Equal(t, "Connection Efficiency", meta.Name)
	require.Equal(t, check.CategoryConfigs, meta.Category)
	require.NotEmpty(t, meta.Description)
	require.NotEmpty(t, meta.SQL)
	require.NotEmpty(t, meta.Readme)
}

func Test_ConnectionEfficiency_AllOK(t *testing.T) {
	t.Parallel()

	mock := &mockQueries{stats: healthyStats()}
	checker := connectionefficiency.New(mock)
	report, err := checker.Check(ctxWithPgVersion(17))

	require.NoError(t, err)
	require.NotNil(t, report)

	// All 4 subchecks should report OK.
	require.Len(t, report.Results, 4)
	require.True(t, hasResult(report.Results, "busy-ratio", check.SeverityOK))
	require.True(t, hasResult(report.Results, "sessions-abandoned", check.SeverityOK))
	require.True(t, hasResult(report.Results, "sessions-fatal", check.SeverityOK))
	require.True(t, hasResult(report.Results, "sessions-killed", check.SeverityOK))
}

func Test_ConnectionEfficiency_PostgreSQL13_Skipped(t *testing.T) {
	t.Parallel()

	mock := &mockQueries{stats: healthyStats()}
	checker := connectionefficiency.New(mock)
	report, err := checker.Check(ctxWithPgVersion(13))

	require.NoError(t, err)
	require.NotNil(t, report)

	// Should return single OK finding explaining PG13 doesn't support session stats.
	require.Len(t, report.Results, 1)
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "Does not support session statistics")
	require.Contains(t, report.Results[0].Details, "requires PG14+")
}

func Test_ConnectionEfficiency_NoMetadata_Skipped(t *testing.T) {
	t.Parallel()

	mock := &mockQueries{stats: healthyStats()}
	checker := connectionefficiency.New(mock)
	report, err := checker.Check(context.Background()) // No instance metadata

	require.NoError(t, err)
	require.NotNil(t, report)

	require.Len(t, report.Results, 1)
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "Does not support session statistics")
}

func Test_ConnectionEfficiency_NoSessions(t *testing.T) {
	t.Parallel()

	mock := &mockQueries{
		stats: db.SessionStatisticsRow{
			TotalSessions: int64Val(0),
		},
	}
	checker := connectionefficiency.New(mock)
	report, err := checker.Check(ctxWithPgVersion(17))

	require.NoError(t, err)
	require.NotNil(t, report)

	// Should return single OK finding explaining no stats yet.
	require.Len(t, report.Results, 1)
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "No session statistics")
}

func Test_ConnectionEfficiency_QueryError(t *testing.T) {
	t.Parallel()

	mock := &mockQueries{
		err: fmt.Errorf("connection refused"),
	}
	checker := connectionefficiency.New(mock)
	_, err := checker.Check(ctxWithPgVersion(17))

	require.Error(t, err)
	require.Contains(t, err.Error(), "connection refused")
	require.Contains(t, err.Error(), "connection-efficiency")
}

func Test_ConnectionEfficiency_BusyRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		busyRatio        float64
		expectedSeverity check.Severity
	}{
		{
			name:             "good utilization (30%)",
			busyRatio:        30.0,
			expectedSeverity: check.SeverityOK,
		},
		{
			name:             "low utilization warning (3%)",
			busyRatio:        3.0,
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "at warn threshold (20%)",
			busyRatio:        20.0,
			expectedSeverity: check.SeverityOK, // 5% is acceptable, <5% is warn
		},
		{
			name:             "just below warn threshold (19.9%)",
			busyRatio:        19.9,
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "high utilization is OK (93%)",
			busyRatio:        93.0,
			expectedSeverity: check.SeverityOK, // High utilization is not a problem - pool pressure is checked by connection-health
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.SessionBusyRatioPercent = float64Val(tt.busyRatio)

			mock := &mockQueries{stats: stats}
			checker := connectionefficiency.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "busy-ratio", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionEfficiency_SessionsAbandoned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		totalSessions    int64
		abandoned        int64
		expectedSeverity check.Severity
	}{
		{
			name:             "healthy (0.5%)",
			totalSessions:    1000,
			abandoned:        5,
			expectedSeverity: check.SeverityOK,
		},
		{
			name:             "at warn threshold (1%)",
			totalSessions:    1000,
			abandoned:        10,
			expectedSeverity: check.SeverityOK, // <= 1% is OK
		},
		{
			name:             "warning (2%)",
			totalSessions:    1000,
			abandoned:        20,
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "critical (6%)",
			totalSessions:    1000,
			abandoned:        60,
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.TotalSessions = int64Val(tt.totalSessions)
			stats.SessionsAbandoned = int64Val(tt.abandoned)

			mock := &mockQueries{stats: stats}
			checker := connectionefficiency.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "sessions-abandoned", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionEfficiency_SessionsFatal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		totalSessions    int64
		fatal            int64
		expectedSeverity check.Severity
	}{
		{
			name:             "healthy (0.2%)",
			totalSessions:    1000,
			fatal:            2,
			expectedSeverity: check.SeverityOK,
		},
		{
			name:             "warning (2%)",
			totalSessions:    1000,
			fatal:            20,
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "critical (6%)",
			totalSessions:    1000,
			fatal:            60,
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.TotalSessions = int64Val(tt.totalSessions)
			stats.SessionsFatal = int64Val(tt.fatal)

			mock := &mockQueries{stats: stats}
			checker := connectionefficiency.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "sessions-fatal", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionEfficiency_SessionsKilled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		totalSessions    int64
		killed           int64
		expectedSeverity check.Severity
	}{
		{
			name:             "healthy (0.3%)",
			totalSessions:    1000,
			killed:           3,
			expectedSeverity: check.SeverityOK,
		},
		{
			name:             "warning (2%)",
			totalSessions:    1000,
			killed:           20,
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "critical (6%)",
			totalSessions:    1000,
			killed:           60,
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.TotalSessions = int64Val(tt.totalSessions)
			stats.SessionsKilled = int64Val(tt.killed)

			mock := &mockQueries{stats: stats}
			checker := connectionefficiency.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "sessions-killed", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionEfficiency_Prescriptions(t *testing.T) {
	t.Parallel()

	// Trigger all warnings.
	stats := db.SessionStatisticsRow{
		TotalSessionTimeMs:      float64Val(3600000),
		TotalActiveTimeMs:       float64Val(36000), // 1% busy (warn)
		TotalSessions:           int64Val(1000),
		SessionsAbandoned:       int64Val(20), // 2% (warn)
		SessionsFatal:           int64Val(20), // 2% (warn)
		SessionsKilled:          int64Val(20), // 2% (warn)
		SessionBusyRatioPercent: float64Val(1.0),
	}

	mock := &mockQueries{stats: stats}
	checker := connectionefficiency.New(mock)
	_, err := checker.Check(ctxWithPgVersion(17))

	require.NoError(t, err)
}

func Test_ConnectionEfficiency_ReportSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		stats            db.SessionStatisticsRow
		expectedSeverity check.Severity
	}{
		{
			name:             "all OK",
			stats:            healthyStats(),
			expectedSeverity: check.SeverityOK,
		},
		{
			name: "one warning",
			stats: func() db.SessionStatisticsRow {
				s := healthyStats()
				s.SessionBusyRatioPercent = float64Val(3.0) // warn
				return s
			}(),
			expectedSeverity: check.SeverityWarn,
		},
		{
			name: "one fail",
			stats: func() db.SessionStatisticsRow {
				s := healthyStats()
				s.SessionsAbandoned = int64Val(60) // 6% = fail
				return s
			}(),
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockQueries{stats: tt.stats}
			checker := connectionefficiency.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.Equal(t, tt.expectedSeverity, report.Severity)
		})
	}
}
