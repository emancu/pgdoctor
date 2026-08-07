package cacheefficiency_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/cacheefficiency"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type mockCacheEfficiencyQueryer struct {
	row        db.DatabaseCacheEfficiencyRow
	indexRows  []db.IndexCacheEfficiencyRow
	tableRows  []db.TableCacheEfficiencyRow
	err        error
	indexError error
	tableError error
}

func (m *mockCacheEfficiencyQueryer) DatabaseCacheEfficiency(context.Context) (db.DatabaseCacheEfficiencyRow, error) {
	if m.err != nil {
		return db.DatabaseCacheEfficiencyRow{}, m.err
	}
	return m.row, nil
}

func (m *mockCacheEfficiencyQueryer) IndexCacheEfficiency(context.Context) ([]db.IndexCacheEfficiencyRow, error) {
	if m.indexError != nil {
		return nil, m.indexError
	}
	return m.indexRows, nil
}

func (m *mockCacheEfficiencyQueryer) TableCacheEfficiency(context.Context) ([]db.TableCacheEfficiencyRow, error) {
	if m.tableError != nil {
		return nil, m.tableError
	}
	return m.tableRows, nil
}

func newMockQueryer(row db.DatabaseCacheEfficiencyRow) *mockCacheEfficiencyQueryer {
	return &mockCacheEfficiencyQueryer{row: row}
}

func newMockQueryerWithError(err error) *mockCacheEfficiencyQueryer {
	return &mockCacheEfficiencyQueryer{err: err}
}

func makeNumeric(value float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(value, 'f', -1, 64))
	return n
}

func indexRow(name string, sizeBytes, idxScan, rank int64, share, ratio float64) db.IndexCacheEfficiencyRow {
	return db.IndexCacheEfficiencyRow{
		IndexName:      pgtype.Text{String: name, Valid: true},
		IndexSizeBytes: pgtype.Int8{Int64: sizeBytes, Valid: true},
		IdxScan:        pgtype.Int8{Int64: idxScan, Valid: true},
		ScanRank:       pgtype.Int8{Int64: rank, Valid: true},
		ScanShare:      makeNumeric(share),
		CacheHitRatio:  makeNumeric(ratio),
	}
}

func tableRow(name string, sizeBytes, reads, rank int64, share, ratio float64) db.TableCacheEfficiencyRow {
	return db.TableCacheEfficiencyRow{
		TableName:      pgtype.Text{String: name, Valid: true},
		TableSizeBytes: pgtype.Int8{Int64: sizeBytes, Valid: true},
		Reads:          pgtype.Int8{Int64: reads, Valid: true},
		ReadRank:       pgtype.Int8{Int64: rank, Valid: true},
		ReadShare:      makeNumeric(share),
		CacheHitRatio:  makeNumeric(ratio),
	}
}

func mb(n int64) int64 { return n * 1024 * 1024 }

func findFinding(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()
	for _, f := range report.Results {
		if f.ID == id {
			return f
		}
	}
	require.Failf(t, "missing finding", "no finding with id %q", id)
	return check.Finding{}
}

func Test_CacheEfficiency(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Row              db.DatabaseCacheEfficiencyRow
		ExpectedSeverity check.Severity
		ExpectedID       string
	}

	testCases := []testCase{
		{
			Name: "healthy cache hit ratio (>95%) - OK",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(98.5),
				BlksHit:       pgtype.Int8{Int64: 1000000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 15000, Valid: true},
			},
			ExpectedSeverity: check.SeverityPass,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "above 60% threshold (85%) - OK",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(85.0),
				BlksHit:       pgtype.Int8{Int64: 850000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 150000, Valid: true},
			},
			ExpectedSeverity: check.SeverityPass,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "low ratio (<60%) - INFO",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(55.0),
				BlksHit:       pgtype.Int8{Int64: 550000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 450000, Valid: true},
			},
			ExpectedSeverity: check.SeverityInfo,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "no cache activity - SKIP",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: pgtype.Numeric{Valid: false},
				BlksHit:       pgtype.Int8{Int64: 0, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 0, Valid: true},
			},
			ExpectedSeverity: check.SeveritySkip,
			ExpectedID:       "cache-hit-ratio",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer(tc.Row)

			checker := cacheefficiency.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			result := findFinding(t, report, tc.ExpectedID)
			require.Equal(t, tc.ExpectedSeverity, result.Severity, "Result severity should match")
			require.Equal(t, check.CategoryPerformance, report.Category, "Category should be performance")
		})
	}
}

func Test_CacheEfficiency_DetailsContent(t *testing.T) {
	t.Parallel()

	row := db.DatabaseCacheEfficiencyRow{
		CacheHitRatio: makeNumeric(55.0),
		BlksHit:       pgtype.Int8{Int64: 550000, Valid: true},
		BlksRead:      pgtype.Int8{Int64: 450000, Valid: true},
	}

	queryer := newMockQueryer(row)

	checker := cacheefficiency.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	result := findFinding(t, report, "cache-hit-ratio")
	require.Equal(t, check.SeverityInfo, result.Severity)

	require.Contains(t, result.Details, "55.00%", "Details should contain cache ratio")
	require.Contains(t, result.Details, "550000", "Details should contain blocks hit")
	require.Contains(t, result.Details, "450000", "Details should contain blocks read")
}

func Test_CacheEfficiency_OKResult(t *testing.T) {
	t.Parallel()

	row := db.DatabaseCacheEfficiencyRow{
		CacheHitRatio: makeNumeric(99.0),
		BlksHit:       pgtype.Int8{Int64: 990000, Valid: true},
		BlksRead:      pgtype.Int8{Int64: 10000, Valid: true},
	}

	queryer := newMockQueryer(row)

	checker := cacheefficiency.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	result := findFinding(t, report, "cache-hit-ratio")
	require.Equal(t, check.SeverityPass, result.Severity, "Should be OK when cache ratio is healthy")
	require.Contains(t, result.Name, "Cache Hit Ratio: ", "the ratio belongs in the title, which renders when passing")
}

func Test_CacheEfficiency_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := cacheefficiency.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "cache-efficiency", "Error should mention check ID")
}

func Test_CacheEfficiency_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer(db.DatabaseCacheEfficiencyRow{})
	checker := cacheefficiency.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "cache-efficiency", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Cache Efficiency", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryPerformance, metadata.Category, "Category should be performance")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
	require.NotEmpty(t, metadata.SQL, "SQL should not be empty")
	require.NotEmpty(t, metadata.Readme, "Readme should not be empty")
}

func Test_CacheEfficiency_ThresholdBoundaries(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		CacheRatio       float64
		ExpectedSeverity check.Severity
	}

	testCases := []testCase{
		{
			Name:             "well above threshold - OK",
			CacheRatio:       95.0,
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "just above threshold - OK",
			CacheRatio:       60.1,
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "exactly 60% - OK",
			CacheRatio:       60.0,
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "just below 60% - INFO",
			CacheRatio:       59.9,
			ExpectedSeverity: check.SeverityInfo,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			row := db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(tc.CacheRatio),
				BlksHit:       pgtype.Int8{Int64: 1000000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 100000, Valid: true},
			}

			queryer := newMockQueryer(row)

			checker := cacheefficiency.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			result := findFinding(t, report, "cache-hit-ratio")
			require.Equal(t, tc.ExpectedSeverity, result.Severity, "Severity should match expected")
		})
	}
}

func Test_CacheEfficiency_NoActivityHandling(t *testing.T) {
	t.Parallel()

	row := db.DatabaseCacheEfficiencyRow{
		CacheHitRatio: pgtype.Numeric{Valid: false},
		BlksHit:       pgtype.Int8{Int64: 0, Valid: true},
		BlksRead:      pgtype.Int8{Int64: 0, Valid: true},
	}

	queryer := newMockQueryer(row)

	checker := cacheefficiency.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	result := findFinding(t, report, "cache-hit-ratio")
	require.Equal(t, check.SeveritySkip, result.Severity, "no blocks read or hit means nothing was measured")
	require.Contains(t, result.Details, "no ratio to report", "Details should explain why")
}

// healthyDBRow isolates per-index findings from the database-wide finding.
func healthyDBRow() db.DatabaseCacheEfficiencyRow {
	return db.DatabaseCacheEfficiencyRow{
		CacheHitRatio: makeNumeric(99.0),
		BlksHit:       pgtype.Int8{Int64: 990000, Valid: true},
		BlksRead:      pgtype.Int8{Int64: 10000, Valid: true},
	}
}

func runWithIndexRows(t *testing.T, rows []db.IndexCacheEfficiencyRow) *check.Report {
	t.Helper()

	queryer := &mockCacheEfficiencyQueryer{row: healthyDBRow(), indexRows: rows}
	report, err := cacheefficiency.New(queryer).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

func Test_IndexCacheRatio_Informational(t *testing.T) {
	t.Parallel()

	report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
		indexRow("public.idx_orders_created", mb(600), 60000, 5, 0.5, 70.0),
	})

	f := findFinding(t, report, "index-cache-ratio")
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Equal(t, []string{"Size", "Hit %", "Index"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)
	require.Equal(t, []string{"600.0MiB", "70.0%", "public.idx_orders_created"}, f.Table.Rows[0].Cells)
	require.Equal(t, check.SeverityInfo, f.Table.Rows[0].Severity)
	// Info must not escalate the report.
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_IndexCacheRatio_Gate(t *testing.T) {
	t.Parallel()

	// hot = (scan_rank <= 20 OR scan_share >= 1%) AND idx_scan >= 10,000.
	tests := []struct {
		name    string
		size    int64
		idxScan int64
		rank    int64
		share   float64
		ratio   float64
		listed  bool
	}{
		{"rank 20 + low share - listed", mb(600), 50000, 20, 0.001, 70.0, true},
		{"rank 21 + low share - not listed", mb(600), 50000, 21, 0.001, 70.0, false},
		{"rank 21 + share exactly 1% - listed", mb(600), 50000, 21, 0.01, 70.0, true},
		{"rank 21 + share just below 1% - not listed", mb(600), 50000, 21, 0.009, 70.0, false},
		{"scan floor exactly 10,000 - listed", mb(600), 10000, 5, 0.5, 70.0, true},
		{"scan floor 9,999 - not listed", mb(600), 9999, 5, 0.5, 70.0, false},
		{"hot + ok-ratio - not listed", mb(600), 50000, 5, 0.5, 80.0, false},
		{"hot + exactly 75% ratio - not listed", mb(600), 50000, 5, 0.5, 75.0, false},
		{"hot + small - not listed", mb(400), 50000, 5, 0.5, 70.0, false},
		{"hot + exactly 500MB - listed", mb(500), 50000, 5, 0.5, 70.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
				indexRow("public.idx_x", tt.size, tt.idxScan, tt.rank, tt.share, tt.ratio),
			})
			f := findFinding(t, report, "index-cache-ratio")

			if tt.listed {
				require.Equal(t, check.SeverityInfo, f.Severity)
				require.Len(t, f.Table.Rows, 1)
			} else {
				require.Equal(t, check.SeverityPass, f.Severity)
				require.Nil(t, f.Table)
			}
		})
	}
}

func Test_IndexCacheRatio_NullRatioSkipped(t *testing.T) {
	t.Parallel()

	// Hot, big index whose ratio is NULL (no block activity) must still be skipped.
	report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
		{
			IndexName:      pgtype.Text{String: "public.idx_idle", Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: mb(600), Valid: true},
			IdxScan:        pgtype.Int8{Int64: 20000, Valid: true},
			ScanRank:       pgtype.Int8{Int64: 5, Valid: true},
			ScanShare:      makeNumeric(0.5),
			CacheHitRatio:  pgtype.Numeric{Valid: false},
		},
	})

	f := findFinding(t, report, "index-cache-ratio")
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Nil(t, f.Table)
}

func Test_IndexCacheRatio_NoIndexes_Pass(t *testing.T) {
	t.Parallel()

	report := runWithIndexRows(t, nil)

	f := findFinding(t, report, "index-cache-ratio")
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Nil(t, f.Table)
}

func Test_IndexCacheRatio_QueryError(t *testing.T) {
	t.Parallel()

	queryer := &mockCacheEfficiencyQueryer{
		row:        healthyDBRow(),
		indexError: fmt.Errorf("connection refused"),
	}
	_, err := cacheefficiency.New(queryer).Check(context.Background())
	require.ErrorContains(t, err, "cache-efficiency")
}

func Test_IndexCacheRatio_DebugTopIndexes(t *testing.T) {
	t.Parallel()

	report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
		indexRow("public.idx_big_cold", mb(600), 60000, 2, 0.30, 70.0),
		indexRow("public.idx_small_hot", mb(100), 90000, 1, 0.45, 99.0),
		indexRow("public.idx_unranked", mb(700), 500, 30, 0.001, 50.0),
	})

	f := findFinding(t, report, "index-cache-ratio")
	require.Contains(t, f.Debug, "Top indexes by scans:")
	require.Contains(t, f.Debug, "#1  hit 99.0%  share 45.0%  scans 90.0K  public.idx_small_hot")
	require.Contains(t, f.Debug, "#2  hit 70.0%  share 30.0%  scans 60.0K  public.idx_big_cold")
	require.NotContains(t, f.Debug, "idx_unranked")
	require.Less(t, strings.Index(f.Debug, "#1"), strings.Index(f.Debug, "#2"))
}

func Test_IndexCacheRatio_DebugOnPass(t *testing.T) {
	t.Parallel()

	report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
		indexRow("public.idx_healthy", mb(600), 90000, 1, 0.45, 99.0),
	})

	f := findFinding(t, report, "index-cache-ratio")
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Contains(t, f.Debug, "scans 90.0K  public.idx_healthy")
}

func runWithTableRows(t *testing.T, rows []db.TableCacheEfficiencyRow) *check.Report {
	t.Helper()

	queryer := &mockCacheEfficiencyQueryer{row: healthyDBRow(), tableRows: rows}
	report, err := cacheefficiency.New(queryer).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

func Test_TableCacheRatio_Informational(t *testing.T) {
	t.Parallel()

	report := runWithTableRows(t, []db.TableCacheEfficiencyRow{
		tableRow("public.orders", mb(600), 60000, 5, 0.5, 70.0),
	})

	f := findFinding(t, report, "table-cache-ratio")
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Equal(t, []string{"Size", "Hit %", "Table"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)
	require.Equal(t, []string{"600.0MiB", "70.0%", "public.orders"}, f.Table.Rows[0].Cells)
	require.Equal(t, check.SeverityInfo, f.Table.Rows[0].Severity)
	// Info must not escalate the report.
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_TableCacheRatio_Gate(t *testing.T) {
	t.Parallel()

	// hot = (read_rank <= 20 OR read_share >= 1%) AND reads >= 10,000.
	tests := []struct {
		name   string
		size   int64
		reads  int64
		rank   int64
		share  float64
		ratio  float64
		listed bool
	}{
		{"rank 20 + low share - listed", mb(600), 50000, 20, 0.001, 70.0, true},
		{"rank 21 + low share - not listed", mb(600), 50000, 21, 0.001, 70.0, false},
		{"rank 21 + share exactly 1% - listed", mb(600), 50000, 21, 0.01, 70.0, true},
		{"rank 21 + share just below 1% - not listed", mb(600), 50000, 21, 0.009, 70.0, false},
		{"read floor exactly 10,000 - listed", mb(600), 10000, 5, 0.5, 70.0, true},
		{"read floor 9,999 - not listed", mb(600), 9999, 5, 0.5, 70.0, false},
		{"hot + ok-ratio - not listed", mb(600), 50000, 5, 0.5, 80.0, false},
		{"hot + exactly 75% ratio - not listed", mb(600), 50000, 5, 0.5, 75.0, false},
		{"hot + small - not listed", mb(400), 50000, 5, 0.5, 70.0, false},
		{"hot + exactly 500MB - listed", mb(500), 50000, 5, 0.5, 70.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := runWithTableRows(t, []db.TableCacheEfficiencyRow{
				tableRow("public.t", tt.size, tt.reads, tt.rank, tt.share, tt.ratio),
			})
			f := findFinding(t, report, "table-cache-ratio")

			if tt.listed {
				require.Equal(t, check.SeverityInfo, f.Severity)
				require.Len(t, f.Table.Rows, 1)
			} else {
				require.Equal(t, check.SeverityPass, f.Severity)
				require.Nil(t, f.Table)
			}
		})
	}
}

func Test_TableCacheRatio_NullRatioSkipped(t *testing.T) {
	t.Parallel()

	// Hot, big table whose ratio is NULL (no heap block activity) must still be skipped.
	report := runWithTableRows(t, []db.TableCacheEfficiencyRow{
		{
			TableName:      pgtype.Text{String: "public.idle", Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: mb(600), Valid: true},
			Reads:          pgtype.Int8{Int64: 20000, Valid: true},
			ReadRank:       pgtype.Int8{Int64: 5, Valid: true},
			ReadShare:      makeNumeric(0.5),
			CacheHitRatio:  pgtype.Numeric{Valid: false},
		},
	})

	f := findFinding(t, report, "table-cache-ratio")
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Nil(t, f.Table)
}

func Test_TableCacheRatio_NoTables_Pass(t *testing.T) {
	t.Parallel()

	report := runWithTableRows(t, nil)

	f := findFinding(t, report, "table-cache-ratio")
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Nil(t, f.Table)
}

func Test_TableCacheRatio_QueryError(t *testing.T) {
	t.Parallel()

	queryer := &mockCacheEfficiencyQueryer{
		row:        healthyDBRow(),
		tableError: fmt.Errorf("connection refused"),
	}
	_, err := cacheefficiency.New(queryer).Check(context.Background())
	require.ErrorContains(t, err, "cache-efficiency")
}

func Test_TableCacheRatio_DebugTopTables(t *testing.T) {
	t.Parallel()

	report := runWithTableRows(t, []db.TableCacheEfficiencyRow{
		tableRow("public.big_cold", mb(600), 60000, 2, 0.30, 70.0),
		tableRow("public.small_hot", mb(100), 90000, 1, 0.45, 99.0),
		tableRow("public.unranked", mb(700), 500, 30, 0.001, 50.0),
	})

	f := findFinding(t, report, "table-cache-ratio")
	require.Contains(t, f.Debug, "Top tables by reads:")
	require.Contains(t, f.Debug, "#1  hit 99.0%  share 45.0%  reads 90.0K  public.small_hot")
	require.Contains(t, f.Debug, "#2  hit 70.0%  share 30.0%  reads 60.0K  public.big_cold")
	require.NotContains(t, f.Debug, "unranked")
	require.Less(t, strings.Index(f.Debug, "#1"), strings.Index(f.Debug, "#2"))
}

func Test_TableCacheRatio_DebugOnPass(t *testing.T) {
	t.Parallel()

	report := runWithTableRows(t, []db.TableCacheEfficiencyRow{
		tableRow("public.healthy", mb(600), 90000, 1, 0.45, 99.0),
	})

	f := findFinding(t, report, "table-cache-ratio")
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Contains(t, f.Debug, "reads 90.0K  public.healthy")
}
