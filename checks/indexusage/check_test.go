package indexusage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/indexusage"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type mockQueryer struct {
	rows []db.IndexUsageStatsRow
	err  error
}

func (m *mockQueryer) IndexUsageStats(context.Context) ([]db.IndexUsageStatsRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func pgText(s string) pgtype.Text         { return pgtype.Text{String: s, Valid: true} }
func pgInt8(i int64) pgtype.Int8          { return pgtype.Int8{Int64: i, Valid: true} }
func pgTS(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func mb(n int64) int64 { return n * 1024 * 1024 }

const secondsPerDay = 24 * 60 * 60

// window is what the query reports about the counters: the reset timestamp, shown
// as a date, and the age the server measured, which the rate threshold divides by.
type window struct {
	reset pgtype.Timestamptz
	age   pgtype.Int8
}

// daysAgo is a consistent window: the timestamp and the server-measured age agree.
// Exact-duration offset (not AddDate) so window boundaries don't drift across DST.
func daysAgo(n int) window {
	return window{
		reset: pgTS(time.Now().Add(-time.Duration(n) * 24 * time.Hour)),
		age:   pgInt8(int64(n) * secondsPerDay),
	}
}

// noReset is a database whose counters were never reset: no timestamp, no age.
func noReset() window { return window{} }

func row(table, index string, scans, writes, sizeBytes int64, w window) db.IndexUsageStatsRow {
	return db.IndexUsageStatsRow{
		TableName:       pgText(table),
		IndexName:       pgText(index),
		IdxScan:         pgInt8(scans),
		TableWrites:     pgInt8(writes),
		IndexSizeBytes:  pgInt8(sizeBytes),
		StatsReset:      w.reset,
		StatsAgeSeconds: w.age,
	}
}

func runCheck(t *testing.T, rows []db.IndexUsageStatsRow) *check.Report {
	t.Helper()

	report, err := indexusage.New(&mockQueryer{rows: rows}).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

func finding(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()

	for _, f := range report.Results {
		if f.ID == id {
			return f
		}
	}
	require.Failf(t, "missing finding", "no finding with id %q", id)
	return check.Finding{}
}

func Test_Metadata(t *testing.T) {
	t.Parallel()

	m := indexusage.Metadata()
	require.Equal(t, "index-usage", m.CheckID)
	require.Equal(t, "Index Usage", m.Name)
	require.Equal(t, check.CategoryIndexes, m.Category)
	require.NotEmpty(t, m.Description)
	require.NotEmpty(t, m.SQL)
	require.NotEmpty(t, m.Readme)
}

func Test_NoIndexes_Pass(t *testing.T) {
	t.Parallel()

	report := runCheck(t, nil)
	require.Len(t, report.Results, 1)
	require.Equal(t, check.SeverityPass, report.Severity)
	require.Equal(t, "index-usage", report.Results[0].ID)
}

func Test_QueryError(t *testing.T) {
	t.Parallel()

	q := &mockQueryer{err: fmt.Errorf("connection refused")}
	_, err := indexusage.New(q).Check(context.Background())
	require.ErrorContains(t, err, "index-usage")
}

func Test_UnusedIndexes_SizeFloor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		size   int64
		listed bool
	}{
		{"just below floor (499MB) - not listed", mb(499), false},
		{"at floor (500MB) - listed", mb(500), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := row("public.users", "idx_users_unused", 0, 50000, tt.size, daysAgo(90))
			report := runCheck(t, []db.IndexUsageStatsRow{r})
			unused := finding(t, report, "unused-indexes")

			if !tt.listed {
				require.Equal(t, check.SeverityPass, unused.Severity)
				require.Nil(t, unused.Table)
				return
			}

			require.Equal(t, check.SeverityWarn, unused.Severity)
			require.Equal(t, []string{"Table", "Index", "Size"}, unused.Table.Headers)
			require.Len(t, unused.Table.Rows, 1)
			require.Equal(t, []string{"public.users", "idx_users_unused", "500.0MiB"}, unused.Table.Rows[0].Cells)
			require.Equal(t, check.SeverityWarn, unused.Table.Rows[0].Severity)
		})
	}
}

func Test_UnusedIndexes_StatsWindowInDetails(t *testing.T) {
	t.Parallel()

	reset := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	w := window{reset: pgTS(reset), age: pgInt8(90 * secondsPerDay)}
	r := row("public.users", "idx_users_unused", 0, 50000, mb(600), w)

	report := runCheck(t, []db.IndexUsageStatsRow{r})
	unused := finding(t, report, "unused-indexes")
	require.Contains(t, unused.Details, "0 scans since 2026-06-01")
	require.Contains(t, unused.Details, ">500MB")
}

func Test_UnusedIndexes_NullStatsReset_OmitsDate(t *testing.T) {
	t.Parallel()

	r := row("public.users", "idx_users_unused", 0, 50000, mb(600), noReset())

	report := runCheck(t, []db.IndexUsageStatsRow{r})
	unused := finding(t, report, "unused-indexes")
	require.Equal(t, check.SeverityWarn, unused.Severity)
	require.Contains(t, unused.Details, "(0 scans, >500MB)")
	require.NotContains(t, unused.Details, "since")
}

func Test_UnusedIndexes_SortedBySizeDesc(t *testing.T) {
	t.Parallel()

	// Rows arrive largest-first (SQL ORDER BY pg_relation_size DESC); Go preserves order.
	big := row("public.a", "idx_big", 0, 50000, mb(900), daysAgo(90))
	small := row("public.b", "idx_small", 0, 50000, mb(600), daysAgo(90))

	report := runCheck(t, []db.IndexUsageStatsRow{big, small})
	unused := finding(t, report, "unused-indexes")
	require.Len(t, unused.Table.Rows, 2)
	require.Equal(t, "idx_big", unused.Table.Rows[0].Cells[1])
	require.Equal(t, "idx_small", unused.Table.Rows[1].Cells[1])
}

func Test_UnusedIndexes_SkipPrimaryAndUnique(t *testing.T) {
	t.Parallel()

	pk := row("public.users", "users_pkey", 0, 50000, mb(600), daysAgo(90))
	pk.IsPrimary = true
	uniq := row("public.users", "idx_users_email", 0, 50000, mb(600), daysAgo(90))
	uniq.IsUnique = true

	report := runCheck(t, []db.IndexUsageStatsRow{pk, uniq})
	require.Equal(t, check.SeverityPass, finding(t, report, "unused-indexes").Severity)
}

func Test_LowUsageIndexes_Boundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scans  int64
		writes int64
		size   int64
		reset  window
		listed bool
	}{
		{"window 29d - not listed", 0, 50000, mb(600), daysAgo(29), false},
		{"window 30d - listed", 1, 50000, mb(600), daysAgo(30), true},
		{"zero scans - not listed (belongs to unused)", 0, 50000, mb(600), daysAgo(90), false},
		{"rate under 1/week (9 scans, 70d) - listed", 9, 50000, mb(600), daysAgo(70), true},
		{"rate at 1/week (10 scans, 70d) - not listed", 10, 50000, mb(600), daysAgo(70), false},
		{"writes below floor (9999) - not listed", 5, 9999, mb(600), daysAgo(90), false},
		{"writes at floor (10000) - listed", 5, 10000, mb(600), daysAgo(90), true},
		{"size below floor (499MB) - not listed", 5, 50000, mb(499), daysAgo(90), false},
		{"size at floor (500MB) - listed", 5, 50000, mb(500), daysAgo(90), true},
		{"null stats_reset qualifies window and rate", 5, 50000, mb(600), noReset(), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := row("public.posts", "idx_posts_status", tt.scans, tt.writes, tt.size, tt.reset)
			report := runCheck(t, []db.IndexUsageStatsRow{r})
			low := finding(t, report, "low-usage-indexes")

			if tt.listed {
				require.Equal(t, check.SeverityInfo, low.Severity)
				require.Len(t, low.Table.Rows, 1)
			} else {
				require.Equal(t, check.SeverityPass, low.Severity)
				require.Nil(t, low.Table)
			}
		})
	}
}

// The window comes from the server, not from differencing stats_reset against this
// host's clock. Both cases below hand the check a timestamp that implies the
// opposite verdict from the server-measured age; the server has to win, otherwise a
// CLI host whose clock is off decides which indexes get reported.
func Test_LowUsageIndexes_WindowComesFromServerNotHostClock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		w      window
		listed bool
	}{
		{
			// Host clock 2 days behind the server: the timestamp implies a 2-day
			// window (below the 30-day gate), the server says 90 days.
			name:   "host clock behind: server window opens the gate",
			w:      window{reset: pgTS(time.Now().Add(-2 * 24 * time.Hour)), age: pgInt8(90 * secondsPerDay)},
			listed: true,
		},
		{
			// Host clock 90 days ahead: the timestamp implies a 90-day window, the
			// server says 2 days, which is too short to judge a read rate.
			name:   "host clock ahead: server window keeps the gate shut",
			w:      window{reset: pgTS(time.Now().Add(-90 * 24 * time.Hour)), age: pgInt8(2 * secondsPerDay)},
			listed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// 1 scan over 90 days is under 1/week; over 2 days it is not judged at all.
			r := row("public.posts", "idx_posts_status", 1, 50000, mb(600), tt.w)
			low := finding(t, runCheck(t, []db.IndexUsageStatsRow{r}), "low-usage-indexes")

			if tt.listed {
				require.Equal(t, check.SeverityInfo, low.Severity)
				require.Len(t, low.Table.Rows, 1)
				return
			}

			require.Equal(t, check.SeverityPass, low.Severity)
			require.Nil(t, low.Table)
		})
	}
}

func Test_ZeroScan_UnusedNotLowUsage(t *testing.T) {
	t.Parallel()

	// Clears both size floors and the low-usage window, but has zero scans.
	r := row("public.posts", "idx_posts_dead", 0, 50000, mb(600), daysAgo(90))

	report := runCheck(t, []db.IndexUsageStatsRow{r})

	unused := finding(t, report, "unused-indexes")
	require.Equal(t, check.SeverityWarn, unused.Severity)
	require.Len(t, unused.Table.Rows, 1)

	low := finding(t, report, "low-usage-indexes")
	require.Equal(t, check.SeverityPass, low.Severity)
	require.Nil(t, low.Table)
}

func Test_LowUsageIndexes_SkipPrimaryAndUnique(t *testing.T) {
	t.Parallel()

	pk := row("public.users", "users_pkey", 5, 50000, mb(600), daysAgo(90))
	pk.IsPrimary = true

	report := runCheck(t, []db.IndexUsageStatsRow{pk})
	require.Equal(t, check.SeverityPass, finding(t, report, "low-usage-indexes").Severity)
}

func Test_LowUsageIndexes_Columns(t *testing.T) {
	t.Parallel()

	r := row("public.posts", "idx_recent", 5, 50000, mb(600), daysAgo(90))

	report := runCheck(t, []db.IndexUsageStatsRow{r})

	low := finding(t, report, "low-usage-indexes")
	require.Equal(t, []string{"Table", "Index", "Size", "Scans", "Writes"}, low.Table.Headers)
	require.Len(t, low.Table.Rows[0].Cells, 5)
}

func Test_MixedFindings_ReportWarns(t *testing.T) {
	t.Parallel()

	unused := row("public.users", "idx_unused", 0, 50000, mb(600), daysAgo(90))
	lowUsage := row("public.orders", "idx_orders", 5, 50000, mb(600), daysAgo(90))

	report := runCheck(t, []db.IndexUsageStatsRow{unused, lowUsage})
	require.Len(t, report.Results, 2)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, check.SeverityWarn, finding(t, report, "unused-indexes").Severity)
	require.Equal(t, check.SeverityInfo, finding(t, report, "low-usage-indexes").Severity)
}
