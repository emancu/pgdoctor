package replicationlag_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/replicationlag"
	"github.com/emancu/pgdoctor/db"
)

const (
	findingIDPhysicalLag      = "physical-replication-lag"
	findingIDLogicalLag       = "logical-replication-lag"
	findingIDReplicationState = "replication-state"
	findingIDWALRetention     = "wal-retention"
)

type mockQueryer struct {
	rows []db.ReplicationLagRow
	err  error
}

func (m *mockQueryer) ReplicationLag(context.Context) ([]db.ReplicationLagRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

func pgFloat8(f float64) pgtype.Float8 {
	return pgtype.Float8{Float64: f, Valid: true}
}

func pgInt8(i int64) pgtype.Int8 {
	return pgtype.Int8{Int64: i, Valid: true}
}

func healthyPhysical(appName string) db.ReplicationLagRow {
	return db.ReplicationLagRow{
		ApplicationName:  pgText(appName),
		State:            pgText("streaming"),
		ReplicationType:  pgText("physical"),
		ReplayLagBytes:   pgInt8(1024),
		ReplayLagSeconds: pgFloat8(0.1), // 100ms - healthy
		SlotName:         pgText(fmt.Sprintf("%s_slot", appName)),
		WalStatus:        pgText("reserved"),
	}
}

func healthyLogical(appName string) db.ReplicationLagRow {
	return db.ReplicationLagRow{
		ApplicationName:  pgText(appName),
		State:            pgText("streaming"),
		ReplicationType:  pgText("logical"),
		ReplayLagBytes:   pgInt8(10240),
		ReplayLagSeconds: pgFloat8(1.0), // 1s - healthy for logical
		SlotName:         pgText(fmt.Sprintf("%s_slot", appName)),
		WalStatus:        pgText("reserved"),
	}
}

func laggingPhysical(appName string, lagSeconds float64) db.ReplicationLagRow {
	return db.ReplicationLagRow{
		ApplicationName:  pgText(appName),
		State:            pgText("streaming"),
		ReplicationType:  pgText("physical"),
		ReplayLagBytes:   pgInt8(int64(lagSeconds * 1024 * 1024)), // Rough estimate
		ReplayLagSeconds: pgFloat8(lagSeconds),
		SlotName:         pgText(fmt.Sprintf("%s_slot", appName)),
		WalStatus:        pgText("reserved"),
	}
}

func laggingLogical(appName string, lagSeconds float64) db.ReplicationLagRow {
	return db.ReplicationLagRow{
		ApplicationName:  pgText(appName),
		State:            pgText("streaming"),
		ReplicationType:  pgText("logical"),
		ReplayLagBytes:   pgInt8(int64(lagSeconds * 1024 * 1024)),
		ReplayLagSeconds: pgFloat8(lagSeconds),
		SlotName:         pgText(fmt.Sprintf("%s_slot", appName)),
		WalStatus:        pgText("reserved"),
	}
}

func nonStreamingState(appName, state string) db.ReplicationLagRow {
	return db.ReplicationLagRow{
		ApplicationName:  pgText(appName),
		State:            pgText(state),
		ReplicationType:  pgText("physical"),
		ReplayLagBytes:   pgInt8(0),
		ReplayLagSeconds: pgFloat8(0),
		SlotName:         pgText(fmt.Sprintf("%s_slot", appName)),
		WalStatus:        pgText("reserved"),
	}
}

func walIssue(appName, walStatus string) db.ReplicationLagRow {
	return db.ReplicationLagRow{
		ApplicationName:  pgText(appName),
		State:            pgText("streaming"),
		ReplicationType:  pgText("logical"),
		ReplayLagBytes:   pgInt8(1024),
		ReplayLagSeconds: pgFloat8(0.5),
		SlotName:         pgText(fmt.Sprintf("%s_slot", appName)),
		WalStatus:        pgText(walStatus),
	}
}

func TestCheck_NoReplication(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.ReplicationLagRow{}}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Equal(t, "no-replication", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "No active replication")
}

func TestCheck_AllHealthy(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			healthyPhysical("standby1"),
			healthyPhysical("standby2"),
			healthyLogical("debezium"),
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should have 5 findings: replication-state, wal-retention, physical-lag, logical-lag (all OK)
	assert.Len(t, report.Results, 4)
	assert.Equal(t, check.SeverityOK, report.Severity)

	// Verify all subchecks are OK
	for _, finding := range report.Results {
		assert.Equal(t, check.SeverityOK, finding.Severity)
	}
}

func TestCheck_PhysicalReplicationLag_Warning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingPhysical("standby1", 0.5), // 500ms - warning
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.Equal(t, check.SeverityWarn, report.Severity)

	// Find physical-replication-lag finding
	var physicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDPhysicalLag {
			physicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, physicalFinding)
	assert.Equal(t, check.SeverityWarn, physicalFinding.Severity)
	assert.Contains(t, physicalFinding.Details, "1 of 1")
	assert.Contains(t, physicalFinding.Details, "lagging")
	assert.NotNil(t, physicalFinding.Table)
	assert.Len(t, physicalFinding.Table.Rows, 1)
}

func TestCheck_PhysicalReplicationLag_Fail(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingPhysical("standby1", 2.0), // 2s - fail
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.Equal(t, check.SeverityFail, report.Severity)

	var physicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDPhysicalLag {
			physicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, physicalFinding)
	assert.Equal(t, check.SeverityFail, physicalFinding.Severity)
	assert.Contains(t, physicalFinding.Details, "lagging")
}

func TestCheck_PhysicalReplicationLag_Thresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		lagSeconds     float64
		expectSeverity check.Severity
	}{
		{"under threshold", 0.1, check.SeverityOK},
		{"exactly at warn threshold", 0.25, check.SeverityWarn},
		{"over warn threshold", 0.5, check.SeverityWarn},
		{"exactly at fail threshold", 1.0, check.SeverityFail},
		{"over fail threshold", 2.0, check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				rows: []db.ReplicationLagRow{
					laggingPhysical("standby", tt.lagSeconds),
				},
			}
			checker := replicationlag.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Equal(t, tt.expectSeverity, report.Severity)
		})
	}
}

func TestCheck_LogicalReplicationLag_Warning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingLogical("debezium", 25.0), // 25s - warning (threshold: 20s)
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.Equal(t, check.SeverityWarn, report.Severity)

	var logicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLogicalLag {
			logicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, logicalFinding)
	assert.Equal(t, check.SeverityWarn, logicalFinding.Severity)
	assert.Contains(t, logicalFinding.Details, "1 of 1")
	assert.Contains(t, logicalFinding.Details, "lagging")
}

func TestCheck_LogicalReplicationLag_Fail(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingLogical("debezium", 40.0), // 40s - fail (threshold: 35s)
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.Equal(t, check.SeverityFail, report.Severity)

	var logicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLogicalLag {
			logicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, logicalFinding)
	assert.Equal(t, check.SeverityFail, logicalFinding.Severity)
}

func TestCheck_LogicalReplicationLag_Thresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		lagSeconds     float64
		expectSeverity check.Severity
	}{
		{"under threshold", 10.0, check.SeverityOK},
		{"exactly at warn threshold", 20.0, check.SeverityWarn},
		{"over warn threshold", 25.0, check.SeverityWarn},
		{"exactly at fail threshold", 35.0, check.SeverityFail},
		{"over fail threshold", 50.0, check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				rows: []db.ReplicationLagRow{
					laggingLogical("debezium", tt.lagSeconds),
				},
			}
			checker := replicationlag.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Equal(t, tt.expectSeverity, report.Severity)
		})
	}
}

func TestCheck_MixedReplicationTypes(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			healthyPhysical("standby1"),
			laggingPhysical("standby2", 0.5), // warn
			healthyLogical("debezium1"),
			laggingLogical("debezium2", 40.0), // fail (threshold: 35s)
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Highest severity should be FAIL
	assert.Equal(t, check.SeverityFail, report.Severity)

	// Should have findings for both physical and logical
	var physicalFinding, logicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDPhysicalLag {
			physicalFinding = &report.Results[i]
		}
		if report.Results[i].ID == findingIDLogicalLag {
			logicalFinding = &report.Results[i]
		}
	}

	require.NotNil(t, physicalFinding)
	require.NotNil(t, logicalFinding)

	assert.Equal(t, check.SeverityWarn, physicalFinding.Severity)
	assert.Equal(t, check.SeverityFail, logicalFinding.Severity)
}

func TestCheck_ReplicationState_Catchup(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			nonStreamingState("standby1", "catchup"),
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.Equal(t, check.SeverityWarn, report.Severity)

	var stateFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDReplicationState {
			stateFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, stateFinding)
	assert.Equal(t, check.SeverityWarn, stateFinding.Severity)
	assert.Contains(t, stateFinding.Details, "1 of 1")
	assert.Contains(t, stateFinding.Details, "not in 'streaming' state")
	assert.NotNil(t, stateFinding.Table)
}

func TestCheck_ReplicationState_BackupStopping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state string
	}{
		{"backup"},
		{"stopping"},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				rows: []db.ReplicationLagRow{
					nonStreamingState("standby1", tt.state),
				},
			}
			checker := replicationlag.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Equal(t, check.SeverityWarn, report.Severity)

			var stateFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingIDReplicationState {
					stateFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, stateFinding)
			assert.Contains(t, stateFinding.Details, "not in 'streaming' state")
		})
	}
}

func TestCheck_ReplicationState_AllStreaming(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			healthyPhysical("standby1"),
			healthyLogical("debezium1"),
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var stateFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDReplicationState {
			stateFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, stateFinding)
	assert.Equal(t, check.SeverityOK, stateFinding.Severity)
	assert.Contains(t, stateFinding.Details, "All 2 replication stream(s) are in 'streaming' state")
}

func TestCheck_WALRetention_Extended(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			walIssue("debezium", "extended"),
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.Equal(t, check.SeverityWarn, report.Severity)

	var walFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDWALRetention {
			walFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, walFinding)
	assert.Equal(t, check.SeverityWarn, walFinding.Severity)
	assert.Contains(t, walFinding.Details, "1 of 1")
	assert.Contains(t, walFinding.Details, "WAL retention issues")
	assert.NotNil(t, walFinding.Table)
}

func TestCheck_WALRetention_UnreservedLost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		walStatus string
	}{
		{"unreserved"},
		{"lost"},
	}

	for _, tt := range tests {
		t.Run(tt.walStatus, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				rows: []db.ReplicationLagRow{
					walIssue("debezium", tt.walStatus),
				},
			}
			checker := replicationlag.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Equal(t, check.SeverityFail, report.Severity)

			var walFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingIDWALRetention {
					walFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, walFinding)
			assert.Equal(t, check.SeverityFail, walFinding.Severity)
			assert.Contains(t, walFinding.Details, "WAL retention issues")
		})
	}
}

func TestCheck_WALRetention_AllHealthy(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			healthyPhysical("standby1"),
			healthyLogical("debezium1"),
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var walFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDWALRetention {
			walFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, walFinding)
	assert.Equal(t, check.SeverityOK, walFinding.Severity)
	assert.Contains(t, walFinding.Details, "All 2 replication slot(s) have healthy WAL retention")
}

func TestCheck_MultipleIssues(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingPhysical("standby1", 2.0),         // physical lag fail
			laggingLogical("debezium1", 40.0),        // logical lag fail (threshold: 35s)
			nonStreamingState("standby2", "catchup"), // state warn
			walIssue("debezium2", "unreserved"),      // wal fail
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Overall severity should be FAIL
	assert.Equal(t, check.SeverityFail, report.Severity)

	// Should have 4 findings (replication-state, wal-retention, physical-lag, logical-lag)
	assert.Len(t, report.Results, 4)

	// Count findings by severity
	severityCounts := map[check.Severity]int{}
	for _, finding := range report.Results {
		severityCounts[finding.Severity]++
	}

	assert.Equal(t, 3, severityCounts[check.SeverityFail])
	assert.Equal(t, 1, severityCounts[check.SeverityWarn])
}

func TestCheck_NoSlotName(t *testing.T) {
	t.Parallel()

	// Test handling of replication streams without slots
	row := db.ReplicationLagRow{
		ApplicationName:  pgText("standby1"),
		State:            pgText("streaming"),
		ReplicationType:  pgText("physical"),
		ReplayLagBytes:   pgInt8(1024),
		ReplayLagSeconds: pgFloat8(2.0),             // Lagging to trigger table output
		SlotName:         pgtype.Text{Valid: false}, // No slot
		WalStatus:        pgtype.Text{Valid: false},
	}

	queryer := &mockQueryer{rows: []db.ReplicationLagRow{row}}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var physicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDPhysicalLag {
			physicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, physicalFinding)
	assert.NotNil(t, physicalFinding.Table)
	assert.Len(t, physicalFinding.Table.Rows, 1)

	// Should show "[no slot]" in the table
	slotCell := physicalFinding.Table.Rows[0].Cells[4]
	assert.Equal(t, "[no slot]", slotCell)
}

func TestCheck_FormatBytes(t *testing.T) {
	t.Parallel()

	row := db.ReplicationLagRow{
		ApplicationName:  pgText("debezium"),
		State:            pgText("streaming"),
		ReplicationType:  pgText("logical"),
		ReplayLagBytes:   pgInt8(1536 * 1024 * 1024), // 1.5GiB
		ReplayLagSeconds: pgFloat8(40.0),             // Lagging (threshold: 35s)
		SlotName:         pgText("debezium_slot"),
		WalStatus:        pgText("reserved"),
	}

	queryer := &mockQueryer{rows: []db.ReplicationLagRow{row}}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var logicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLogicalLag {
			logicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, logicalFinding)
	assert.NotNil(t, logicalFinding.Table)

	// Check lag bytes formatting (4th column, index 3)
	lagBytesCell := logicalFinding.Table.Rows[0].Cells[3]
	assert.Contains(t, lagBytesCell, "1.5GiB")
}

func TestCheck_FormatSeconds(t *testing.T) {
	t.Parallel()

	row := db.ReplicationLagRow{
		ApplicationName:  pgText("debezium"),
		State:            pgText("streaming"),
		ReplicationType:  pgText("logical"),
		ReplayLagBytes:   pgInt8(1024),
		ReplayLagSeconds: pgFloat8(23.456), // Lagging (warn threshold: 20s)
		SlotName:         pgText("debezium_slot"),
		WalStatus:        pgText("reserved"),
	}

	queryer := &mockQueryer{rows: []db.ReplicationLagRow{row}}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var logicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLogicalLag {
			logicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, logicalFinding)
	assert.NotNil(t, logicalFinding.Table)

	// Check lag seconds formatting (3rd column, index 2) - should be formatted to 2 decimals
	lagSecondsCell := logicalFinding.Table.Rows[0].Cells[2]
	assert.Equal(t, "23.46s", lagSecondsCell)
}

func TestCheck_QueryError(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{err: fmt.Errorf("connection refused")}
	checker := replicationlag.New(queryer)

	_, err := checker.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replication-lag")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestCheck_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{}
	checker := replicationlag.New(queryer)
	metadata := checker.Metadata()

	assert.Equal(t, "replication-lag", metadata.CheckID)
	assert.Equal(t, "Replication Lag", metadata.Name)
	assert.Equal(t, check.CategoryPerformance, metadata.Category)
	assert.NotEmpty(t, metadata.Description)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
	assert.Contains(t, metadata.Readme, "Debezium")
	assert.Contains(t, metadata.SQL, "pg_stat_replication")
}

func TestCheck_PrescriptionsPresent(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingPhysical("standby1", 2.0),
			laggingLogical("debezium1", 40.0), // fail (threshold: 35s)
			nonStreamingState("standby2", "catchup"),
			walIssue("debezium2", "lost"),
		},
	}
	checker := replicationlag.New(queryer)

	_, err := checker.Check(context.Background())
	require.NoError(t, err)
}

func TestCheck_TableStructure(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.ReplicationLagRow{
			laggingPhysical("standby1", 2.0),
		},
	}
	checker := replicationlag.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var physicalFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDPhysicalLag {
			physicalFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, physicalFinding)
	require.NotNil(t, physicalFinding.Table)

	// Check table structure
	assert.Equal(t, []string{"Application", "State", "Replay Lag", "Lag Bytes", "Slot"}, physicalFinding.Table.Headers)
	assert.Len(t, physicalFinding.Table.Rows, 1)
	assert.Len(t, physicalFinding.Table.Rows[0].Cells, 5)
	assert.Equal(t, check.SeverityFail, physicalFinding.Table.Rows[0].Severity)
}

func TestCheck_SeverityMaxCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rows        []db.ReplicationLagRow
		expectedMax check.Severity
	}{
		{
			name:        "all OK",
			rows:        []db.ReplicationLagRow{healthyPhysical("standby1")},
			expectedMax: check.SeverityOK,
		},
		{
			name: "warn trumps OK",
			rows: []db.ReplicationLagRow{
				healthyPhysical("standby1"),
				laggingPhysical("standby2", 0.5), // warn
			},
			expectedMax: check.SeverityWarn,
		},
		{
			name: "fail trumps warn",
			rows: []db.ReplicationLagRow{
				laggingPhysical("standby1", 0.5), // warn
				laggingPhysical("standby2", 2.0), // fail
			},
			expectedMax: check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: tt.rows}
			checker := replicationlag.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Equal(t, tt.expectedMax, report.Severity)
		})
	}
}
