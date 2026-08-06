package querystatistics_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/querystatistics"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type mockQueryStatisticsQueryer struct {
	availability     db.QueryStatisticsAvailabilityRow
	availabilityErr  error
	window           db.QueryStatisticsWindowRow
	statsResetErr    error
	windowCalledFlag bool
}

func (m *mockQueryStatisticsQueryer) QueryStatisticsAvailability(context.Context) (db.QueryStatisticsAvailabilityRow, error) {
	if m.availabilityErr != nil {
		return db.QueryStatisticsAvailabilityRow{}, m.availabilityErr
	}

	return m.availability, nil
}

func (m *mockQueryStatisticsQueryer) QueryStatisticsWindow(context.Context) (db.QueryStatisticsWindowRow, error) {
	m.windowCalledFlag = true
	if m.statsResetErr != nil {
		return db.QueryStatisticsWindowRow{}, m.statsResetErr
	}

	return m.window, nil
}

func available(loaded, installed bool) *mockQueryStatisticsQueryer {
	return &mockQueryStatisticsQueryer{
		availability: db.QueryStatisticsAvailabilityRow{
			IsLoaded:    loaded,
			IsInstalled: installed,
			IsReachable: pgtype.Bool{Bool: loaded && installed, Valid: true},
		},
	}
}

func resetAgo(d time.Duration) *mockQueryStatisticsQueryer {
	m := available(true, true)
	m.window = db.QueryStatisticsWindowRow{
		StatsReset: pgtype.Timestamptz{Time: time.Now().Add(-d), Valid: true},
		AgeSeconds: pgtype.Int8{Int64: int64(d.Seconds()), Valid: true},
	}

	return m
}

func runCheck(t *testing.T, queryer *mockQueryStatisticsQueryer) check.Finding {
	t.Helper()

	report, err := querystatistics.New(queryer).Check(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Results, 1, "check reports exactly one finding")
	require.Equal(t, check.CategoryConfigs, report.Category)

	return report.Results[0]
}

// The window is in the title because renderers drop Details on a PASS finding, and
// this check passes in every state but one.
func Test_QueryStatistics_TitleCarriesWindow(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name          string
		Age           time.Duration
		ExpectedTitle string
	}{
		{"days", 5 * 24 * time.Hour, "Query statistics: 5d since last reset"},
		{"hours", 6 * time.Hour, "Query statistics: 6h since last reset"},
		{"minutes", 45 * time.Minute, "Query statistics: 45m since last reset"},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			result := runCheck(t, resetAgo(tc.Age))
			require.Equal(t, tc.ExpectedTitle, result.Name)
			require.Equal(t, check.SeverityPass, result.Severity)
		})
	}
}

// Engineers reset this clock on purpose while investigating, so a short window here
// is normal - unlike db-statistics, which warns below seven days.
func Test_QueryStatistics_ShortWindowNeverWarns(t *testing.T) {
	t.Parallel()

	for _, age := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour} {
		result := runCheck(t, resetAgo(age))
		require.Equal(t, check.SeverityPass, result.Severity, "age %s must not warn", age)
	}
}

func Test_QueryStatistics_Availability(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Loaded           bool
		Installed        bool
		Reachable        bool
		ExpectedTitle    string
		ExpectedSeverity check.Severity
	}{
		{
			Name:             "absent entirely",
			ExpectedTitle:    "Query statistics: pg_stat_statements not installed",
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "created but not preloaded",
			Installed:        true,
			ExpectedTitle:    "Query statistics: pg_stat_statements not loaded",
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "preloaded but not created here",
			Loaded:           true,
			ExpectedTitle:    "Query statistics: pg_stat_statements not created in this database",
			ExpectedSeverity: check.SeverityPass,
		},
		{
			Name:             "installed into a schema outside search_path",
			Loaded:           true,
			Installed:        true,
			Reachable:        false,
			ExpectedTitle:    "Query statistics: pg_stat_statements outside search_path",
			ExpectedSeverity: check.SeverityWarn,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := available(tc.Loaded, tc.Installed)
			queryer.availability.IsReachable = pgtype.Bool{Bool: tc.Reachable, Valid: true}
			result := runCheck(t, queryer)

			require.Equal(t, tc.ExpectedTitle, result.Name)
			require.Equal(t, tc.ExpectedSeverity, result.Severity)
			require.False(t, queryer.windowCalledFlag,
				"must not read pg_stat_statements_info when it would error")
		})
	}
}

func Test_QueryStatistics_NeverReset(t *testing.T) {
	t.Parallel()

	queryer := available(true, true)
	queryer.window = db.QueryStatisticsWindowRow{StatsReset: pgtype.Timestamptz{Valid: false}}

	result := runCheck(t, queryer)
	require.Equal(t, "Query statistics: never reset", result.Name)
	require.Equal(t, check.SeverityPass, result.Severity)
}

func Test_QueryStatistics_AvailabilityQueryError(t *testing.T) {
	t.Parallel()

	queryer := available(true, true)
	queryer.availabilityErr = fmt.Errorf("connection refused")

	_, err := querystatistics.New(queryer).Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "query-statistics", "error should mention the check ID")
}

func Test_QueryStatistics_WindowQueryError(t *testing.T) {
	t.Parallel()

	queryer := available(true, true)
	queryer.statsResetErr = fmt.Errorf("permission denied")

	_, err := querystatistics.New(queryer).Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "query-statistics", "error should mention the check ID")
}

func Test_QueryStatistics_Metadata(t *testing.T) {
	t.Parallel()

	metadata := querystatistics.New(available(true, true)).Metadata()

	require.Equal(t, "query-statistics", metadata.CheckID)
	require.Equal(t, "Query Statistics", metadata.Name)
	require.Equal(t, check.CategoryConfigs, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}
