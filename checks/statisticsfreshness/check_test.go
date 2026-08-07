package statisticsfreshness_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/statisticsfreshness"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
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

func makeTimestamp(daysAgo int) pgtype.Timestamptz {
	t := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func makeAgeSeconds(daysAgo int) pgtype.Int8 {
	return pgtype.Int8{Int64: int64(daysAgo) * 24 * 60 * 60, Valid: true}
}

// resetAgo: an explicit reset, so the window is exact.
func resetAgo(days int) db.StatisticsFreshnessRow {
	return db.StatisticsFreshnessRow{
		StatsReset:    makeTimestamp(days),
		AgeSeconds:    makeAgeSeconds(days),
		UptimeSeconds: makeAgeSeconds(days),
	}
}

// noResetUptime: nothing recorded, so the window is only known to be >= uptime.
func noResetUptime(days int) db.StatisticsFreshnessRow {
	return db.StatisticsFreshnessRow{
		StatsReset:    pgtype.Timestamptz{Valid: false},
		AgeSeconds:    pgtype.Int8{Valid: false},
		UptimeSeconds: makeAgeSeconds(days),
	}
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
				AgeSeconds: makeAgeSeconds(int(10)), UptimeSeconds: makeAgeSeconds(10),
			},
			ExpectedSeverity: check.SeverityPass,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "exactly 7 days - OK",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(7),
				AgeSeconds: makeAgeSeconds(int(7)), UptimeSeconds: makeAgeSeconds(7),
			},
			ExpectedSeverity: check.SeverityPass,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "immature statistics (<7 days) - WARN",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(3),
				AgeSeconds: makeAgeSeconds(int(3)), UptimeSeconds: makeAgeSeconds(3),
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name: "fresh statistics (1 day) - WARN",
			Row: db.StatisticsFreshnessRow{
				StatsReset: makeTimestamp(1),
				AgeSeconds: makeAgeSeconds(int(1)), UptimeSeconds: makeAgeSeconds(1),
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "statistics-freshness",
		},
		{
			Name:             "stats never reset (default) - OK",
			Row:              noResetUptime(300),
			ExpectedSeverity: check.SeverityPass,
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
		AgeSeconds: makeAgeSeconds(int(14)),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityPass, result.Severity)
	require.Equal(t, "Statistics: 14d since last reset", result.Name)
}

func Test_StatisticsFreshness_ImmatureStats(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: makeTimestamp(3),
		AgeSeconds: makeAgeSeconds(int(3)), UptimeSeconds: makeAgeSeconds(3),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity)
	require.Equal(t, "Statistics: 3d since last reset", result.Name)
	require.Contains(t, result.Details, "less than the 7 days recommended")
	require.Contains(t, result.Details, "index-usage")
	require.Contains(t, result.Details, "table-seq-scans")
	require.Contains(t, result.Details, "cache-efficiency")
}

func Test_StatisticsFreshness_NeverReset(t *testing.T) {
	t.Parallel()

	// NULL stats_reset means statistics have NEVER been reset
	// This is the ideal state - maximum data accumulation
	row := noResetUptime(300)

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityPass, result.Severity)
	require.Equal(t, "Statistics: at least 300d, no reset recorded", result.Name)
	require.NotContains(t, result.Details, "optimal")
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
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "just below 7 days - WARN",
			AgeDays:          6,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "well above threshold - OK",
			AgeDays:          30,
			ExpectedSeverity: check.SeverityPass,
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
				AgeSeconds: makeAgeSeconds(int(tc.AgeDays)),
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
		AgeSeconds: makeAgeSeconds(int(3)), UptimeSeconds: makeAgeSeconds(3),
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
		AgeSeconds: makeAgeSeconds(int(90)),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityPass, result.Severity, "Very old stats should still be OK")
	require.Equal(t, "Statistics: 90d since last reset", result.Name)
}

func Test_StatisticsFreshness_ZeroAge(t *testing.T) {
	t.Parallel()

	row := db.StatisticsFreshnessRow{
		StatsReset: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		AgeSeconds: makeAgeSeconds(int(0)),
	}

	queryer := newMockQueryer(row)

	checker := statisticsfreshness.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity, "Just-reset stats should be WARN")
	// Regression: the age used to come from a truncated day count, so anything under
	// 24h reported "reset 0 days ago".
	require.Equal(t, "Statistics: 0s since last reset", result.Name)
	require.NotContains(t, result.Name, "0 days")
}

// The case the check used to miss. A crash, unclean shutdown or rebuilt replica
// zeroes the counters and leaves stats_reset NULL, so judging only explicit resets
// warned about the deliberate reset an operator already knew about and stayed silent
// about the one that silently invalidated every usage-based check.
func Test_StatisticsFreshness_UnrecordedResetIsCaughtByUptime(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Row              db.StatisticsFreshnessRow
		ExpectedSeverity check.Severity
		ExpectedTitle    string
	}{
		{
			Name:             "no reset, server up for months",
			Row:              noResetUptime(288),
			ExpectedSeverity: check.SeverityPass,
			ExpectedTitle:    "Statistics: at least 288d, no reset recorded",
		},
		{
			Name:             "no reset, server restarted two days ago",
			Row:              noResetUptime(2),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedTitle:    "Statistics: at least 2d, no reset recorded",
		},
		{
			Name:             "explicit reset, mature",
			Row:              resetAgo(30),
			ExpectedSeverity: check.SeverityPass,
			ExpectedTitle:    "Statistics: 30d since last reset",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			report, err := statisticsfreshness.New(newMockQueryer(tc.Row)).Check(context.Background())
			require.NoError(t, err)
			require.Len(t, report.Results, 1)

			require.Equal(t, tc.ExpectedSeverity, report.Results[0].Severity)
			require.Equal(t, tc.ExpectedTitle, report.Results[0].Name)
		})
	}
}
