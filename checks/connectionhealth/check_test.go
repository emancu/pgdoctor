package connectionhealth_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/connectionhealth"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

// mockQueries implements ConnectionHealthQueries for testing.
type mockQueries struct {
	stats       db.ConnectionStatsRow
	idleTxns    []db.IdleInTransactionRow
	longIdle    []db.LongIdleConnectionsRow
	statsErr    error
	idleTxnsErr error
	longIdleErr error
}

func (m *mockQueries) ConnectionStats(context.Context) (db.ConnectionStatsRow, error) {
	return m.stats, m.statsErr
}

func (m *mockQueries) IdleInTransaction(context.Context) ([]db.IdleInTransactionRow, error) {
	return m.idleTxns, m.idleTxnsErr
}

func (m *mockQueries) LongIdleConnections(context.Context) ([]db.LongIdleConnectionsRow, error) {
	return m.longIdle, m.longIdleErr
}

// ctxWithPgVersion creates a context with instance metadata containing the specified PG version.
func ctxWithPgVersion(major int) context.Context {
	return check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion:      fmt.Sprintf("%d.0", major),
		EngineVersionMajor: major,
		EngineVersionMinor: 0,
	})
}

// Helper functions to create test data.

func int32Val(v int32) pgtype.Int4 {
	return pgtype.Int4{Int32: v, Valid: true}
}

func int64Val(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

func textVal(v string) pgtype.Text {
	return pgtype.Text{String: v, Valid: true}
}

// makeLongIdleRows creates a slice of LongIdleConnectionsRow for testing.
func makeLongIdleRows(count int) []db.LongIdleConnectionsRow {
	rows := make([]db.LongIdleConnectionsRow, count)
	for i := range rows {
		// #nosec G115 -- safe conversion for test data with small values
		pid := int32(1000 + i)
		rows[i] = db.LongIdleConnectionsRow{
			Pid:                  int32Val(pid),
			Username:             textVal("app_ro"),
			DatabaseName:         textVal("production"),
			ApplicationName:      textVal("myapp"),
			State:                textVal("idle"),
			IdleDurationSeconds:  int64Val(3600),
			ConnectionAgeSeconds: int64Val(7200),
		}
	}
	return rows
}

func healthyStats() db.ConnectionStatsRow {
	return db.ConnectionStatsRow{
		MaxConnections:           int32Val(100),
		ReservedConnections:      int32Val(3),
		TotalConnections:         int64Val(50),
		ActiveConnections:        int64Val(28),
		IdleConnections:          int64Val(17), // 34% idle (below 50% warn threshold)
		IdleInTransaction:        int64Val(3),
		IdleInTransactionAborted: int64Val(2),
		WaitingConnections:       int64Val(0),
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

// getFinding returns the finding with the given ID.
func getFinding(results []check.Finding, id string) *check.Finding {
	for _, r := range results {
		if r.ID == id {
			return &r
		}
	}
	return nil
}

func Test_Metadata(t *testing.T) {
	t.Parallel()

	meta := connectionhealth.Metadata()

	require.Equal(t, "connection-health", meta.CheckID)
	require.Equal(t, "Connection Health", meta.Name)
	require.Equal(t, check.CategoryConfigs, meta.Category)
	require.NotEmpty(t, meta.Description)
	require.NotEmpty(t, meta.SQL)
	require.NotEmpty(t, meta.Readme)
}

func Test_ConnectionHealth_AllOK(t *testing.T) {
	t.Parallel()

	mock := &mockQueries{
		stats:    healthyStats(),
		idleTxns: nil,
		longIdle: nil,
	}

	checker := connectionhealth.New(mock)
	report, err := checker.Check(ctxWithPgVersion(17))

	require.NoError(t, err)
	require.NotNil(t, report)

	// All 6 subchecks should report OK (overview + 5 checks).
	require.Len(t, report.Results, 6)
	require.True(t, hasResult(report.Results, "connection-overview", check.SeverityPass))
	require.True(t, hasResult(report.Results, "connection-saturation", check.SeverityPass))
	require.True(t, hasResult(report.Results, "pool-pressure", check.SeverityPass))
	require.True(t, hasResult(report.Results, "idle-ratio", check.SeverityPass))
	require.True(t, hasResult(report.Results, "idle-in-transaction", check.SeverityPass))
	require.True(t, hasResult(report.Results, "long-idle", check.SeverityPass))
}

func Test_ConnectionHealth_Saturation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		maxConns         int32
		reserved         int32
		total            int64
		expectedSeverity check.Severity
	}{
		{
			name:             "below warning threshold",
			maxConns:         100,
			reserved:         3,
			total:            50, // 51.5% of 97 available
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "at warning threshold",
			maxConns:         100,
			reserved:         3,
			total:            70, // 72.2% of 97 available
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "at fail threshold",
			maxConns:         100,
			reserved:         3,
			total:            85, // 87.6% of 97 available
			expectedSeverity: check.SeverityFail,
		},
		{
			name:             "near max",
			maxConns:         100,
			reserved:         3,
			total:            95, // 97.9% of 97 available
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.MaxConnections = int32Val(tt.maxConns)
			stats.ReservedConnections = int32Val(tt.reserved)
			stats.TotalConnections = int64Val(tt.total)

			mock := &mockQueries{
				stats: stats,
			}

			checker := connectionhealth.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "connection-saturation", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionHealth_PoolPressure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		total            int64
		active           int64
		idle             int64
		expectedSeverity check.Severity
	}{
		{
			name:             "too few connections to check",
			total:            8,
			active:           7,
			idle:             1,
			expectedSeverity: check.SeverityPass, // Skipped, too few connections
		},
		{
			name:             "healthy pool with idle capacity",
			total:            50,
			active:           30, // 60% active
			idle:             15,
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "high active but enough idle",
			total:            50,
			active:           46, // 92% active
			idle:             4,  // >= 3 idle, so OK
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "warning - high active, low idle",
			total:            50,
			active:           48, // 96% active
			idle:             2,  // < 3 idle
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "critical - high active, almost no idle",
			total:            50,
			active:           49, // 98% active
			idle:             1,  // <= 1 idle
			expectedSeverity: check.SeverityFail,
		},
		{
			name:             "critical - all connections active",
			total:            50,
			active:           50, // 100% active
			idle:             0,  // no idle
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.TotalConnections = int64Val(tt.total)
			stats.ActiveConnections = int64Val(tt.active)
			stats.IdleConnections = int64Val(tt.idle)

			mock := &mockQueries{
				stats: stats,
			}

			checker := connectionhealth.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "pool-pressure", tt.expectedSeverity),
				"expected pool-pressure to have severity %v", tt.expectedSeverity)
		})
	}
}

func Test_ConnectionHealth_IdleRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		total            int64
		idle             int64
		expectedSeverity check.Severity
	}{
		{
			name:             "too few connections to check",
			total:            15,
			idle:             14, // 93% but under 20 total
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "reported sample no longer fails",
			total:            29,
			idle:             22, // 75.9% idle - advisory, stays OK
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "healthy idle ratio",
			total:            100,
			idle:             40, // 40% (well below 90% warn threshold)
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "warning idle ratio",
			total:            29,
			idle:             27, // 93.1% (at or above 90% warn threshold)
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "very high idle ratio still only warns",
			total:            100,
			idle:             95, // 95% - no longer a FAIL tier
			expectedSeverity: check.SeverityWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.TotalConnections = int64Val(tt.total)
			stats.IdleConnections = int64Val(tt.idle)
			stats.ActiveConnections = int64Val(tt.total - tt.idle)

			mock := &mockQueries{
				stats: stats,
			}

			checker := connectionhealth.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "idle-ratio", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionHealth_IdleInTransaction(t *testing.T) {
	t.Parallel()

	// With default 5min timeout (when TimeoutMs is 0): warn at 150s, fail at 300s.
	// TimeoutMs=0 in row means use default.
	tests := []struct {
		name             string
		idleTxns         []db.IdleInTransactionRow
		expectedSeverity check.Severity
	}{
		{
			name:             "no idle in transaction",
			idleTxns:         nil,
			expectedSeverity: check.SeverityPass,
		},
		{
			name: "below warn threshold (default timeout)",
			idleTxns: []db.IdleInTransactionRow{
				{
					Pid:                        int32Val(1234),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("myapp"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(100), // 100s < 150s warn threshold
					QueryPreview:               textVal("SELECT * FROM users"),
					TimeoutMs:                  int64Val(0), // 0 = use default 5min
				},
			},
			expectedSeverity: check.SeverityPass,
		},
		{
			name: "warning level (default timeout)",
			idleTxns: []db.IdleInTransactionRow{
				{
					Pid:                        int32Val(1234),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("myapp"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(200), // 200s >= 150s warn, < 300s fail
					QueryPreview:               textVal("SELECT * FROM orders"),
					TimeoutMs:                  int64Val(0),
				},
			},
			expectedSeverity: check.SeverityWarn,
		},
		{
			name: "fail level (default timeout)",
			idleTxns: []db.IdleInTransactionRow{
				{
					Pid:                        int32Val(1234),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("myapp"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(300), // 300s = fail threshold
					QueryPreview:               textVal("BEGIN; UPDATE accounts SET balance = 0"),
					TimeoutMs:                  int64Val(0),
				},
			},
			expectedSeverity: check.SeverityFail,
		},
		{
			name: "uses DB timeout setting",
			idleTxns: []db.IdleInTransactionRow{
				{
					Pid:                        int32Val(1234),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("myapp"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(400), // 400s >= 300s warn, < 600s fail
					QueryPreview:               textVal("SELECT * FROM orders"),
					TimeoutMs:                  int64Val(600000), // 10 minutes = 600s, warn at 300s, fail at 600s
				},
			},
			expectedSeverity: check.SeverityWarn,
		},
		{
			name: "multiple with mixed severity",
			idleTxns: []db.IdleInTransactionRow{
				{
					Pid:                        int32Val(1234),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("myapp"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(200), // warning
					QueryPreview:               textVal("SELECT * FROM users"),
					TimeoutMs:                  int64Val(0),
				},
				{
					Pid:                        int32Val(5678),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("batch"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(350), // fail
					QueryPreview:               textVal("DELETE FROM logs"),
					TimeoutMs:                  int64Val(0),
				},
			},
			expectedSeverity: check.SeverityFail, // highest severity wins
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockQueries{
				stats:    healthyStats(),
				idleTxns: tt.idleTxns,
			}

			checker := connectionhealth.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "idle-in-transaction", tt.expectedSeverity))
		})
	}
}

func Test_ConnectionHealth_LongIdle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		maxConns         int32
		longIdle         []db.LongIdleConnectionsRow
		expectedSeverity check.Severity
	}{
		{
			name:             "no long idle connections",
			maxConns:         100,
			longIdle:         nil,
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "at warn threshold (100) stays OK",
			maxConns:         100,
			longIdle:         makeLongIdleRows(100),
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "above 100 warns",
			maxConns:         100,
			longIdle:         makeLongIdleRows(101),
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "at fail threshold (500) still warns",
			maxConns:         100,
			longIdle:         makeLongIdleRows(500),
			expectedSeverity: check.SeverityWarn,
		},
		{
			name:             "above 500 fails",
			maxConns:         100,
			longIdle:         makeLongIdleRows(501),
			expectedSeverity: check.SeverityFail,
		},
		{
			name:             "pooled warm floor stays OK",
			maxConns:         400,
			longIdle:         makeLongIdleRows(62), // shedul's pgbouncer min_pool_size floor
			expectedSeverity: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stats := healthyStats()
			stats.MaxConnections = int32Val(tt.maxConns)

			mock := &mockQueries{
				stats:    stats,
				longIdle: tt.longIdle,
			}

			checker := connectionhealth.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.True(t, hasResult(report.Results, "long-idle", tt.expectedSeverity))
			checktest.AssertSeverityInvariant(t, report)
		})
	}
}

func Test_ConnectionHealth_TableDetails(t *testing.T) {
	t.Parallel()

	t.Run("idle-in-transaction table has correct columns", func(t *testing.T) {
		t.Parallel()

		mock := &mockQueries{
			stats: healthyStats(),
			idleTxns: []db.IdleInTransactionRow{
				{
					Pid:                        int32Val(1234),
					Username:                   textVal("app_rw"),
					DatabaseName:               textVal("production"),
					ApplicationName:            textVal("myapp"),
					State:                      textVal("idle in transaction"),
					TransactionDurationSeconds: int64Val(200), // >= 150s warn threshold
					QueryPreview:               textVal("SELECT 1"),
					TimeoutMs:                  int64Val(0),
				},
			},
		}

		checker := connectionhealth.New(mock)
		report, err := checker.Check(ctxWithPgVersion(17))

		require.NoError(t, err)

		finding := getFinding(report.Results, "idle-in-transaction")
		require.NotNil(t, finding)
		require.NotNil(t, finding.Table)
		require.Equal(t, []string{"PID", "User", "Database", "Duration", "Query"}, finding.Table.Headers)
	})

	t.Run("connection-overview has no table (inline details)", func(t *testing.T) {
		t.Parallel()

		mock := &mockQueries{
			stats: healthyStats(),
		}

		checker := connectionhealth.New(mock)
		report, err := checker.Check(ctxWithPgVersion(17))

		require.NoError(t, err)

		finding := getFinding(report.Results, "connection-overview")
		require.NotNil(t, finding)
		require.Nil(t, finding.Table) // No table, metrics are in Details
		require.Contains(t, finding.Details, "Connections:")
	})

	t.Run("saturation has no table (info in details)", func(t *testing.T) {
		t.Parallel()

		stats := healthyStats()
		stats.TotalConnections = int64Val(80) // trigger warning

		mock := &mockQueries{
			stats: stats,
		}

		checker := connectionhealth.New(mock)
		report, err := checker.Check(ctxWithPgVersion(17))

		require.NoError(t, err)

		finding := getFinding(report.Results, "connection-saturation")
		require.NotNil(t, finding)
		require.Nil(t, finding.Table) // No table anymore
		require.Contains(t, finding.Details, "80/97")
	})

	t.Run("idle-ratio has no table (info in details)", func(t *testing.T) {
		t.Parallel()

		stats := healthyStats()
		stats.TotalConnections = int64Val(100)
		stats.IdleConnections = int64Val(92) // trigger warning (>= 90%)

		mock := &mockQueries{
			stats: stats,
		}

		checker := connectionhealth.New(mock)
		report, err := checker.Check(ctxWithPgVersion(17))

		require.NoError(t, err)

		finding := getFinding(report.Results, "idle-ratio")
		require.NotNil(t, finding)
		require.Nil(t, finding.Table) // Table was removed as redundant
	})
}

func Test_ConnectionHealth_Prescriptions(t *testing.T) {
	t.Parallel()

	// Trigger all warnings to verify prescriptions are populated.
	stats := healthyStats()
	stats.TotalConnections = int64Val(85) // saturation warning
	stats.IdleConnections = int64Val(75)  // idle ratio warning

	mock := &mockQueries{
		stats: stats,
		idleTxns: []db.IdleInTransactionRow{
			{
				Pid:                        int32Val(1234),
				Username:                   textVal("app_rw"),
				DatabaseName:               textVal("production"),
				ApplicationName:            textVal("myapp"),
				State:                      textVal("idle in transaction"),
				TransactionDurationSeconds: int64Val(200), // >= 150s warn threshold
				QueryPreview:               textVal("SELECT 1"),
				TimeoutMs:                  int64Val(0),
			},
		},
		longIdle: makeLongIdleRows(15),
	}

	checker := connectionhealth.New(mock)
	_, err := checker.Check(ctxWithPgVersion(17))

	require.NoError(t, err)
}

func Test_ConnectionHealth_ReportSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		setupMock        func() *mockQueries
		expectedSeverity check.Severity
	}{
		{
			name: "all OK subchecks",
			setupMock: func() *mockQueries {
				return &mockQueries{
					stats: healthyStats(),
				}
			},
			expectedSeverity: check.SeverityPass,
		},
		{
			name: "one warning subcheck",
			setupMock: func() *mockQueries {
				stats := healthyStats()
				stats.TotalConnections = int64Val(75) // ~77% saturation - warning
				return &mockQueries{
					stats: stats,
				}
			},
			expectedSeverity: check.SeverityWarn,
		},
		{
			name: "one fail subcheck",
			setupMock: func() *mockQueries {
				stats := healthyStats()
				stats.TotalConnections = int64Val(90) // ~93% saturation - fail
				return &mockQueries{
					stats: stats,
				}
			},
			expectedSeverity: check.SeverityFail,
		},
		{
			name: "multiple warnings, one fail",
			setupMock: func() *mockQueries {
				stats := healthyStats()
				stats.TotalConnections = int64Val(75)
				stats.IdleConnections = int64Val(65) // idle ratio warning
				return &mockQueries{
					stats: stats,
					idleTxns: []db.IdleInTransactionRow{
						{
							Pid:                        int32Val(1234),
							Username:                   textVal("app_rw"),
							DatabaseName:               textVal("production"),
							ApplicationName:            textVal("myapp"),
							State:                      textVal("idle in transaction"),
							TransactionDurationSeconds: int64Val(300), // >= 300s fail threshold
							QueryPreview:               textVal("SELECT 1"),
							TimeoutMs:                  int64Val(0),
						},
					},
				}
			},
			expectedSeverity: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := tt.setupMock()
			checker := connectionhealth.New(mock)
			report, err := checker.Check(ctxWithPgVersion(17))

			require.NoError(t, err)
			require.Equal(t, tt.expectedSeverity, report.Severity)
		})
	}
}
