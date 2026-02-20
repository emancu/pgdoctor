package partitionusage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/partitionusage"
	"github.com/emancu/pgdoctor/db"
)

// Finding IDs.
const (
	findingIDPartitionKeyUnused   = "partition-key-unused"
	findingIDHighSeqScanRatio     = "high-seq-scan-ratio"
	findingIDJoinMissingPartKey   = "join-missing-partition-key"
	findingIDExtensionUnavailable = "extension-unavailable"
)

// Mock queryer for testing.
type mockQueryer struct {
	tables       []db.PartitionedTablesWithKeysRow
	queryStats   []db.QueryStatsFromStatStatementsRow
	hasExtension *bool // Use pointer so we can distinguish between unset and false
	tablesErr    error
	statsErr     error
	extensionErr error
}

func (m *mockQueryer) HasPgStatStatements(context.Context) (bool, error) {
	if m.extensionErr != nil {
		return false, m.extensionErr
	}
	// Default to true (extension available) unless explicitly set to false
	if m.hasExtension == nil {
		return true, nil
	}
	return *m.hasExtension, nil
}

func (m *mockQueryer) PartitionedTablesWithKeys(context.Context) ([]db.PartitionedTablesWithKeysRow, error) {
	if m.tablesErr != nil {
		return nil, m.tablesErr
	}
	return m.tables, nil
}

func (m *mockQueryer) QueryStatsFromStatStatements(context.Context) ([]db.QueryStatsFromStatStatementsRow, error) {
	if m.statsErr != nil {
		return nil, m.statsErr
	}
	return m.queryStats, nil
}

// Helper to create a PartitionedTablesWithKeysRow.
func makePartitionedTable(schema, name, partitionKey string, partitionCount int64) db.PartitionedTablesWithKeysRow {
	return db.PartitionedTablesWithKeysRow{
		SchemaName:          pgtype.Text{String: schema, Valid: true},
		TableName:           pgtype.Text{String: name, Valid: true},
		PartitionStrategy:   pgtype.Text{String: "r", Valid: true}, // range
		PartitionKeyColumns: pgtype.Text{String: partitionKey, Valid: true},
		HasExpressionKey:    pgtype.Bool{Bool: false, Valid: true},
		PartitionCount:      pgtype.Int8{Int64: partitionCount, Valid: true},
		TotalSizeBytes:      pgtype.Int8{Int64: 1000000000, Valid: true},
		EstimatedRows:       pgtype.Int8{Int64: 10000000, Valid: true},
		TotalSeqScans:       pgtype.Int8{Int64: 0, Valid: true},
		TotalIdxScans:       pgtype.Int8{Int64: 0, Valid: true},
	}
}

// Helper to create a PartitionedTablesWithKeysRow with scan stats.
func makePartitionedTableWithScans(schema, name, partitionKey string, seqScans, idxScans int64) db.PartitionedTablesWithKeysRow {
	return db.PartitionedTablesWithKeysRow{
		SchemaName:          pgtype.Text{String: schema, Valid: true},
		TableName:           pgtype.Text{String: name, Valid: true},
		PartitionStrategy:   pgtype.Text{String: "r", Valid: true},
		PartitionKeyColumns: pgtype.Text{String: partitionKey, Valid: true},
		HasExpressionKey:    pgtype.Bool{Bool: false, Valid: true},
		PartitionCount:      pgtype.Int8{Int64: 12, Valid: true},
		TotalSizeBytes:      pgtype.Int8{Int64: 1000000000, Valid: true},
		EstimatedRows:       pgtype.Int8{Int64: 10000000, Valid: true},
		TotalSeqScans:       pgtype.Int8{Int64: seqScans, Valid: true},
		TotalIdxScans:       pgtype.Int8{Int64: idxScans, Valid: true},
	}
}

// Helper to create a QueryStatsFromStatStatementsRow.
func makeQueryStats(query string, calls int64, totalExecTime float64) db.QueryStatsFromStatStatementsRow {
	return db.QueryStatsFromStatStatementsRow{
		QueryID:       pgtype.Int8{Int64: 12345, Valid: true},
		Query:         pgtype.Text{String: query, Valid: true},
		Calls:         pgtype.Int8{Int64: calls, Valid: true},
		TotalExecTime: pgtype.Float8{Float64: totalExecTime, Valid: true},
		MeanExecTime:  pgtype.Float8{Float64: totalExecTime / float64(calls), Valid: true},
		RowsReturned:  pgtype.Int8{Int64: calls * 10, Valid: true},
	}
}

func Test_PartitionUsage_NoPartitionedTables(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables:     []db.PartitionedTablesWithKeysRow{},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "No partitioned tables found")
}

func Test_PartitionUsage_NoQueryStats(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "No query statistics available")
}

func Test_PartitionUsage_AllQueriesUsePartitionKey(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders WHERE created_at > $1 AND customer_id = $2", 1000, 500000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "properly use partition keys")
}

func Test_PartitionUsage_QueryMissingPartitionKey_Warning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// Query references 'orders' but doesn't filter on created_at
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 500, 400000), // 500 calls, 400s total
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityWarn, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "1 partitioned table")
}

func Test_PartitionUsage_QueryMissingPartitionKey_Fail(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// Query with very high calls (>1000) should be FAIL
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 5000, 4000000), // 5000 calls, 4000s total
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityFail, report.Results[0].Severity)
}

func Test_PartitionUsage_QueryBelowThreshold_Ignored(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// Low call count (< 100) and low exec time (< 5min) should be ignored
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 50, 100000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
}

func Test_PartitionUsage_QueryNotReferencingTable_Ignored(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// Query doesn't reference 'orders' table
			makeQueryStats("SELECT * FROM users WHERE id = $1", 5000, 4000000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
}

func Test_PartitionUsage_ExpressionBasedKey_Skipped(t *testing.T) {
	t.Parallel()

	table := makePartitionedTable("public", "orders", "created_at", 12)
	table.HasExpressionKey = pgtype.Bool{Bool: true, Valid: true} // Expression-based key

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{table},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 5000, 4000000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should be OK because expression-based keys are skipped
	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityOK, report.Results[0].Severity)
}

func Test_PartitionUsage_MultipleTables(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
			makePartitionedTable("public", "events", "timestamp", 24),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// Query missing partition key for orders
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 500, 400000),
			// Query missing partition key for events
			makeQueryStats("SELECT * FROM events WHERE event_type = $1", 500, 400000),
			// Query properly using partition key
			makeQueryStats("SELECT * FROM orders WHERE created_at > $1", 1000, 500000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	require.Equal(t, check.SeverityWarn, report.Results[0].Severity)
	require.Contains(t, report.Results[0].Details, "2 partitioned table")
}

func Test_PartitionUsage_TablesQueryError(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tablesErr: fmt.Errorf("database connection error"),
	}

	checker := partitionusage.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "partition-usage")
}

func Test_PartitionUsage_ExtensionNotInstalled(t *testing.T) {
	t.Parallel()

	hasExtFalse := false
	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTableWithScans("public", "orders", "created_at", 2000, 100),
		},
		hasExtension: &hasExtFalse,
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, 2, len(report.Results)) // seq scan check + extension warning

	// Check that seq scan analysis still ran (runs first, doesn't need extension)
	seqScanFinding := report.Results[0]
	require.Equal(t, findingIDHighSeqScanRatio, seqScanFinding.ID)
	require.Equal(t, check.SeverityWarn, seqScanFinding.Severity)

	// Check extension warning finding (runs after seq scan check)
	extensionFinding := report.Results[1]
	require.Equal(t, findingIDExtensionUnavailable, extensionFinding.ID)
	require.Equal(t, check.SeverityWarn, extensionFinding.Severity)
	require.Contains(t, extensionFinding.Details, "cannot analyze query patterns")
}
func Test_PartitionUsage_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{}
	checker := partitionusage.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "partition-usage", metadata.CheckID)
	require.Equal(t, "Partition Key Usage", metadata.Name)
	require.Equal(t, check.CategoryPerformance, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}

func Test_PartitionUsage_TableOutput(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 500, 400000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, len(report.Results))
	result := report.Results[0]

	require.NotNil(t, result.Table)
	require.Equal(t, []string{"Table", "Partition Key", "Partitions", "Problem Queries", "Total Calls", "Total Time"}, result.Table.Headers)
	require.Greater(t, len(result.Table.Rows), 0)

	row := result.Table.Rows[0]
	require.Equal(t, "public.orders", row.Cells[0])
	require.Equal(t, "created_at", row.Cells[1])
	require.Equal(t, "12", row.Cells[2])
}

func Test_PartitionUsage_PartitionKeyVariations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		query        string
		partitionKey string
		shouldBeOK   bool
		description  string
	}{
		{
			name:         "exact column match with equals",
			query:        "SELECT * FROM orders WHERE created_at = $1",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "column match with greater than",
			query:        "SELECT * FROM orders WHERE created_at > $1",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "column match with BETWEEN",
			query:        "SELECT * FROM orders WHERE created_at between $1 and $2",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "column match with IN",
			query:        "SELECT * FROM orders WHERE created_at in ($1, $2)",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "table-qualified column",
			query:        "SELECT * FROM orders WHERE orders.created_at > $1",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "missing partition key",
			query:        "SELECT * FROM orders WHERE customer_id = $1",
			partitionKey: "created_at",
			shouldBeOK:   false,
		},
		{
			name:         "no WHERE clause",
			query:        "SELECT * FROM orders",
			partitionKey: "created_at",
			shouldBeOK:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				tables: []db.PartitionedTablesWithKeysRow{
					makePartitionedTable("public", "orders", tc.partitionKey, 12),
				},
				queryStats: []db.QueryStatsFromStatStatementsRow{
					makeQueryStats(tc.query, 500, 400000),
				},
			}

			checker := partitionusage.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			require.Equal(t, 1, len(report.Results))
			if tc.shouldBeOK {
				require.Equal(t, check.SeverityOK, report.Results[0].Severity, "Expected OK for: %s", tc.name)
			} else {
				require.NotEqual(t, check.SeverityOK, report.Results[0].Severity, "Expected not OK for: %s", tc.name)
			}
		})
	}
}

// Tests for sequential scan subcheck.

func Test_PartitionUsage_HighSeqScanRatio_Warning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTableWithScans("public", "orders", "created_at", 5000, 100), // 50:1 ratio
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var seqScanFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDHighSeqScanRatio {
			seqScanFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, seqScanFinding)
	require.Equal(t, check.SeverityWarn, seqScanFinding.Severity)
	require.Contains(t, seqScanFinding.Details, "1 partitioned table")
}

func Test_PartitionUsage_HighSeqScanRatio_Fail(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTableWithScans("public", "orders", "created_at", 50000, 100), // 500:1 ratio
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var seqScanFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDHighSeqScanRatio {
			seqScanFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, seqScanFinding)
	require.Equal(t, check.SeverityFail, seqScanFinding.Severity)
}

func Test_PartitionUsage_LowSeqScans_NoFinding(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTableWithScans("public", "orders", "created_at", 500, 10), // Below threshold
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should not have seq scan finding
	for _, result := range report.Results {
		require.NotEqual(t, findingIDHighSeqScanRatio, result.ID)
	}
}

func Test_PartitionUsage_HealthySeqIdxRatio_NoFinding(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTableWithScans("public", "orders", "created_at", 2000, 1000), // 2:1 ratio (healthy)
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should not have seq scan finding
	for _, result := range report.Results {
		require.NotEqual(t, findingIDHighSeqScanRatio, result.ID)
	}
}

func Test_PartitionUsage_ZeroIdxScans_Fail(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTableWithScans("public", "orders", "created_at", 5000, 0), // No idx scans
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var seqScanFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDHighSeqScanRatio {
			seqScanFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, seqScanFinding)
	require.Equal(t, check.SeverityFail, seqScanFinding.Severity)
	require.Contains(t, seqScanFinding.Table.Rows[0].Cells[3], "no idx scans")
}

// Tests for JOIN missing partition key subcheck.

func Test_PartitionUsage_JoinMissingPartitionKey_Warning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders o JOIN order_items oi ON oi.order_id = o.id", 500, 400000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var joinFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDJoinMissingPartKey {
			joinFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, joinFinding)
	require.Equal(t, check.SeverityWarn, joinFinding.Severity)
	require.Contains(t, joinFinding.Details, "1 partitioned table")
}

func Test_PartitionUsage_JoinWithPartitionKey_NoFinding(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders o JOIN order_items oi ON oi.order_id = o.id AND o.created_at > $1", 500, 400000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should not have join finding
	for _, result := range report.Results {
		require.NotEqual(t, findingIDJoinMissingPartKey, result.ID)
	}
}

func Test_PartitionUsage_NonJoinQuery_NoJoinFinding(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// No JOIN, just SELECT without partition key - should trigger partition-key-unused, not join-missing
			makeQueryStats("SELECT * FROM orders WHERE customer_id = $1", 500, 400000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should not have join finding (no JOIN in query)
	for _, result := range report.Results {
		require.NotEqual(t, findingIDJoinMissingPartKey, result.ID)
	}
}

func Test_PartitionUsage_JoinMissingPartitionKey_Fail(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders o JOIN order_items oi ON oi.order_id = o.id", 5000, 4000000),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var joinFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDJoinMissingPartKey {
			joinFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, joinFinding)
	require.Equal(t, check.SeverityFail, joinFinding.Severity)
}
