package partitioning_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/partitioning"
	"github.com/emancu/pgdoctor/db"
)

// Mock queryer for testing.
type mockQueryer struct {
	tables []db.LargeTablesRow
	err    error
}

func (m *mockQueryer) LargeTables(context.Context) ([]db.LargeTablesRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.tables, nil
}

func newMockQueryer(tables []db.LargeTablesRow) *mockQueryer {
	return &mockQueryer{tables: tables}
}

func newMockQueryerWithError(err error) *mockQueryer {
	return &mockQueryer{err: err}
}

// Finding IDs.
const (
	findingIDLargeUnpartitioned     = "large-unpartitioned"
	findingIDTransientUnpartitioned = "transient-unpartitioned"
	findingIDInefficientPartitions  = "inefficient-partitions"
)

// Helper to create a LargeTablesRow with common defaults.
// table_name is always in "schema.table" format.
func makeTable(schema, name string, rows int64, partitioned, transient bool) db.LargeTablesRow {
	tableName := schema + "." + name
	return db.LargeTablesRow{
		TableName:      pgtype.Text{String: tableName, Valid: true},
		TableSizeBytes: pgtype.Int8{Int64: rows * 100, Valid: true}, // Rough size estimate
		EstimatedRows:  pgtype.Int8{Int64: rows, Valid: true},
		IsPartitioned:  pgtype.Bool{Bool: partitioned, Valid: true},
		IsPartition:    pgtype.Bool{Bool: false, Valid: true},
		ParentTable:    pgtype.Text{Valid: false},
		IsTransient:    pgtype.Bool{Bool: transient, Valid: true},
	}
}

// Helper to create a partition (child of a partitioned table).
// table_name is always in "schema.table" format.
func makePartition(schema, name, parentTable string, rows int64) db.LargeTablesRow {
	tableName := schema + "." + name
	return db.LargeTablesRow{
		TableName:      pgtype.Text{String: tableName, Valid: true},
		TableSizeBytes: pgtype.Int8{Int64: rows * 100, Valid: true},
		EstimatedRows:  pgtype.Int8{Int64: rows, Valid: true},
		IsPartitioned:  pgtype.Bool{Bool: false, Valid: true},
		IsPartition:    pgtype.Bool{Bool: true, Valid: true},
		ParentTable:    pgtype.Text{String: parentTable, Valid: true},
		IsTransient:    pgtype.Bool{Bool: false, Valid: true},
	}
}

// Helper to create a table with activity data.
func makeTableWithActivity(schema, name string, rows, ins, upd, del int64, partitioned, transient bool) db.LargeTablesRow {
	tableName := schema + "." + name
	return db.LargeTablesRow{
		TableName:      pgtype.Text{String: tableName, Valid: true},
		TableSizeBytes: pgtype.Int8{Int64: rows * 100, Valid: true},
		EstimatedRows:  pgtype.Int8{Int64: rows, Valid: true},
		IsPartitioned:  pgtype.Bool{Bool: partitioned, Valid: true},
		IsPartition:    pgtype.Bool{Bool: false, Valid: true},
		ParentTable:    pgtype.Text{Valid: false},
		IsTransient:    pgtype.Bool{Bool: transient, Valid: true},
		NTupIns:        pgtype.Int8{Int64: ins, Valid: true},
		NTupUpd:        pgtype.Int8{Int64: upd, Valid: true},
		NTupDel:        pgtype.Int8{Int64: del, Valid: true},
	}
}

func Test_Partitioning_NoLargeTables(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.LargeTablesRow{})

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should have 2 findings (large-unpartitioned and transient-unpartitioned)
	require.Equal(t, 2, len(report.Results))

	// Both should be OK
	for _, result := range report.Results {
		require.Equal(t, check.SeverityOK, result.Severity)
	}
	require.Equal(t, check.SeverityOK, report.Severity)
}

func Test_Partitioning_AllPartitioned(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, true, false),
		makeTable("public", "events", 20_000_000, true, true),
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 2, len(report.Results))
	for _, result := range report.Results {
		require.Equal(t, check.SeverityOK, result.Severity)
	}
}

func Test_Partitioning_LargeUnpartitioned_Warning(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 30_000_000, false, false), // 30M - warn
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Find the large-unpartitioned finding
	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeUnpartitioned {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	require.Equal(t, check.SeverityWarn, largeFinding.Severity)
	require.Contains(t, largeFinding.Details, "1 large table")
}

func Test_Partitioning_LargeUnpartitioned_Fail(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, false, false), // 60M - fail
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeUnpartitioned {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	require.Equal(t, check.SeverityFail, largeFinding.Severity)
	require.Contains(t, largeFinding.Details, "1 large table")
	require.NotNil(t, largeFinding.Table)
	require.Contains(t, largeFinding.Table.Rows[0].Cells[4], "MUST partition")
}

func Test_Partitioning_TransientUnpartitioned(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "outbox_events", 15_000_000, false, true), // transient, unpartitioned
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var transientFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTransientUnpartitioned {
			transientFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, transientFinding)
	require.Equal(t, check.SeverityFail, transientFinding.Severity)
	require.Contains(t, transientFinding.Details, "1 large transient table")
}

func Test_Partitioning_MixedResults(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, false, false),       // Large, fail
		makeTable("public", "products", 30_000_000, false, false),     // Large, warn
		makeTable("public", "users", 20_000_000, true, false),         // Large, partitioned - OK
		makeTable("public", "outbox_events", 12_000_000, false, true), // Transient, fail
		makeTable("public", "inbox_events", 11_000_000, true, true),   // Transient, partitioned - OK
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 2, len(report.Results))
	require.Equal(t, check.SeverityFail, report.Severity)

	// Check large-unpartitioned finding
	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeUnpartitioned {
			largeFinding = &report.Results[i]
			break
		}
	}
	require.NotNil(t, largeFinding)
	require.Equal(t, check.SeverityFail, largeFinding.Severity)
	require.Contains(t, largeFinding.Details, "2 large table")

	// Check transient-unpartitioned finding
	var transientFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTransientUnpartitioned {
			transientFinding = &report.Results[i]
			break
		}
	}
	require.NotNil(t, transientFinding)
	require.Equal(t, check.SeverityFail, transientFinding.Severity)
}

func Test_Partitioning_InefficientPartitions(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makePartition("public", "orders_2024", "public.orders", 15_000_000), // Large partition
		makePartition("public", "orders_2023", "public.orders", 20_000_000), // Another large partition
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should have inefficient-partitions finding plus OK findings for large/transient
	var inefficientFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDInefficientPartitions {
			inefficientFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, inefficientFinding)
	require.Equal(t, check.SeverityWarn, inefficientFinding.Severity)
	require.Contains(t, inefficientFinding.Details, "2 partition(s)")
	require.NotNil(t, inefficientFinding.Table)
	require.Equal(t, 2, len(inefficientFinding.Table.Rows))
	require.Equal(t, "public.orders", inefficientFinding.Table.Rows[0].Cells[1]) // Parent table
}

func Test_Partitioning_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := partitioning.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "partitioning")
}

func Test_Partitioning_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.LargeTablesRow{})
	checker := partitioning.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "partitioning", metadata.CheckID)
	require.Equal(t, "Table Partitioning", metadata.Name)
	require.Equal(t, check.CategorySchema, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}

func Test_Partitioning_Thresholds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		rows             int64
		expectedSeverity check.Severity
		expectedStatus   string
	}{
		{
			name:             "below 25M - no finding",
			rows:             24_999_999,
			expectedSeverity: check.SeverityOK,
			expectedStatus:   "",
		},
		{
			name:             "exactly 25M - warning",
			rows:             25_000_000,
			expectedSeverity: check.SeverityWarn,
			expectedStatus:   "Approaching threshold",
		},
		{
			name:             "between 25M and 50M - warning",
			rows:             30_000_000,
			expectedSeverity: check.SeverityWarn,
			expectedStatus:   "Approaching threshold",
		},
		{
			name:             "exactly 50M - fail",
			rows:             50_000_000,
			expectedSeverity: check.SeverityFail,
			expectedStatus:   "MUST partition",
		},
		{
			name:             "above 50M - fail",
			rows:             100_000_000,
			expectedSeverity: check.SeverityFail,
			expectedStatus:   "MUST partition",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tables := []db.LargeTablesRow{
				makeTable("public", "test_table", tc.rows, false, false),
			}

			queryer := newMockQueryer(tables)
			checker := partitioning.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			var largeFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingIDLargeUnpartitioned {
					largeFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, largeFinding)
			require.Equal(t, tc.expectedSeverity, largeFinding.Severity)

			if tc.expectedStatus != "" {
				require.NotNil(t, largeFinding.Table)
				require.Greater(t, len(largeFinding.Table.Rows), 0)
				require.Contains(t, largeFinding.Table.Rows[0].Cells[4], tc.expectedStatus)
			}
		})
	}
}

func Test_Partitioning_PrescriptionContent(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, false, false),
	}

	queryer := newMockQueryer(tables)
	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeUnpartitioned {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
}

func Test_Partitioning_TransientPrescriptionContent(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "outbox_events", 15_000_000, false, true),
	}

	queryer := newMockQueryer(tables)
	checker := partitioning.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var transientFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTransientUnpartitioned {
			transientFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, transientFinding)
}

func Test_Partitioning_ActivityAwareThresholds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		rows             int64
		inserts          int64
		updates          int64
		deletes          int64
		expectedSeverity check.Severity
		expectedReason   string
	}{
		{
			name:             "insert-heavy 15M - warning (would be OK without activity)",
			rows:             15_000_000,
			inserts:          900_000, // 90% inserts
			updates:          50_000,
			deletes:          50_000,
			expectedSeverity: check.SeverityWarn,
			expectedReason:   "Insert-heavy",
		},
		{
			name:             "insert-heavy 25M - fail (would be warning without activity)",
			rows:             25_000_000,
			inserts:          850_000, // 85% inserts
			updates:          100_000,
			deletes:          50_000,
			expectedSeverity: check.SeverityFail,
			expectedReason:   "Insert-heavy",
		},
		{
			name:             "high-delete 12M - warning (would be OK without activity)",
			rows:             12_000_000,
			inserts:          100_000,
			updates:          50_000,
			deletes:          25_000, // 25% delete ratio
			expectedSeverity: check.SeverityWarn,
			expectedReason:   "High-delete",
		},
		{
			name:             "high-delete 30M - fail (would be warning without activity)",
			rows:             30_000_000,
			inserts:          100_000,
			updates:          50_000,
			deletes:          30_000, // 30% delete ratio
			expectedSeverity: check.SeverityFail,
			expectedReason:   "High-delete",
		},
		{
			name:             "regular table 15M - OK (no activity-aware)",
			rows:             15_000_000,
			inserts:          50_000, // 50% inserts - not insert-heavy
			updates:          40_000,
			deletes:          10_000, // 20% delete ratio - borderline
			expectedSeverity: check.SeverityOK,
			expectedReason:   "",
		},
		{
			name:             "zero activity 15M - OK (can't determine activity pattern)",
			rows:             15_000_000,
			inserts:          0,
			updates:          0,
			deletes:          0,
			expectedSeverity: check.SeverityOK,
			expectedReason:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tables := []db.LargeTablesRow{
				makeTableWithActivity("public", "test_table", tc.rows, tc.inserts, tc.updates, tc.deletes, false, false),
			}

			queryer := newMockQueryer(tables)
			checker := partitioning.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			var largeFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingIDLargeUnpartitioned {
					largeFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, largeFinding)
			require.Equal(t, tc.expectedSeverity, largeFinding.Severity)

			if tc.expectedReason != "" {
				require.NotNil(t, largeFinding.Table)
				require.Greater(t, len(largeFinding.Table.Rows), 0)
				require.Equal(t, tc.expectedReason, largeFinding.Table.Rows[0].Cells[3])
			}
		})
	}
}
