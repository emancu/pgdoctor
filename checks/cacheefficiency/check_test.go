package cacheefficiency_test

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	err        error
	indexError error
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

func newMockQueryer(row db.DatabaseCacheEfficiencyRow) *mockCacheEfficiencyQueryer {
	return &mockCacheEfficiencyQueryer{row: row}
}

func newMockQueryerWithError(err error) *mockCacheEfficiencyQueryer {
	return &mockCacheEfficiencyQueryer{err: err}
}

func makeNumeric(value float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.2f", value))
	return n
}

func indexRow(name string, sizeBytes, idxScan int64, ratio float64, statsReset pgtype.Timestamptz) db.IndexCacheEfficiencyRow {
	return db.IndexCacheEfficiencyRow{
		IndexName:      pgtype.Text{String: name, Valid: true},
		IndexSizeBytes: pgtype.Int8{Int64: sizeBytes, Valid: true},
		IdxScan:        pgtype.Int8{Int64: idxScan, Valid: true},
		CacheHitRatio:  makeNumeric(ratio),
		StatsReset:     statsReset,
	}
}

func mb(n int64) int64 { return n * 1024 * 1024 }

func windowDaysAgo(days int) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -days), Valid: true}
}

func nullWindow() pgtype.Timestamptz { return pgtype.Timestamptz{Valid: false} }

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
			Name: "no cache activity - OK",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: pgtype.Numeric{Valid: false},
				BlksHit:       pgtype.Int8{Int64: 0, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 0, Valid: true},
			},
			ExpectedSeverity: check.SeverityPass,
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
	require.Contains(t, result.Details, "healthy", "Details should mention healthy status")
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
	require.Equal(t, check.SeverityPass, result.Severity, "Should be OK when no cache activity")
	require.Contains(t, result.Details, "Insufficient cache activity", "Details should explain no activity")
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
		indexRow("public.idx_orders_created", mb(600), 6000, 70.0, windowDaysAgo(30)),
	})

	f := findFinding(t, report, "index-cache-ratio")
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Equal(t, []string{"Index", "Size", "Hit %"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)
	require.Equal(t, []string{"public.idx_orders_created", "600.0MiB", "70.0%"}, f.Table.Rows[0].Cells)
	require.Equal(t, check.SeverityInfo, f.Table.Rows[0].Severity)
	// Info must not escalate the report.
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_IndexCacheRatio_Gate(t *testing.T) {
	t.Parallel()

	// A 30-day window turns idx_scan into a scans/day rate: 3300 = 110/day (hot),
	// 2700 = 90/day (cold), 3000 = 100/day (exactly at the frequency floor).
	window := windowDaysAgo(30)

	tests := []struct {
		name       string
		size       int64
		idxScan    int64
		ratio      float64
		statsReset pgtype.Timestamptz
		listed     bool
	}{
		{"hot + big + low-ratio - listed", mb(600), 3300, 70.0, window, true},
		{"hot + big + ok-ratio - not listed", mb(600), 3300, 80.0, window, false},
		{"cold + big + low-ratio - not listed", mb(600), 2700, 70.0, window, false},
		{"hot + small - not listed", mb(400), 3300, 70.0, window, false},
		{"exactly 500MB + hot + low-ratio - listed", mb(500), 3300, 70.0, window, true},
		{"exactly 75% ratio - not listed", mb(600), 3300, 75.0, window, false},
		{"NULL window, lifetime >=10k - listed", mb(600), 20000, 70.0, nullWindow(), true},
		{"NULL window, lifetime <10k - not listed", mb(600), 5000, 70.0, nullWindow(), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
				indexRow("public.idx_x", tt.size, tt.idxScan, tt.ratio, tt.statsReset),
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
			CacheHitRatio:  pgtype.Numeric{Valid: false},
			StatsReset:     windowDaysAgo(30),
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
