package replicationslots_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/replicationslots"
	"github.com/emancu/pgdoctor/db"
)

// mockQueryer implements ReplicationSlotsQueries for testing.
type mockQueryer struct {
	pg17Slots  []db.ReplicationSlotsRow
	pg15Slots  []db.ReplicationSlotsPG15Row
	pg17Called bool
	pg15Called bool
	err        error
}

func (m *mockQueryer) ReplicationSlots(context.Context) ([]db.ReplicationSlotsRow, error) {
	m.pg17Called = true
	if m.err != nil {
		return nil, m.err
	}
	return m.pg17Slots, nil
}

func (m *mockQueryer) ReplicationSlotsPG15(context.Context) ([]db.ReplicationSlotsPG15Row, error) {
	m.pg15Called = true
	if m.err != nil {
		return nil, m.err
	}
	return m.pg15Slots, nil
}

func pgText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

func pgBool(b bool) pgtype.Bool {
	return pgtype.Bool{Bool: b, Valid: true}
}

func pgInt8(i int64) pgtype.Int8 {
	return pgtype.Int8{Int64: i, Valid: true}
}

func pgTextNull() pgtype.Text {
	return pgtype.Text{Valid: false}
}

func pgBoolNull() pgtype.Bool {
	return pgtype.Bool{Valid: false}
}

func pgInt8Null() pgtype.Int8 {
	return pgtype.Int8{Valid: false}
}

func healthySlot(name string) db.ReplicationSlotsRow {
	return db.ReplicationSlotsRow{
		SlotName:           pgText(name),
		SlotType:           pgText("logical"),
		Active:             pgBool(true),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8Null(),
		RestartLsnLagBytes: pgInt8(1024),
	}
}

func inactiveSlot(name string, inactiveSecs int64, lagBytes int64) db.ReplicationSlotsRow {
	return db.ReplicationSlotsRow{
		SlotName:           pgText(name),
		SlotType:           pgText("logical"),
		Active:             pgBool(false),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8(inactiveSecs),
		RestartLsnLagBytes: pgInt8(lagBytes),
	}
}

func invalidSlot(name string, reason string) db.ReplicationSlotsRow {
	return db.ReplicationSlotsRow{
		SlotName:           pgText(name),
		SlotType:           pgText("logical"),
		Active:             pgBool(false),
		WalStatus:          pgText("lost"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgText(reason),
		InactiveSeconds:    pgInt8Null(),
		RestartLsnLagBytes: pgInt8Null(),
	}
}

func conflictingSlot(name string) db.ReplicationSlotsRow {
	return db.ReplicationSlotsRow{
		SlotName:           pgText(name),
		SlotType:           pgText("logical"),
		Active:             pgBool(true),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBool(true),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8Null(),
		RestartLsnLagBytes: pgInt8(1024),
	}
}

func lostWALSlot(name string, walStatus string) db.ReplicationSlotsRow {
	return db.ReplicationSlotsRow{
		SlotName:           pgText(name),
		SlotType:           pgText("logical"),
		Active:             pgBool(false),
		WalStatus:          pgText(walStatus),
		Conflicting:        pgBool(false),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8(3600),
		RestartLsnLagBytes: pgInt8Null(),
	}
}

func TestCheck_NoSlots(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{pg15Slots: []db.ReplicationSlotsPG15Row{}}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Equal(t, "All 0 replication slot(s) are healthy", report.Results[0].Details)
}

func TestCheck_AllHealthy(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{
			db.ReplicationSlotsPG15Row(healthySlot("slot1")),
			db.ReplicationSlotsPG15Row(healthySlot("slot2")),
		},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Contains(t, report.Results[0].Details, "2 replication slot(s) are healthy")
}

func TestCheck_InvalidSlots(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{
			db.ReplicationSlotsPG15Row(invalidSlot("broken_slot", "wal_removed")),
		},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Equal(t, "invalid-slots", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "broken_slot")
	assert.Contains(t, report.Results[0].Details, "wal_removed")
}

func TestCheck_ConflictingSlots(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{
			db.ReplicationSlotsPG15Row(conflictingSlot("conflict_slot")),
		},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityWarn, report.Severity)
	assert.Equal(t, "conflicting-slots", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "conflict_slot")
}

func TestCheck_LostWALSlots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		walStatus string
	}{
		{"lost WAL", "lost"},
		{"unreserved WAL", "unreserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				pg15Slots: []db.ReplicationSlotsPG15Row{
					db.ReplicationSlotsPG15Row(lostWALSlot("wal_slot", tt.walStatus)),
				},
			}
			checker := replicationslots.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			require.Len(t, report.Results, 1)
			assert.Equal(t, check.SeverityFail, report.Severity)
			assert.Equal(t, "lost-wal-slots", report.Results[0].ID)
			assert.Contains(t, report.Results[0].Details, tt.walStatus)
		})
	}
}

func TestCheck_InactiveSlots(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{
			db.ReplicationSlotsPG15Row(inactiveSlot("idle_slot", 3600, 1024*1024)),
		},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityWarn, report.Severity)
	assert.Equal(t, "inactive-slots", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "idle_slot")
	assert.Contains(t, report.Results[0].Details, "1h")     // formatDuration
	assert.Contains(t, report.Results[0].Details, "1.0MiB") // formatBytes
}

func TestCheck_CategorizationPriority(t *testing.T) {
	t.Parallel()

	// A slot with InvalidationReason should be categorized as invalid,
	// not as inactive (even though Active=false)
	slot := db.ReplicationSlotsRow{
		SlotName:           pgText("priority_slot"),
		SlotType:           pgText("logical"),
		Active:             pgBool(false), // Would qualify as inactive
		WalStatus:          pgText("lost"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgText("wal_removed"), // But this takes priority
		InactiveSeconds:    pgInt8(3600),
		RestartLsnLagBytes: pgInt8(1024),
	}

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(slot)},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should only have one finding (invalid), not two (invalid + inactive)
	require.Len(t, report.Results, 1)
	assert.Equal(t, "invalid-slots", report.Results[0].ID)
}

func TestCheck_MultipleFindingTypes(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{
			db.ReplicationSlotsPG15Row(invalidSlot("invalid1", "wal_removed")),
			db.ReplicationSlotsPG15Row(lostWALSlot("lost1", "lost")),
			db.ReplicationSlotsPG15Row(conflictingSlot("conflict1")),
			db.ReplicationSlotsPG15Row(inactiveSlot("inactive1", 60, 1024)),
			db.ReplicationSlotsPG15Row(healthySlot("healthy1")),
		},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should have 4 findings (invalid, lost-wal, conflicting, inactive)
	require.Len(t, report.Results, 4)

	// Overall severity should be FAIL (highest)
	assert.Equal(t, check.SeverityFail, report.Severity)

	// Verify all finding IDs are present
	ids := map[string]bool{}
	for _, r := range report.Results {
		ids[r.ID] = true
	}
	assert.True(t, ids["invalid-slots"])
	assert.True(t, ids["lost-wal-slots"])
	assert.True(t, ids["conflicting-slots"])
	assert.True(t, ids["inactive-slots"])
}

func TestCheck_QuerySelection_PG17(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg17Slots: []db.ReplicationSlotsRow{healthySlot("slot1")},
		pg15Slots: []db.ReplicationSlotsPG15Row{},
	}
	checker := replicationslots.New(queryer)

	// Create context with PG17 metadata
	meta := &check.InstanceMetadata{EngineVersion: "17.0", EngineVersionMajor: 17, EngineVersionMinor: 0}
	ctx := check.ContextWithInstanceMetadata(context.Background(), meta)

	_, err := checker.Check(ctx)
	require.NoError(t, err)

	assert.True(t, queryer.pg17Called, "Should use PG17 query for version 17")
	assert.False(t, queryer.pg15Called, "Should not use PG15 query for version 17")
}

func TestCheck_QuerySelection_PG15(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg17Slots: []db.ReplicationSlotsRow{},
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(healthySlot("slot1"))},
	}
	checker := replicationslots.New(queryer)

	// Create context with PG15 metadata
	meta := &check.InstanceMetadata{EngineVersion: "15.4", EngineVersionMajor: 15, EngineVersionMinor: 4}
	ctx := check.ContextWithInstanceMetadata(context.Background(), meta)

	_, err := checker.Check(ctx)
	require.NoError(t, err)

	assert.False(t, queryer.pg17Called, "Should not use PG17 query for version 15")
	assert.True(t, queryer.pg15Called, "Should use PG15 query for version 15")
}

func TestCheck_QuerySelection_PG16(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg17Slots: []db.ReplicationSlotsRow{},
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(healthySlot("slot1"))},
	}
	checker := replicationslots.New(queryer)

	// Create context with PG16 metadata
	meta := &check.InstanceMetadata{EngineVersion: "16.2", EngineVersionMajor: 16, EngineVersionMinor: 2}
	ctx := check.ContextWithInstanceMetadata(context.Background(), meta)

	_, err := checker.Check(ctx)
	require.NoError(t, err)

	assert.False(t, queryer.pg17Called, "Should not use PG17 query for version 16")
	assert.True(t, queryer.pg15Called, "Should use PG15 query for version 16")
}

func TestCheck_QuerySelection_NoMetadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		pg17Slots: []db.ReplicationSlotsRow{},
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(healthySlot("slot1"))},
	}
	checker := replicationslots.New(queryer)

	// No instance metadata in context - should fall back to PG15 query
	_, err := checker.Check(context.Background())
	require.NoError(t, err)

	assert.False(t, queryer.pg17Called, "Should not use PG17 query when metadata is missing")
	assert.True(t, queryer.pg15Called, "Should fall back to PG15 query when metadata is missing")
}

func TestCheck_QueryError(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{err: fmt.Errorf("connection refused")}
	checker := replicationslots.New(queryer)

	_, err := checker.Check(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replication-slots")
}

func TestCheck_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{}
	checker := replicationslots.New(queryer)
	metadata := checker.Metadata()

	assert.Equal(t, "replication-slots", metadata.CheckID)
	assert.Equal(t, "Replication Slots", metadata.Name)
	assert.Equal(t, check.CategoryConfigs, metadata.Category)
	assert.NotEmpty(t, metadata.Description)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
}

func TestCheck_PG15NullFields(t *testing.T) {
	t.Parallel()

	// PG15 query returns NULLs for conflicting, invalidation_reason, inactive_seconds
	// These should be handled gracefully (not cause panics or false positives)
	slot := db.ReplicationSlotsPG15Row{
		SlotName:           pgText("pg15_slot"),
		SlotType:           pgText("logical"),
		Active:             pgBool(false),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBoolNull(), // NULL in PG15
		InvalidationReason: pgTextNull(), // NULL in PG15
		InactiveSeconds:    pgInt8Null(), // NULL in PG15
		RestartLsnLagBytes: pgInt8(1024),
	}

	queryer := &mockQueryer{pg15Slots: []db.ReplicationSlotsPG15Row{slot}}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should categorize as inactive (Active=false), not invalid or conflicting
	require.Len(t, report.Results, 1)
	assert.Equal(t, "inactive-slots", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "unknown") // inactive_seconds is NULL
}

func TestCheck_CriticalLag(t *testing.T) {
	t.Parallel()

	// Active slot with >= 10GB lag
	slot := db.ReplicationSlotsRow{
		SlotName:           pgText("lagging_slot"),
		SlotType:           pgText("logical"),
		Active:             pgBool(true),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8Null(),
		RestartLsnLagBytes: pgInt8(11 * 1024 * 1024 * 1024), // 11GB
	}

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(slot)},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Equal(t, "critical-lag", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "11.0GiB")
}

func TestCheck_HighLag(t *testing.T) {
	t.Parallel()

	// Active slot with >= 1GB but < 10GB lag
	slot := db.ReplicationSlotsRow{
		SlotName:           pgText("lagging_slot"),
		SlotType:           pgText("logical"),
		Active:             pgBool(true),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8Null(),
		RestartLsnLagBytes: pgInt8(2 * 1024 * 1024 * 1024), // 2GB
	}

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(slot)},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityWarn, report.Severity)
	assert.Equal(t, "high-lag", report.Results[0].ID)
	assert.Contains(t, report.Results[0].Details, "2.0GiB")
}

func TestCheck_LagBelowThreshold(t *testing.T) {
	t.Parallel()

	// Active slot with < 1GB lag - should be healthy
	slot := db.ReplicationSlotsRow{
		SlotName:           pgText("healthy_slot"),
		SlotType:           pgText("logical"),
		Active:             pgBool(true),
		WalStatus:          pgText("reserved"),
		Conflicting:        pgBool(false),
		InvalidationReason: pgTextNull(),
		InactiveSeconds:    pgInt8Null(),
		RestartLsnLagBytes: pgInt8(500 * 1024 * 1024), // 500MB
	}

	queryer := &mockQueryer{
		pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(slot)},
	}
	checker := replicationslots.New(queryer)

	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Contains(t, report.Results[0].Details, "healthy")
}

func TestCheck_LagThresholdBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		lagBytes   int64
		expectedID string
		severity   check.Severity
	}{
		{
			name:       "just under 1GB",
			lagBytes:   1*1024*1024*1024 - 1,
			expectedID: "replication-slots", // OK
			severity:   check.SeverityOK,
		},
		{
			name:       "exactly 1GB",
			lagBytes:   1 * 1024 * 1024 * 1024,
			expectedID: "high-lag",
			severity:   check.SeverityWarn,
		},
		{
			name:       "just under 5GB",
			lagBytes:   5*1024*1024*1024 - 1,
			expectedID: "high-lag",
			severity:   check.SeverityWarn,
		},
		{
			name:       "exactly 5GB",
			lagBytes:   5 * 1024 * 1024 * 1024,
			expectedID: "critical-lag",
			severity:   check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			slot := db.ReplicationSlotsRow{
				SlotName:           pgText("slot"),
				SlotType:           pgText("logical"),
				Active:             pgBool(true),
				WalStatus:          pgText("reserved"),
				Conflicting:        pgBool(false),
				InvalidationReason: pgTextNull(),
				InactiveSeconds:    pgInt8Null(),
				RestartLsnLagBytes: pgInt8(tt.lagBytes),
			}

			queryer := &mockQueryer{
				pg15Slots: []db.ReplicationSlotsPG15Row{db.ReplicationSlotsPG15Row(slot)},
			}
			checker := replicationslots.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			require.Len(t, report.Results, 1)
			assert.Equal(t, tt.severity, report.Severity)
			assert.Equal(t, tt.expectedID, report.Results[0].ID)
		})
	}
}

func TestCheck_FormatDuration(t *testing.T) {
	t.Parallel()

	// Test via inactive slot details
	tests := []struct {
		seconds  int64
		expected string
	}{
		{30, "30s"},
		{90, "1m"},
		{3600, "1h"},
		{86400, "1d"},
		{172800, "2d"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_seconds", tt.seconds), func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				pg15Slots: []db.ReplicationSlotsPG15Row{
					db.ReplicationSlotsPG15Row(inactiveSlot("slot", tt.seconds, 0)),
				},
			}
			checker := replicationslots.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Contains(t, report.Results[0].Details, tt.expected)
		})
	}
}

func TestCheck_FormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes    int64
		expected string
	}{
		{500, "500B"},
		{1024, "1.0KiB"},
		{1024 * 1024, "1.0MiB"},
		{1024 * 1024 * 1024, "1.0GiB"},
		{1536 * 1024, "1.5MiB"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d_bytes", tt.bytes), func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				pg15Slots: []db.ReplicationSlotsPG15Row{
					db.ReplicationSlotsPG15Row(inactiveSlot("slot", 60, tt.bytes)),
				},
			}
			checker := replicationslots.New(queryer)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			assert.Contains(t, report.Results[0].Details, tt.expected)
		})
	}
}
