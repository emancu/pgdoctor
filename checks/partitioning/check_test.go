package partitioning_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/partitioning"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

// Mock queryer for testing.
type mockQueryer struct {
	tables []db.LargeTablesRow
	err    error
	params db.LargeTablesParams
}

func (m *mockQueryer) LargeTables(_ context.Context, arg db.LargeTablesParams) ([]db.LargeTablesRow, error) {
	m.params = arg
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

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should have 2 findings (large-unpartitioned and transient-unpartitioned)
	require.Equal(t, 2, len(report.Results))

	// Both should be OK
	for _, result := range report.Results {
		require.Equal(t, check.SeverityPass, result.Severity)
	}
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_Partitioning_AllPartitioned(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, true, false),
		makeTable("public", "events", 20_000_000, true, true),
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 2, len(report.Results))
	for _, result := range report.Results {
		require.Equal(t, check.SeverityPass, result.Severity)
	}
}

func Test_Partitioning_LargeUnpartitioned_Warning(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, false, false),
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
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

func Test_Partitioning_TransientUnpartitioned(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "outbox_events", 15_000_000, false, true), // transient, unpartitioned
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
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
	require.Equal(t, check.SeverityWarn, transientFinding.Severity)
	require.Contains(t, transientFinding.Details, "1 large transient table")
	require.Equal(t, check.SeverityWarn, transientFinding.Table.Rows[0].Severity)
}

func Test_Partitioning_MixedResults(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, false, false),       // Large, warn
		makeTable("public", "products", 30_000_000, false, false),     // Below large threshold - OK
		makeTable("public", "users", 20_000_000, true, false),         // Large, partitioned - OK
		makeTable("public", "outbox_events", 12_000_000, false, true), // Transient, warn
		makeTable("public", "inbox_events", 11_000_000, true, true),   // Transient, partitioned - OK
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 2, len(report.Results))
	require.Equal(t, check.SeverityWarn, report.Severity)

	// Check large-unpartitioned finding
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

	// Check transient-unpartitioned finding
	var transientFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTransientUnpartitioned {
			transientFinding = &report.Results[i]
			break
		}
	}
	require.NotNil(t, transientFinding)
	require.Equal(t, check.SeverityWarn, transientFinding.Severity)
}

func Test_Partitioning_InefficientPartitions(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makePartition("public", "orders_2024", "public.orders", 15_000_000), // Large partition
		makePartition("public", "orders_2023", "public.orders", 20_000_000), // Another large partition
	}

	queryer := newMockQueryer(tables)

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
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

func Test_Partitioning_MinPartitionRowsConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         partitioning.Config
		wantMinRows int64
		wantDetails string
	}{
		{
			name:        "default",
			cfg:         partitioning.DefaultConfig(),
			wantMinRows: 10_000_000,
			wantDetails: ">= 10.0M rows",
		},
		{
			name:        "configured",
			cfg:         partitioning.Config{InefficientPartitionsMinRows: 25_000_000, LargeUnpartitionedMinRows: 50_000_000, TransientUnpartitionedMinRows: 10_000_000},
			wantMinRows: 25_000_000,
			wantDetails: ">= 25.0M rows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer([]db.LargeTablesRow{
				makePartition("public", "orders_2024", "public.orders", 30_000_000),
			})

			report, err := partitioning.New(queryer, tt.cfg).Check(context.Background())
			require.NoError(t, err)

			require.Equal(t, tt.wantMinRows, queryer.params.MinPartitionRows)
			var details string
			for _, finding := range report.Results {
				if finding.ID == findingIDInefficientPartitions {
					details = finding.Details
				}
			}
			require.Contains(t, details, tt.wantDetails)
		})
	}
}

func Test_Partitioning_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := partitioning.New(queryer, partitioning.DefaultConfig())
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "partitioning")
}

func Test_Partitioning_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.LargeTablesRow{})
	checker := partitioning.New(queryer, partitioning.DefaultConfig())
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
		transient        bool
		expectedID       string
		expectedSeverity check.Severity
	}{
		{name: "below 50M - pass", rows: 49_999_999, expectedID: findingIDLargeUnpartitioned, expectedSeverity: check.SeverityPass},
		{name: "exactly 50M - warn", rows: 50_000_000, expectedID: findingIDLargeUnpartitioned, expectedSeverity: check.SeverityWarn},
		{name: "above 50M - warn", rows: 100_000_000, expectedID: findingIDLargeUnpartitioned, expectedSeverity: check.SeverityWarn},
		{name: "transient below 10M - pass", rows: 9_999_999, transient: true, expectedID: findingIDTransientUnpartitioned, expectedSeverity: check.SeverityPass},
		{name: "transient exactly 10M - warn", rows: 10_000_000, transient: true, expectedID: findingIDTransientUnpartitioned, expectedSeverity: check.SeverityWarn},
		{name: "transient above 50M - warn", rows: 100_000_000, transient: true, expectedID: findingIDTransientUnpartitioned, expectedSeverity: check.SeverityWarn},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer([]db.LargeTablesRow{
				makeTable("public", "test_table", tc.rows, false, tc.transient),
			})
			report, err := partitioning.New(queryer, partitioning.DefaultConfig()).Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			var finding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == tc.expectedID {
					finding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, finding)
			require.Equal(t, tc.expectedSeverity, finding.Severity)
			require.Equal(t, tc.expectedSeverity, report.Severity)
		})
	}
}

func Test_Partitioning_PrescriptionContent(t *testing.T) {
	t.Parallel()

	tables := []db.LargeTablesRow{
		makeTable("public", "orders", 60_000_000, false, false),
	}

	queryer := newMockQueryer(tables)
	checker := partitioning.New(queryer, partitioning.DefaultConfig())
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
	checker := partitioning.New(queryer, partitioning.DefaultConfig())
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

func Test_Partitioning_ActivityContext(t *testing.T) {
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
			name:             "insert-heavy below 50M - pass",
			rows:             25_000_000,
			inserts:          900_000,
			updates:          50_000,
			deletes:          50_000,
			expectedSeverity: check.SeverityPass,
		},
		{
			name:             "insert-heavy 60M - warn",
			rows:             60_000_000,
			inserts:          900_000,
			updates:          50_000,
			deletes:          50_000,
			expectedSeverity: check.SeverityWarn,
			expectedReason:   "Insert-heavy",
		},
		{
			name:             "high-delete 60M - warn",
			rows:             60_000_000,
			inserts:          100_000,
			updates:          50_000,
			deletes:          30_000,
			expectedSeverity: check.SeverityWarn,
			expectedReason:   "High-delete",
		},
		{
			name:             "regular 60M - warn",
			rows:             60_000_000,
			inserts:          50_000,
			updates:          40_000,
			deletes:          10_000,
			expectedSeverity: check.SeverityWarn,
			expectedReason:   "Large table",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer([]db.LargeTablesRow{
				makeTableWithActivity("public", "test_table", tc.rows, tc.inserts, tc.updates, tc.deletes, false, false),
			})
			report, err := partitioning.New(queryer, partitioning.DefaultConfig()).Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

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
				require.Equal(t, []string{"Table", "Size", "Est. Rows", "Reason"}, largeFinding.Table.Headers)
				require.Equal(t, tc.expectedReason, largeFinding.Table.Rows[0].Cells[3])
				require.Equal(t, check.SeverityWarn, largeFinding.Table.Rows[0].Severity)
			}
		})
	}
}

func Test_Partitioning_RowThresholdConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cfg           partitioning.Config
		wantQueryRows int64
		wantLarge     check.Severity
		wantTransient check.Severity
	}{
		{
			name:          "defaults",
			cfg:           partitioning.DefaultConfig(),
			wantQueryRows: 10_000_000,
			wantLarge:     check.SeverityPass,
			wantTransient: check.SeverityPass,
		},
		{
			name:          "lower thresholds lower the query floor",
			cfg:           partitioning.Config{InefficientPartitionsMinRows: 10_000_000, LargeUnpartitionedMinRows: 5_000_000, TransientUnpartitionedMinRows: 2_000_000},
			wantQueryRows: 2_000_000,
			wantLarge:     check.SeverityWarn,
			wantTransient: check.SeverityWarn,
		},
		{
			name:          "large threshold below transient threshold sets the query floor",
			cfg:           partitioning.Config{InefficientPartitionsMinRows: 10_000_000, LargeUnpartitionedMinRows: 5_000_000, TransientUnpartitionedMinRows: 10_000_000},
			wantQueryRows: 5_000_000,
			wantLarge:     check.SeverityWarn,
			wantTransient: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer([]db.LargeTablesRow{
				makeTable("public", "orders", 6_000_000, false, false),
				makeTable("public", "outbox", 3_000_000, false, true),
			})
			report, err := partitioning.New(queryer, tt.cfg).Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			require.Equal(t, tt.wantQueryRows, queryer.params.MinTableRows)
			severities := map[string]check.Severity{}
			for _, finding := range report.Results {
				severities[finding.ID] = finding.Severity
			}
			require.Equal(t, tt.wantLarge, severities[findingIDLargeUnpartitioned])
			require.Equal(t, tt.wantTransient, severities[findingIDTransientUnpartitioned])
		})
	}
}

func Test_Partitioning_ConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     partitioning.Config
		wantErr bool
	}{
		{name: "defaults", cfg: partitioning.DefaultConfig()},
		{name: "zero inefficient", cfg: partitioning.Config{LargeUnpartitionedMinRows: 1, TransientUnpartitionedMinRows: 1}, wantErr: true},
		{name: "negative large", cfg: partitioning.Config{InefficientPartitionsMinRows: 1, LargeUnpartitionedMinRows: -1, TransientUnpartitionedMinRows: 1}, wantErr: true},
		{name: "zero transient", cfg: partitioning.Config{InefficientPartitionsMinRows: 1, LargeUnpartitionedMinRows: 1}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
