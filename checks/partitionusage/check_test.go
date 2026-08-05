package partitionusage_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/partitionusage"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

// Finding IDs.
const (
	findingIDPartitionKeyUnused   = "partition-key-unused"
	findingIDHighSeqScanRatio     = "high-seq-scan-ratio"
	findingIDJoinMissingPartKey   = "join-missing-partition-key"
	findingIDExtensionUnavailable = "extension-unavailable"
	findingIDQueryTextRestricted  = "query-text-restricted"
)

// Mock queryer for testing.
type mockQueryer struct {
	tables        []db.PartitionedTablesWithKeysRow
	queryStats    []db.QueryStatsFromStatStatementsRow
	hasExtension  *bool // Use pointer so we can distinguish between unset and false
	hiddenQueries int64
	tablesErr     error
	statsErr      error
	extensionErr  error
	hiddenErr     error
}

func (m *mockQueryer) HiddenQueryTextCount(context.Context) (pgtype.Int8, error) {
	if m.hiddenErr != nil {
		return pgtype.Int8{}, m.hiddenErr
	}
	return pgtype.Int8{Int64: m.hiddenQueries, Valid: true}, nil
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

// Helper to create a PartitionedTablesWithKeysRow with a partition strategy
// ('h' hash, 'l' list, 'r' range).
func makePartitionedTableWithStrategy(schema, name, partitionKey, strategy string) db.PartitionedTablesWithKeysRow {
	table := makePartitionedTable(schema, name, partitionKey, 12)
	table.PartitionStrategy = pgtype.Text{String: strategy, Valid: true}

	return table
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
// Lowercased, mirroring the LOWER() in query.sql.
func makeQueryStats(query string, calls int64, totalExecTime float64) db.QueryStatsFromStatStatementsRow {
	return db.QueryStatsFromStatStatementsRow{
		QueryID:       pgtype.Int8{Int64: 12345, Valid: true},
		Query:         pgtype.Text{String: strings.ToLower(query), Valid: true},
		Calls:         pgtype.Int8{Int64: calls, Valid: true},
		TotalExecTime: pgtype.Float8{Float64: totalExecTime, Valid: true},
		MeanExecTime:  pgtype.Float8{Float64: totalExecTime / float64(calls), Valid: true},
		RowsReturned:  pgtype.Int8{Int64: calls * 10, Valid: true},
	}
}

// keyFinding returns the partition-key-unused finding. Looking findings up by ID
// rather than by position keeps assertions valid as the check gains findings.
func keyFinding(t *testing.T, report *check.Report) check.Finding {
	t.Helper()

	return findingByID(t, report, findingIDPartitionKeyUnused)
}

func findingByID(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()

	for _, result := range report.Results {
		if result.ID == id {
			return result
		}
	}

	require.FailNowf(t, "finding not found", "no finding with ID %q in %+v", id, report.Results)

	return check.Finding{}
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

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
	require.Contains(t, keyFinding(t, report).Details, "No partitioned tables found")
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

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
	require.Contains(t, keyFinding(t, report).Details, "No query statistics available")
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

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
	require.Contains(t, keyFinding(t, report).Details, "properly use partition keys")
}

func Test_PartitionUsage_QuotedPartitionKeyRecognized(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "booking_time_segments", "booking_id", 32),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats(
				`SELECT "booking_time_segments"."id", "booking_time_segments"."created_at", `+
					`"booking_time_segments"."updated_at", "booking_time_segments"."starts_at", `+
					`"booking_time_segments"."ends_at" FROM "booking_time_segments" `+
					`WHERE "booking_time_segments"."booking_id" = $1`,
				806_000_000,
				45_720_000,
			),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
	require.Contains(t, keyFinding(t, report).Details, "properly use partition keys")
}

func Test_PartitionUsage_PartitionLeafQueryIgnored(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "booking_time_segments", "booking_id", 32),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats(
				`SELECT * FROM booking_time_segments_17 WHERE customer_id = $1`,
				5000,
				4_000_000,
			),
		},
	}

	checker := partitionusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
}

// Partition-leaf and lookalike table names must not be attributed to the parent,
// while quoted or schema-qualified parent references still must be.
func Test_PartitionUsage_TableMatchingBoundaries(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		query      string
		shouldBeOK bool
	}{
		{
			name:       "schema-qualified leaf",
			query:      "SELECT * FROM public.orders_2025_01 WHERE customer_id = $1",
			shouldBeOK: true,
		},
		{
			name:       "quoted leaf",
			query:      `SELECT * FROM "orders_2025_01" WHERE customer_id = $1`,
			shouldBeOK: true,
		},
		{
			name:       "pg_partman style leaf",
			query:      "SELECT * FROM orders_p2025 WHERE customer_id = $1",
			shouldBeOK: true,
		},
		{
			name:       "prefix-sharing sibling table",
			query:      "SELECT * FROM customer_orders WHERE customer_id = $1",
			shouldBeOK: true,
		},
		{
			name:       "quoted schema-qualified parent",
			query:      `SELECT * FROM "public"."orders" WHERE customer_id = $1`,
			shouldBeOK: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				tables: []db.PartitionedTablesWithKeysRow{
					makePartitionedTable("public", "orders", "created_at", 12),
				},
				queryStats: []db.QueryStatsFromStatStatementsRow{
					makeQueryStats(tc.query, 5000, 4_000_000),
				},
			}

			checker := partitionusage.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			if tc.shouldBeOK {
				require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
			} else {
				require.NotEqual(t, check.SeverityPass, keyFinding(t, report).Severity)
			}
		})
	}
}

// Pruning depends on the partition strategy: range operators prune RANGE but
// never HASH or LIST, and composite RANGE keys need their leading column.
func Test_PartitionUsage_StrategyAwarePruning(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		strategy     string
		partitionKey string
		query        string
		shouldBeOK   bool
	}{
		{
			name:         "hash with equality prunes",
			strategy:     "h",
			partitionKey: "tenant_id",
			query:        "SELECT * FROM orders WHERE tenant_id = $1",
			shouldBeOK:   true,
		},
		{
			name:         "hash with range operator does not prune",
			strategy:     "h",
			partitionKey: "tenant_id",
			query:        "SELECT * FROM orders WHERE tenant_id > $1",
			shouldBeOK:   false,
		},
		{
			name:         "hash with BETWEEN does not prune",
			strategy:     "h",
			partitionKey: "tenant_id",
			query:        "SELECT * FROM orders WHERE tenant_id between $1 and $2",
			shouldBeOK:   false,
		},
		{
			name:         "hash with IN prunes",
			strategy:     "h",
			partitionKey: "tenant_id",
			query:        "SELECT * FROM orders WHERE tenant_id in ($1, $2)",
			shouldBeOK:   true,
		},
		{
			name:         "hash composite needs every key column",
			strategy:     "h",
			partitionKey: "tenant_id,region",
			query:        "SELECT * FROM orders WHERE tenant_id = $1",
			shouldBeOK:   false,
		},
		{
			name:         "hash composite with every key column prunes",
			strategy:     "h",
			partitionKey: "tenant_id,region",
			query:        "SELECT * FROM orders WHERE tenant_id = $1 AND region = $2",
			shouldBeOK:   true,
		},
		{
			name:         "list with equality prunes",
			strategy:     "l",
			partitionKey: "status",
			query:        "SELECT * FROM orders WHERE status = $1",
			shouldBeOK:   true,
		},
		{
			// Verified with EXPLAIN: PostgreSQL excludes LIST partitions whose
			// listed values cannot satisfy an inequality.
			name:         "list with range operator prunes",
			strategy:     "l",
			partitionKey: "status",
			query:        "SELECT * FROM orders WHERE status > $1",
			shouldBeOK:   true,
		},
		{
			name:         "range composite with leading column prunes",
			strategy:     "r",
			partitionKey: "tenant_id,created_at",
			query:        "SELECT * FROM orders WHERE tenant_id = $1",
			shouldBeOK:   true,
		},
		{
			name:         "range composite with trailing column only does not prune",
			strategy:     "r",
			partitionKey: "tenant_id,created_at",
			query:        "SELECT * FROM orders WHERE created_at >= $1",
			shouldBeOK:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				tables: []db.PartitionedTablesWithKeysRow{
					makePartitionedTableWithStrategy("public", "orders", tc.partitionKey, tc.strategy),
				},
				queryStats: []db.QueryStatsFromStatStatementsRow{
					makeQueryStats(tc.query, 5000, 4_000_000),
				},
			}

			report, err := partitionusage.New(queryer).Check(context.Background())
			require.NoError(t, err)

			if tc.shouldBeOK {
				require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
			} else {
				require.NotEqual(t, check.SeverityPass, keyFinding(t, report).Severity)
			}
		})
	}
}

// A plain UPDATE has no FROM clause, so its WHERE must still be inspected.
func Test_PartitionUsage_UpdateWithoutFrom(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		query      string
		shouldBeOK bool
	}{
		{"key in WHERE prunes", "UPDATE orders SET status = $1 WHERE created_at = $2", true},
		{"key absent", "UPDATE orders SET status = $1 WHERE customer_id = $2", false},
		{"DELETE keeps working", "DELETE FROM orders WHERE created_at < $1", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				tables: []db.PartitionedTablesWithKeysRow{
					makePartitionedTable("public", "orders", "created_at", 12),
				},
				queryStats: []db.QueryStatsFromStatStatementsRow{
					makeQueryStats(tc.query, 5000, 4_000_000),
				},
			}

			report, err := partitionusage.New(queryer).Check(context.Background())
			require.NoError(t, err)

			if tc.shouldBeOK {
				require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
			} else {
				require.NotEqual(t, check.SeverityPass, keyFinding(t, report).Severity)
			}
		})
	}
}

// The target table's own subquery carries the predicate that prunes it, while a
// subquery on another table must not count.
func Test_PartitionUsage_TargetTableInSubquery_NotFlagged(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM (SELECT * FROM orders WHERE created_at = $1) o", 5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
}

// A schema qualifier may be quoted independently of the table name.
func Test_PartitionUsage_MixedQuotingSchemaScoping(t *testing.T) {
	t.Parallel()

	const query = `SELECT * FROM tenant_a."orders" WHERE customer_id = $1`

	testCases := []struct {
		name       string
		schema     string
		shouldBeOK bool
	}{
		{"attributed to its own schema", "tenant_a", false},
		{"not attributed to a sibling schema", "tenant_b", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				tables: []db.PartitionedTablesWithKeysRow{
					makePartitionedTable(tc.schema, "orders", "created_at", 12),
				},
				queryStats: []db.QueryStatsFromStatStatementsRow{
					makeQueryStats(query, 5000, 4_000_000),
				},
			}

			report, err := partitionusage.New(queryer).Check(context.Background())
			require.NoError(t, err)

			if tc.shouldBeOK {
				require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
			} else {
				require.NotEqual(t, check.SeverityPass, keyFinding(t, report).Severity)
			}
		})
	}
}

// A partition key constrained through a JOIN condition can prune when the
// planner parameterizes the partitioned side, so it must not be reported.
func Test_PartitionUsage_KeyConstrainedInJoinCondition_NotFlagged(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "id", 6),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			// Ecto-shaped: the key is on the right of the operator, in ON, not WHERE.
			makeQueryStats(
				`SELECT DISTINCT ON (o0."id") o0."id", o0."reference" FROM "orders" AS o0 `+
					`INNER JOIN "order_items" AS o1 ON o1."order_id" = o0."id" WHERE (o1."type" = $1)`,
				2_404_213, 99_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
}

// The same column name constrained inside a subquery on a different table must
// not count — otherwise any subquery filtering "s.id" would silence a table
// partitioned by "id".
func Test_PartitionUsage_KeyConstrainedOnlyInSubquery_StillFlagged(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "id", 6),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats(
				`SELECT * FROM orders WHERE customer_id = $1 `+
					`AND EXISTS (SELECT 1 FROM shipments s WHERE s.id = $2)`,
				5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityFail, keyFinding(t, report).Severity)
}

// A table referenced under a different schema must not be attributed to this one.
func Test_PartitionUsage_CrossSchemaQueryNotAttributed(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("tenant_b", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM tenant_a.orders WHERE customer_id = $1", 5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
}

func Test_PartitionUsage_OwnSchemaQualifiedQueryAttributed(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("tenant_a", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM tenant_a.orders WHERE customer_id = $1", 5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityFail, keyFinding(t, report).Severity)
}

// A role that cannot read other users' query text gets a warning instead of a
// confident PASS on the fraction of the workload it can see.
func Test_PartitionUsage_HiddenQueryText_Warns(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		hiddenQueries: 42,
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders WHERE created_at > $1", 5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	var restricted *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDQueryTextRestricted {
			restricted = &report.Results[i]
			break
		}
	}

	require.NotNil(t, restricted)
	require.Equal(t, check.SeverityWarn, restricted.Severity)
	require.Contains(t, restricted.Details, "42")
}

func Test_PartitionUsage_NoHiddenQueryText_NoFinding(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders WHERE created_at > $1", 5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	for _, result := range report.Results {
		require.NotEqual(t, findingIDQueryTextRestricted, result.ID)
	}
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

	require.Equal(t, check.SeverityWarn, keyFinding(t, report).Severity)
	require.Contains(t, keyFinding(t, report).Details, "1 partitioned table")
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

	require.Equal(t, check.SeverityFail, keyFinding(t, report).Severity)
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

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
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

	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
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
	require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity)
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

	require.Equal(t, check.SeverityWarn, keyFinding(t, report).Severity)
	require.Contains(t, keyFinding(t, report).Details, "2 partitioned table")
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

	// Check that seq scan analysis still ran (doesn't need the extension)
	seqScanFinding := findingByID(t, report, findingIDHighSeqScanRatio)
	require.Equal(t, check.SeverityWarn, seqScanFinding.Severity)

	extensionFinding := findingByID(t, report, findingIDExtensionUnavailable)
	require.Equal(t, check.SeverityWarn, extensionFinding.Severity)
	require.Contains(t, extensionFinding.Details, "cannot analyze query patterns")
}

// The offending statements are listed in the finding itself, so an engineer can
// investigate without hand-writing pg_stat_statements queries.
func Test_PartitionUsage_ProblemQueriesListed(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats(`SELECT * FROM "orders" WHERE "customer_id" = $1`, 500, 400_000),
			makeQueryStats(`SELECT * FROM "orders" WHERE "status" = $1`, 900, 900_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	examples := keyFinding(t, report)

	// Fails because the two shapes exceed 1000 combined calls.
	require.Equal(t, check.SeverityFail, report.Severity)
	require.Equal(t, check.SeverityFail, examples.Severity)

	require.NotNil(t, examples.Table)
	require.Equal(t, []string{"Table", "Calls", "Total Time", "Query ID", "Query"}, examples.Table.Headers)
	require.Len(t, examples.Table.Rows, 2)
	require.Equal(t, 3, examples.Table.MaxRowsBrief, "brief output shows three, verbose shows all")
	require.Equal(t, check.SeverityFail, examples.Table.Rows[0].Severity)

	// Worst by total time first.
	require.Contains(t, examples.Table.Rows[0].Cells[4], `"status" = $1`)
	require.Contains(t, examples.Table.Rows[1].Cells[4], `"customer_id" = $1`)

	// The queryid is shown so the full text can be looked up.
	require.Equal(t, "12345", examples.Table.Rows[0].Cells[3])
	require.Contains(t, examples.Details, "pg_stat_statements WHERE queryid")
}

func Test_PartitionUsage_NoProblemQueries_NoTable(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders WHERE created_at > $1", 5000, 4_000_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	finding := keyFinding(t, report)
	require.Equal(t, check.SeverityPass, finding.Severity)
	require.Nil(t, finding.Table)
}

// Long ORM statements must not stretch the table beyond a terminal line.
func Test_PartitionUsage_ExampleQueryTextIsClipped(t *testing.T) {
	t.Parallel()

	// A multi-byte identifier straddling the clip boundary must not be split.
	longQuery := `SELECT "orders"."id", "orders"."reference", "orders"."customer_id", ` +
		`"orders"."provider_id", "orders"."ubicación", "orders"."employee_id", ` +
		`"orders"."location_id" FROM "orders" WHERE "orders"."status" = $1`
	require.Greater(t, len([]rune(longQuery)), 120, "fixture must exceed the clip width")

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats(longQuery, 500, 400_000),
		},
	}

	report, err := partitionusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	cell := keyFinding(t, report).Table.Rows[0].Cells[4]

	require.LessOrEqual(t, len([]rune(cell)), 120)
	require.True(t, strings.HasSuffix(cell, "…"), "clipped text is marked with an ellipsis")
	require.True(t, utf8.ValidString(cell), "clipping must not split a multi-byte rune")
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
	// The full normalized query text is analyzed, scoped to the current
	// database and top-level statements.
	require.Contains(t, metadata.SQL, `LOWER(REGEXP_REPLACE(query, '\s+', ' ', 'g'))::text AS query`)
	require.Contains(t, metadata.SQL, "AND toplevel")
	require.Contains(t, metadata.SQL, "d.datname = current_database()")
	// Statement type is matched on the leading keyword. Substring matching let
	// every INSERT carrying an "updated_at" column through as an "UPDATE".
	require.Contains(t, metadata.SQL, `query ~* '^\s*(WITH|SELECT|UPDATE|DELETE)\M'`)
	require.NotContains(t, metadata.SQL, "query ILIKE '%UPDATE%'")
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

	result := keyFinding(t, report)

	// One row per offending statement; the per-table totals live in Details.
	require.NotNil(t, result.Table)
	require.Equal(t, []string{"Table", "Calls", "Total Time", "Query ID", "Query"}, result.Table.Headers)
	require.Len(t, result.Table.Rows, 1)

	row := result.Table.Rows[0]
	require.Equal(t, "public.orders", row.Cells[0])
	require.Equal(t, "500", row.Cells[1])
	require.Equal(t, "12345", row.Cells[3])
	require.Contains(t, row.Cells[4], "customer_id = $1")
	require.Equal(t, check.SeverityWarn, row.Severity)

	require.Contains(t, result.Details, "1 statement(s) not using the partition key")
	require.Contains(t, result.Details, "public.orders (key: created_at, 12 partitions)")
	require.Contains(t, result.Details, "pg_stat_statements WHERE queryid")
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
			name:         "quoted column",
			query:        `SELECT * FROM orders WHERE "created_at" > $1`,
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "key as suffix of another column",
			query:        "SELECT * FROM orders WHERE customer_id = $1",
			partitionKey: "id",
			shouldBeOK:   false,
		},
		{
			name:         "column match with IS NULL",
			query:        "SELECT * FROM orders WHERE created_at IS NULL",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "IS NOT NULL does not prune",
			query:        "SELECT * FROM orders WHERE created_at IS NOT NULL AND customer_id = $1",
			partitionKey: "created_at",
			shouldBeOK:   false,
		},
		{
			name:         "not-equals does not prune",
			query:        "SELECT * FROM orders WHERE created_at <> $1",
			partitionKey: "created_at",
			shouldBeOK:   false,
		},
		{
			name:         "greater-or-equal",
			query:        "SELECT * FROM orders WHERE created_at >= $1",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "key on right-hand side of comparison",
			query:        "SELECT * FROM orders o WHERE $1 <= o.created_at",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "quoted key on right-hand side of comparison",
			query:        `SELECT * FROM orders WHERE $1 = "created_at"`,
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "not-equals with key on right-hand side does not prune",
			query:        "SELECT * FROM orders WHERE $1 <> created_at AND customer_id = $2",
			partitionKey: "created_at",
			shouldBeOK:   false,
		},
		{
			name:         "range containment operator does not prune",
			query:        "SELECT * FROM orders WHERE created_at <@ tstzrange($1, $2) AND customer_id = $3",
			partitionKey: "created_at",
			shouldBeOK:   false,
		},
		{
			name:         "key filtered after a subquery containing LIMIT",
			query:        "SELECT * FROM orders b WHERE (EXISTS (SELECT 1 FROM holds h WHERE h.order_id = b.id LIMIT 1)) AND b.created_at >= $1",
			partitionKey: "created_at",
			shouldBeOK:   true,
		},
		{
			name:         "key only in ORDER BY",
			query:        "SELECT * FROM orders WHERE customer_id = $1 ORDER BY created_at DESC",
			partitionKey: "created_at",
			shouldBeOK:   false,
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

			if tc.shouldBeOK {
				require.Equal(t, check.SeverityPass, keyFinding(t, report).Severity, "Expected OK for: %s", tc.name)
			} else {
				require.NotEqual(t, check.SeverityPass, keyFinding(t, report).Severity, "Expected not OK for: %s", tc.name)
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

// ORDER BY on the partition key prunes nothing — the JOIN must still be flagged.
func Test_PartitionUsage_JoinOrderByPartitionKey_StillFlagged(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "created_at", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders o JOIN order_items oi ON oi.order_id = o.id ORDER BY o.created_at DESC", 5000, 4_000_000),
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

// A key such as "id" must not match inside "customer_id" on the JOIN path.
func Test_PartitionUsage_JoinKeySubstring_StillFlagged(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		tables: []db.PartitionedTablesWithKeysRow{
			makePartitionedTable("public", "orders", "id", 12),
		},
		queryStats: []db.QueryStatsFromStatStatementsRow{
			makeQueryStats("SELECT * FROM orders o JOIN order_items oi ON oi.customer_id = o.customer_id", 5000, 4_000_000),
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

// Without query statistics there is nothing to measure coverage against.
