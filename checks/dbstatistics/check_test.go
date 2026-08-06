package dbstatistics_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/dbstatistics"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type mockDBStatisticsQueryer struct {
	row db.DBStatisticsRow
	err error
}

func (m *mockDBStatisticsQueryer) DBStatistics(context.Context) (db.DBStatisticsRow, error) {
	if m.err != nil {
		return db.DBStatisticsRow{}, m.err
	}

	return m.row, nil
}

func newMockQueryer(row db.DBStatisticsRow) *mockDBStatisticsQueryer {
	return &mockDBStatisticsQueryer{row: row}
}

func newMockQueryerWithError(err error) *mockDBStatisticsQueryer {
	return &mockDBStatisticsQueryer{err: err}
}

func makeInt4(value int32) pgtype.Int4 {
	return pgtype.Int4{Int32: value, Valid: true}
}

// resetAgo builds a row whose counters were reset the given duration ago. AgeDays is
// deliberately left unset: the check must derive the age from the timestamp.
func resetAgo(d time.Duration) db.DBStatisticsRow {
	return db.DBStatisticsRow{
		StatsReset: pgtype.Timestamptz{Time: time.Now().Add(-d), Valid: true},
	}
}

func neverReset() db.DBStatisticsRow {
	return db.DBStatisticsRow{
		StatsReset: pgtype.Timestamptz{Valid: false},
		AgeDays:    pgtype.Int4{Valid: false},
	}
}

const day = 24 * time.Hour

func runCheck(t *testing.T, row db.DBStatisticsRow) check.Finding {
	t.Helper()

	report, err := dbstatistics.New(newMockQueryer(row)).Check(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Results, 1, "check reports exactly one finding")
	require.Equal(t, check.CategoryConfigs, report.Category)

	return report.Results[0]
}

func Test_DBStatistics_Thresholds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Age              time.Duration
		ExpectedSeverity check.Severity
	}{
		{"well above threshold", 30 * day, check.SeverityPass},
		{"exactly 7 days", 7 * day, check.SeverityPass},
		{"just below 7 days", 6 * day, check.SeverityWarn},
		{"very fresh", 1 * day, check.SeverityWarn},
		{"reset an hour ago", time.Hour, check.SeverityWarn},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			result := runCheck(t, resetAgo(tc.Age))
			require.Equal(t, tc.ExpectedSeverity, result.Severity)
			require.Equal(t, "db-statistics", result.ID)
		})
	}
}

// The window belongs in the title because Details are not rendered on a PASS finding.
func Test_DBStatistics_TitleCarriesWindow(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name          string
		Row           db.DBStatisticsRow
		ExpectedTitle string
	}{
		{"mature", resetAgo(288 * day), "DB statistics: 288d since last reset"},
		{"immature", resetAgo(3 * day), "DB statistics: 3d since last reset"},
		{"never reset", neverReset(), "DB statistics: never reset"},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.ExpectedTitle, runCheck(t, tc.Row).Name)
		})
	}
}

// Regression: the age used to come from an integer day count, so anything under 24
// hours reported "0 days ago".
func Test_DBStatistics_SubDayWindowIsNotZeroDays(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name          string
		Age           time.Duration
		ExpectedTitle string
	}{
		{"minutes", 45 * time.Minute, "DB statistics: 45m since last reset"},
		{"hours", 6 * time.Hour, "DB statistics: 6h since last reset"},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			result := runCheck(t, resetAgo(tc.Age))
			require.Equal(t, tc.ExpectedTitle, result.Name)
			require.NotContains(t, result.Name, "0d")
		})
	}
}

// The truncated day count is still selected by the query but must not be trusted.
func Test_DBStatistics_IgnoresAgeDaysColumn(t *testing.T) {
	t.Parallel()

	row := resetAgo(30 * day)
	row.AgeDays = makeInt4(999)

	result := runCheck(t, row)
	require.Equal(t, "DB statistics: 30d since last reset", result.Name)
	require.Equal(t, check.SeverityPass, result.Severity)
}

// A NULL stats_reset is not evidence of a long window: a crash or a rebuilt replica
// also zeroes the counters without recording a timestamp.
func Test_DBStatistics_NeverResetMakesNoMaturityClaim(t *testing.T) {
	t.Parallel()

	result := runCheck(t, neverReset())

	require.Equal(t, check.SeverityPass, result.Severity)
	require.NotContains(t, result.Details, "optimal")
	require.Contains(t, result.Details, "unclean shutdown")
}

func Test_DBStatistics_ImmatureListsDependentChecks(t *testing.T) {
	t.Parallel()

	result := runCheck(t, resetAgo(3*day))

	require.Equal(t, check.SeverityWarn, result.Severity)
	for _, dependent := range []string{"index-usage", "table-seq-scans", "cache-efficiency", "temp-usage"} {
		require.Contains(t, result.Details, dependent)
	}
}

func Test_DBStatistics_QueryError(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryerWithError(fmt.Errorf("database connection error"))
	_, err := dbstatistics.New(queryer).Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "db-statistics", "error should mention the check ID")
}

func Test_DBStatistics_Metadata(t *testing.T) {
	t.Parallel()

	metadata := dbstatistics.New(newMockQueryer(db.DBStatisticsRow{})).Metadata()

	require.Equal(t, "db-statistics", metadata.CheckID)
	require.Equal(t, "DB Statistics", metadata.Name)
	require.Equal(t, check.CategoryConfigs, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}
