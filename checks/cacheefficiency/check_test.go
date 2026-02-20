package cacheefficiency_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/cacheefficiency"
	"github.com/emancu/pgdoctor/db"
)

type mockCacheEfficiencyQueryer struct {
	row db.DatabaseCacheEfficiencyRow
	err error
}

func (m *mockCacheEfficiencyQueryer) DatabaseCacheEfficiency(context.Context) (db.DatabaseCacheEfficiencyRow, error) {
	if m.err != nil {
		return db.DatabaseCacheEfficiencyRow{}, m.err
	}
	return m.row, nil
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
			ExpectedSeverity: check.SeverityOK,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "warning threshold (90-95%) - WARN",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(92.5),
				BlksHit:       pgtype.Int8{Int64: 925000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 75000, Valid: true},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "critical threshold (<90%) - FAIL",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: makeNumeric(85.0),
				BlksHit:       pgtype.Int8{Int64: 850000, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 150000, Valid: true},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "cache-hit-ratio",
		},
		{
			Name: "no cache activity - OK",
			Row: db.DatabaseCacheEfficiencyRow{
				CacheHitRatio: pgtype.Numeric{Valid: false},
				BlksHit:       pgtype.Int8{Int64: 0, Valid: true},
				BlksRead:      pgtype.Int8{Int64: 0, Valid: true},
			},
			ExpectedSeverity: check.SeverityOK,
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

			results := report.Results
			require.Equal(t, 1, len(results), "Should have exactly 1 result")

			result := results[0]
			require.Equal(t, tc.ExpectedID, result.ID, "Result ID should match")
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

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity)

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

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Should be OK when cache ratio is healthy")
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
			Name:             "exactly 95% - OK",
			CacheRatio:       95.0,
			ExpectedSeverity: check.SeverityOK,
		},
		{
			Name:             "just below 95% - WARN",
			CacheRatio:       94.9,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "exactly 90% - WARN",
			CacheRatio:       90.0,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "just below 90% - FAIL",
			CacheRatio:       89.9,
			ExpectedSeverity: check.SeverityWarn,
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

			results := report.Results
			require.Equal(t, 1, len(results))

			result := results[0]
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

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Should be OK when no cache activity")
	require.Contains(t, result.Details, "Insufficient cache activity", "Details should explain no activity")
}
