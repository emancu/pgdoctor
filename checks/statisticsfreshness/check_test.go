package statisticsfreshness_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/statisticsfreshness"
	"github.com/emancu/pgdoctor/db"
)

type mockStatisticsFreshnessQueryer struct {
	row db.StatisticsFreshnessRow
	err error
}

func (m *mockStatisticsFreshnessQueryer) StatisticsFreshness(context.Context) (db.StatisticsFreshnessRow, error) {
	if m.err != nil {
		return db.StatisticsFreshnessRow{}, m.err
	}
	return m.row, nil
}

func newMockQueryer(row db.StatisticsFreshnessRow) *mockStatisticsFreshnessQueryer {
	return &mockStatisticsFreshnessQueryer{row: row}
}

func newMockQueryerWithError(err error) *mockStatisticsFreshnessQueryer {
	return &mockStatisticsFreshnessQueryer{err: err}
}

func makeInt4(value int32) pgtype.Int4 {
	return pgtype.Int4{Int32: value, Valid: true}
}

func makeTimestamp(daysAgo int) pgtype.Timestamptz {
	t := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func Test_StatisticsFreshness(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Row              db.StatisticsFreshnessRow
		ExpectedSeverity check.Severity
		ExpectedID       string
	}

	testCases := []testCase{
		{
			Name: "mature statistics (>7 days) - OK",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(10),
				AgeDays:    makeInt4(10),
			},
			ExpectedSeverity: check.SeverityOK,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "exactly 7 days - OK",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(7),
				AgeDays:    makeInt4(7),
			},
			ExpectedSeverity: check.SeverityOK,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "immature statistics (<7 days) - WARN",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(3),
				AgeDays:    makeInt4(3),
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "fresh statistics (1 day) - WARN",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(1),
				AgeDays:    makeInt4(1),
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "stats never reset (default) - OK",
			Row: db.StatisticsFreshnessRow{
				StatsReset: pgtype.Timestamptz{Valid: false},
				AgeDays:    pgtype.Int4{Valid: false},
			},
			ExpectedSeverity: check.SeverityOK,
			ExpectedID:       "statistics-freshness",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer(tc.Row)

			checker := statisticsfreshness.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, 1, len(results), "Should have exactly 1 result")

			result := results[0]
			require.Equal(t, tc.ExpectedID, result.ID, "Result ID should match")
			require.Equal(t, tc.ExpectedSeverity, result.Severity, "Result severity should match")
			require.Equal(t, check.CategoryConfigs, report.Category, "Category should be configs")
		})
	}
}

func Test_StatisticsFreshness_MatureStats(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: makeTimestamp(14),
		AgeDays:    makeInt4(14),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity)
	require.Contains(t, result.Details, "14 days old")
	require.Contains(t, result.Details, "mature enough")
}

func Test_StatisticsFreshness_ImmatureStats(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: makeTimestamp(3),
		AgeDays:    makeInt4(3),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity)
	require.Contains(t, result.Details, "3 days ago")
	require.Contains(t, result.Details, "less than 7 days")
	require.Contains(t, result.Details, "index-usage")
	require.Contains(t, result.Details, "table-seq-scans")
	require.Contains(t, result.Details, "cache-efficiency")
}

func Test_StatisticsFreshness_NeverReset(t *testing.T) {
	t.Parallel()

	// NULL stats_reset means statistics have NEVER been reset
	// This is the ideal state - maximum data accumulation
	row := db.StatisticsFreshnessRow{
		StatsReset: pgtype.Timestamptz{Valid: false},
		AgeDays:    pgtype.Int4{Valid: false},
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity)
	require.Contains(t, result.Details, "never been reset")
	require.Contains(t, result.Details, "optimal")
}

func Test_StatisticsFreshness_ThresholdBoundary(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		AgeDays          int32
		ExpectedSeverity check.Severity
	}

	testCases := []testCase{
		{
			Name:             "exactly 7 days - OK",
			AgeDays:          7,
			ExpectedSeverity: check.SeverityOK,
		},
		{
			Name:             "just below 7 days - WARN",
			AgeDays:          6,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "well above threshold - OK",
			AgeDays:          30,
			ExpectedSeverity: check.SeverityOK,
		},
		{
			Name:             "very fresh (1 day) - WARN",
			AgeDays:          1,
			ExpectedSeverity: check.SeverityWarn,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			row := db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(int(tc.AgeDays)),
				AgeDays:    makeInt4(tc.AgeDays),
			}

			queryer := newMockQueryer(row)

			checker := statisticsfreshness.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, 1, len(results))

			result := results[0]
			require.Equal(t, tc.ExpectedSeverity, result.Severity, "Severity should match expected")
		})
	}
}

func Test_StatisticsFreshness_AffectedChecks(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: makeTimestamp(3),
		AgeDays:    makeInt4(3),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Contains(t, result.Details, "index-usage", "Should mention index-usage check")
	require.Contains(t, result.Details, "table-seq-scans", "Should mention table-seq-scans check")
	require.Contains(t, result.Details, "cache-efficiency", "Should mention cache-efficiency check")
}

func Test_StatisticsFreshness_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := statisticsfreshness.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "statistics-freshness", "Error should mention check ID")
}

func Test_StatisticsFreshness_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer(db.StatisticsFreshnessRow{})
	checker := statisticsfreshness.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "statistics-freshness", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Statistics Freshness", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryConfigs, metadata.Category, "Category should be configs")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
	require.NotEmpty(t, metadata.SQL, "SQL should not be empty")
	require.NotEmpty(t, metadata.Readme, "Readme should not be empty")
}

func Test_StatisticsFreshness_VeryOldStats(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: makeTimestamp(90),
		AgeDays:    makeInt4(90),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Very old stats should still be OK")
	require.Contains(t, result.Details, "90 days old")
}

func Test_StatisticsFreshness_ZeroAge(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AgeDays:    makeInt4(0),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity, "Just-reset stats should be WARN")
	require.Contains(t, result.Details, "0 days ago")
}
