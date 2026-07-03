package indexusage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/indexusage"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

const (
	unusedIndexesID   = "unused-indexes"
	lowUsageIndexesID = "low-usage-indexes"
	indexCacheRatioID = "index-cache-ratio"
)

type mockIndexUsageQueryer struct {
	rows []db.IndexUsageStatsRow
	err  error
}

func (m *mockIndexUsageQueryer) IndexUsageStats(context.Context) ([]db.IndexUsageStatsRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func newMockQueryer(rows []db.IndexUsageStatsRow) *mockIndexUsageQueryer {
	return &mockIndexUsageQueryer{rows: rows}
}

func newMockQueryerWithError(err error) *mockIndexUsageQueryer {
	return &mockIndexUsageQueryer{err: err}
}

func makeNumeric(value float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.2f", value))
	return n
}

// indexRow builds a well-formed row so tests only set the fields they exercise.
type indexRow struct {
	table       string
	index       string
	scans       int64
	sizeBytes   int64
	tableWrites int64
	isPrimary   bool
	isUnique    bool
	cacheRatio  pgtype.Numeric
	blksHit     int64
	blksRead    int64
	indexdef    string
}

func (r indexRow) build() db.IndexUsageStatsRow {
	return db.IndexUsageStatsRow{
		TableName:      pgtype.Text{String: r.table, Valid: true},
		IndexName:      pgtype.Text{String: r.index, Valid: true},
		IdxScan:        pgtype.Int8{Int64: r.scans, Valid: true},
		IndexSizeBytes: pgtype.Int8{Int64: r.sizeBytes, Valid: true},
		TableWrites:    pgtype.Int8{Int64: r.tableWrites, Valid: true},
		IsPrimary:      r.isPrimary,
		IsUnique:       r.isUnique,
		CacheHitRatio:  r.cacheRatio,
		IdxBlksHit:     pgtype.Int8{Int64: r.blksHit, Valid: true},
		IdxBlksRead:    pgtype.Int8{Int64: r.blksRead, Valid: true},
		Indexdef:       pgtype.Text{String: r.indexdef, Valid: true},
	}
}

func rows(rr ...indexRow) []db.IndexUsageStatsRow {
	out := make([]db.IndexUsageStatsRow, 0, len(rr))
	for _, r := range rr {
		out = append(out, r.build())
	}
	return out
}

func findingByID(report *check.Report, id string) *check.Finding {
	for i := range report.Results {
		if report.Results[i].ID == id {
			return &report.Results[i]
		}
	}
	return nil
}

func runCheck(t *testing.T, data []db.IndexUsageStatsRow) *check.Report {
	t.Helper()
	report, err := indexusage.New(newMockQueryer(data)).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

// Canonical rows reused across cases.
var (
	unusedRow = indexRow{
		table: "users", index: "idx_users_unused",
		scans: 0, sizeBytes: 20 * 1024 * 1024, tableWrites: 50000,
		cacheRatio: makeNumeric(98.0), indexdef: "CREATE INDEX idx_users_unused ON users (deleted_at)",
	}
	lowUsageRow = indexRow{
		table: "posts", index: "idx_posts_status",
		scans: 500, sizeBytes: 10*1024*1024 + 1, tableWrites: 20000,
		cacheRatio: makeNumeric(95.0),
	}
	// Hot, large, well-exercised index with a low cache hit ratio: the only
	// shape index-cache-ratio should report.
	hotLowCacheRow = indexRow{
		table: "orders", index: "idx_orders_created",
		scans: 5000, sizeBytes: 150 * 1024 * 1024, tableWrites: 50000,
		cacheRatio: makeNumeric(85.0), blksHit: 90000, blksRead: 60000,
	}
)

func Test_IndexUsage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Rows             []db.IndexUsageStatsRow
		ExpectedSeverity check.Severity
		ExpectedFindings int
	}{
		{
			Name:             "no rows - single OK finding",
			Rows:             []db.IndexUsageStatsRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name:             "unused index - WARN",
			Rows:             rows(unusedRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
		{
			Name:             "low usage index - WARN",
			Rows:             rows(lowUsageRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
		{
			Name:             "hot large low-cache index - WARN",
			Rows:             rows(hotLowCacheRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
		{
			Name:             "mixed issues - WARN",
			Rows:             rows(unusedRow, lowUsageRow, hotLowCacheRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			report := runCheck(t, tc.Rows)

			require.Len(t, report.Results, tc.ExpectedFindings)
			require.Equal(t, tc.ExpectedSeverity, report.Severity)
			require.Equal(t, check.CategoryIndexes, report.Category)
		})
	}
}

func Test_IndexUsage_UnusedIndexesTable(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(
		unusedRow,
		indexRow{
			table: "posts", index: "idx_posts_unused",
			scans: 0, sizeBytes: 30 * 1024 * 1024, tableWrites: 30000,
			cacheRatio: makeNumeric(97.0), indexdef: "CREATE INDEX idx_posts_unused ON posts (archived_at)",
		},
	))

	f := findingByID(report, unusedIndexesID)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityWarn, f.Severity)
	require.Contains(t, f.Details, "2 unused indexes")

	require.NotNil(t, f.Table)
	require.Equal(t, []string{"Index", "Table", "Size", "Definition"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 2)

	first := f.Table.Rows[0]
	require.Equal(t, check.SeverityWarn, first.Severity)
	require.Equal(t, "idx_users_unused", first.Cells[0])
	require.Equal(t, "users", first.Cells[1])
	require.Equal(t, "20.0MiB", first.Cells[2])
	require.Equal(t, "CREATE INDEX idx_users_unused ON users (deleted_at)", first.Cells[3])
}

func Test_IndexUsage_LowUsageIndexesTable(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(indexRow{
		table: "comments", index: "idx_comments_status",
		scans: 500, sizeBytes: 10*1024*1024 + 1, tableWrites: 20000,
		cacheRatio: makeNumeric(96.0),
	}))

	f := findingByID(report, lowUsageIndexesID)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityWarn, f.Severity)
	require.Contains(t, f.Details, "low read usage but high write cost")

	require.NotNil(t, f.Table)
	require.Equal(t, []string{"Index", "Table", "Size", "Scans", "Table Writes"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)

	row := f.Table.Rows[0]
	require.Equal(t, check.SeverityWarn, row.Severity)
	require.Equal(t, []string{"idx_comments_status", "comments", "10.0MiB", "500", "20.0K"}, row.Cells)
}

func Test_IndexUsage_CacheRatioTable(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(hotLowCacheRow))

	f := findingByID(report, indexCacheRatioID)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityWarn, f.Severity)

	require.NotNil(t, f.Table)
	require.Equal(t, []string{"Index", "Table", "Size", "Scans", "Cache Hit %", "Blocks Read"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)

	row := f.Table.Rows[0]
	require.Equal(t, check.SeverityWarn, row.Severity)
	require.Equal(t, []string{"idx_orders_created", "orders", "150.0MiB", "5.0K", "85.00", "60.0K"}, row.Cells)
}

// Test_IndexUsage_CacheRatioGates proves the new gates: only hot, large,
// well-exercised indexes with a low ratio are reported. A cold index (few
// scans) with an equally bad ratio must NOT be reported.
func Test_IndexUsage_CacheRatioGates(t *testing.T) {
	t.Parallel()

	base := hotLowCacheRow // scans 5000, 150MiB, ratio 85%, blocks 150k

	testCases := []struct {
		Name     string
		Row      indexRow
		Reported bool
	}{
		{
			Name:     "hot, large, well-exercised, low ratio - reported",
			Row:      base,
			Reported: true,
		},
		{
			Name:     "cold index (<1000 scans) with low ratio - not reported",
			Row:      withScans(base, 999),
			Reported: false,
		},
		{
			Name:     "barely-touched (<100k blocks) with low ratio - not reported",
			Row:      withBlocks(base, 40000, 40000),
			Reported: false,
		},
		{
			Name:     "small index (<100MB) with low ratio - not reported",
			Row:      withSize(base, 50*1024*1024),
			Reported: false,
		},
		{
			Name:     "good ratio (>=90%) - not reported",
			Row:      withRatio(base, 95.0),
			Reported: false,
		},
		{
			Name:     "no cache data - not reported",
			Row:      withRatio(base, 0), // overwritten to invalid below
			Reported: false,
		},
	}

	// The "no cache data" case needs an invalid Numeric.
	testCases[len(testCases)-1].Row.cacheRatio = pgtype.Numeric{Valid: false}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			report := runCheck(t, rows(tc.Row))
			f := findingByID(report, indexCacheRatioID)
			require.NotNil(t, f)

			if tc.Reported {
				require.Equal(t, check.SeverityWarn, f.Severity)
				require.NotNil(t, f.Table)
			} else {
				require.Equal(t, check.SeverityOK, f.Severity)
				require.Nil(t, f.Table)
			}
		})
	}
}

func withScans(r indexRow, scans int64) indexRow   { r.scans = scans; return r }
func withSize(r indexRow, bytes int64) indexRow    { r.sizeBytes = bytes; return r }
func withRatio(r indexRow, ratio float64) indexRow { r.cacheRatio = makeNumeric(ratio); return r }
func withBlocks(r indexRow, hit, read int64) indexRow {
	r.blksHit, r.blksRead = hit, read
	return r
}

func Test_IndexUsage_SkipPrimaryAndUnique(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(
		indexRow{
			table: "users", index: "users_pkey",
			scans: 0, sizeBytes: 20 * 1024 * 1024, tableWrites: 50000,
			isPrimary: true, isUnique: true, cacheRatio: makeNumeric(98.0),
		},
		indexRow{
			table: "users", index: "idx_users_email_unique",
			scans: 0, sizeBytes: 15 * 1024 * 1024, tableWrites: 50000,
			isUnique: true, cacheRatio: makeNumeric(97.0),
		},
	))

	require.Equal(t, check.SeverityOK, report.Severity)
	for _, result := range report.Results {
		require.Equal(t, check.SeverityOK, result.Severity, "Primary/unique indexes should not be flagged")
	}
}

func Test_IndexUsage_SizeThresholds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Scans            int64
		SizeBytes        int64
		TableWrites      int64
		ShouldBeUnused   bool
		ShouldBeLowUsage bool
	}{
		{
			Name:           "unused but small (<10MB) - not flagged",
			Scans:          0,
			SizeBytes:      5 * 1024 * 1024,
			TableWrites:    50000,
			ShouldBeUnused: false,
		},
		{
			Name:           "unused and large (>10MB) - flagged",
			Scans:          0,
			SizeBytes:      20 * 1024 * 1024,
			TableWrites:    50000,
			ShouldBeUnused: true,
		},
		{
			Name:             "low scans (<1000) but low writes - not flagged",
			Scans:            500,
			SizeBytes:        10 * 1024 * 1024,
			TableWrites:      5000,
			ShouldBeLowUsage: false,
		},
		{
			Name:             "low scans (<1000) and high writes (>10k) - flagged",
			Scans:            500,
			SizeBytes:        10 * 1024 * 1024,
			TableWrites:      20000,
			ShouldBeLowUsage: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			report := runCheck(t, rows(indexRow{
				table: "test_table", index: "idx_test",
				scans: tc.Scans, sizeBytes: tc.SizeBytes, tableWrites: tc.TableWrites,
				cacheRatio: makeNumeric(98.0),
			}))

			unused := findingByID(report, unusedIndexesID)
			require.NotNil(t, unused)
			if tc.ShouldBeUnused {
				require.Equal(t, check.SeverityWarn, unused.Severity)
			} else {
				require.Equal(t, check.SeverityOK, unused.Severity)
			}

			lowUsage := findingByID(report, lowUsageIndexesID)
			require.NotNil(t, lowUsage)
			if tc.ShouldBeLowUsage {
				require.Equal(t, check.SeverityWarn, lowUsage.Severity)
			} else {
				require.Equal(t, check.SeverityOK, lowUsage.Severity)
			}
		})
	}
}

func Test_IndexUsage_NoCacheData(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(indexRow{
		table: "new_table", index: "idx_new",
		scans: 5000, sizeBytes: 20 * 1024 * 1024, tableWrites: 50000,
		cacheRatio: pgtype.Numeric{Valid: false},
	}))

	f := findingByID(report, indexCacheRatioID)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityOK, f.Severity, "Should be OK when no cache data")
}

func Test_IndexUsage_QueryError(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryerWithError(fmt.Errorf("database connection error"))

	_, err := indexusage.New(queryer).Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "index-usage", "Error should mention check ID")
}

func Test_IndexUsage_Metadata(t *testing.T) {
	t.Parallel()

	metadata := indexusage.New(newMockQueryer(nil)).Metadata()

	require.Equal(t, "index-usage", metadata.CheckID)
	require.Equal(t, "Index Usage", metadata.Name)
	require.Equal(t, check.CategoryIndexes, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}

func Test_IndexUsage_OKResult(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.IndexUsageStatsRow{})

	require.Len(t, report.Results, 1)
	result := report.Results[0]
	require.Equal(t, check.SeverityOK, result.Severity)
	require.Equal(t, "index-usage", result.ID)
	require.Empty(t, result.Details)
	require.Nil(t, result.Table)
}
