package connectionefficiency_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/connectionefficiency"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
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
		TotalIdleInTxnTimeMs: float64Val(36000), // 1% idle in txn
		TotalSessions:        int64Val(1000),
		SessionsAbandoned:    int64Val(5), // 0.5% (healthy)
		SessionsFatal:        int64Val(2), // 0.2% (healthy)
		SessionsKilled:       int64Val(3), // 0.3% (healthy)
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

	// All 3 subchecks should report OK.
	require.Len(t, report.Results, 3)
	require.True(t, hasResult(report.Results, "sessions-abandoned", check.SeverityPass))
	require.True(t, hasResult(report.Results, "sessions-fatal", check.SeverityPass))
	require.True(t, hasResult(report.Results, "sessions-killed", check.SeverityPass))
	checktest.AssertSeverityInvariant(t, report)
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
	require.Equal(t, check.SeverityPass, report.Results[0].Severity)
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
	require.Equal(t, check.SeverityPass, report.Results[0].Severity)
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
	require.Equal(t, check.SeverityPass, report.Results[0].Severity)
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
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "at warn threshold (1%)",
			totalSessions:    1000,
			abandoned:        10,
			expectedSeverity: check.SeverityPass, // <= 1% is OK
		},
		{
			name:             "warning (2%)",
			totalSessions:    1000,
			abandoned:        20,
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "high (6%) caps at warn",
			totalSessions:    1000,
			abandoned:        60,
			expectedSeverity: check.SeverityWarn,
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
			checktest.AssertSeverityInvariant(t, report)
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
			expectedSeverity: check.SeverityPass,
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
			expectedSeverity: check.SeverityPass,
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
		TotalSessions:     int64Val(1000),
		SessionsAbandoned: int64Val(20), // 2% (warn)
		SessionsFatal:     int64Val(20), // 2% (warn)
		SessionsKilled:    int64Val(20), // 2% (warn)
	}

	mock := &mockQueries{stats: stats}
	checker := connectionefficiency.New(mock)
	report, err := checker.Check(ctxWithPgVersion(17))

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
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
			expectedSeverity: check.SeverityPass,
		},
		{
			name: "one warning",
			stats: func() db.SessionStatisticsRow {
				s := healthyStats()
				s.SessionsAbandoned = int64Val(20) // 2% = warn
				return s
			}(),
			expectedSeverity: check.SeverityWarn,
		},
		{
			name: "one fail",
			stats: func() db.SessionStatisticsRow {
				s := healthyStats()
				s.SessionsFatal = int64Val(60) // 6% = fail
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
			checktest.AssertSeverityInvariant(t, report)
		})
	}
}
