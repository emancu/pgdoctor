package cacheefficiency_test

import (
	"context"
	"fmt"
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

func indexRow(name string, sizeBytes int64, ratio float64) db.IndexCacheEfficiencyRow {
	return db.IndexCacheEfficiencyRow{
		IndexName:      pgtype.Text{String: name, Valid: true},
		IndexSizeBytes: pgtype.Int8{Int64: sizeBytes, Valid: true},
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
			Name: "mid band (90-95%) - OK",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(92.5),
				BlksHit:       pgtype.Int8{Int64: 925000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 75000, Valid: true},
			},
			ExpectedSeverity: check.SeverityPass,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "low ratio (<90%) - INFO",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(85.0),
				BlksHit:       pgtype.Int8{Int64: 850000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 150000, Valid: true},
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
		CacheHitRatio: makeNumeric(85.0),
		BlksHit:       pgtype.Int8{Int64: 850000, Valid: true},
		BlksRead:      pgtype.Int8{Int64: 150000, Valid: true},
	}

	queryer := newMockQueryer(row)

	checker := cacheefficiency.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	result := findFinding(t, report, "cache-hit-ratio")
	require.Equal(t, check.SeverityInfo, result.Severity)

	require.Contains(t, result.Details, "85.00%", "Details should contain cache ratio")
	require.Contains(t, result.Details, "850000", "Details should contain blocks hit")
	require.Contains(t, result.Details, "150000", "Details should contain blocks read")
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
			Name:             "mid band now OK - OK",
			CacheRatio:       92.5,
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "exactly 90% - OK",
			CacheRatio:       90.0,
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "just below 90% - INFO",
			CacheRatio:       89.9,
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
		indexRow("public.idx_orders_created", mb(200), 85.0),
	})

	f := findFinding(t, report, "index-cache-ratio")
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Equal(t, []string{"Index", "Size", "Hit %"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)
	require.Equal(t, []string{"public.idx_orders_created", "200.0MiB", "85.0%"}, f.Table.Rows[0].Cells)
	require.Equal(t, check.SeverityInfo, f.Table.Rows[0].Severity)
	// Info must not escalate the report.
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_IndexCacheRatio_Thresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		size   int64
		ratio  float64
		listed bool
	}{
		{"large index below 90% (fail tier) - listed", mb(101), 89.0, true},
		{"large index at 94% (warn tier) - listed", mb(101), 94.0, true},
		{"large index at 95% - not listed", mb(101), 95.0, false},
		{"medium index below 95% (warn tier) - listed", mb(11), 94.0, true},
		{"medium index at 95% - not listed", mb(11), 95.0, false},
		{"index at 10MB floor, 94% - not listed", mb(10), 94.0, false},
		{"healthy large index - not listed", mb(500), 99.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
				indexRow("public.idx_x", tt.size, tt.ratio),
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

	report := runWithIndexRows(t, []db.IndexCacheEfficiencyRow{
		{
			IndexName:      pgtype.Text{String: "public.idx_idle", Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: mb(500), Valid: true},
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
