package querystatscapacity_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/querystatscapacity"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	usageID = "entry-usage"
	rateID  = "statement-eviction-rate"

	day = 86400.0
)

type mockQueryer struct {
	row      db.QueryStatsCapacityRow
	pgssOK   bool
	pgssErr  error
	queryErr error
}

func (m *mockQueryer) HasPgStatStatements(context.Context) (pgtype.Bool, error) {
	return pgtype.Bool{Bool: m.pgssOK, Valid: true}, m.pgssErr
}

func (m *mockQueryer) QueryStatsCapacity(context.Context) (db.QueryStatsCapacityRow, error) {
	return m.row, m.queryErr
}

func pgInt8(i int64) pgtype.Int8 { return pgtype.Int8{Int64: i, Valid: true} }

// capacityRow builds a row with a valid stats_reset placed windowSeconds ago.
func capacityRow(entries, maxEntries, events int64, windowSeconds float64) db.QueryStatsCapacityRow {
	return db.QueryStatsCapacityRow{
		Entries:           pgInt8(entries),
		MaxEntries:        pgInt8(maxEntries),
		EvictionEvents:    pgInt8(events),
		StatsReset:        pgtype.Timestamptz{Time: time.Now().Add(-time.Duration(windowSeconds) * time.Second), Valid: true},
		SecondsSinceReset: pgtype.Float8{Float64: windowSeconds, Valid: true},
	}
}

func run(t *testing.T, queryer *mockQueryer) *check.Report {
	t.Helper()

	report, err := querystatscapacity.New(queryer).Check(context.Background())
	require.NoError(t, err)

	return report
}

func finding(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()

	for _, result := range report.Results {
		if result.ID == id {
			return result
		}
	}

	t.Fatalf("finding %q not found", id)

	return check.Finding{}
}

func Test_EntryUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		row          db.QueryStatsCapacityRow
		wantInName   string
		wantSeverity check.Severity
	}{
		{
			name:         "entries against max",
			row:          capacityRow(4200, 10000, 0, 30*day),
			wantInName:   "4.2K/10.0K entries",
			wantSeverity: check.SeverityPass,
		},
		{
			name:         "at capacity",
			row:          capacityRow(10000, 10000, 0, 30*day),
			wantInName:   "10.0K/10.0K entries",
			wantSeverity: check.SeverityPass,
		},
		{
			// Without max there is nothing for "full" to be relative to, so the
			// present-tense signal is unavailable rather than healthy.
			name: "max unreadable cannot be graded",
			row: func() db.QueryStatsCapacityRow {
				row := capacityRow(4200, 0, 0, 30*day)
				row.MaxEntries = pgtype.Int8{}
				return row
			}(),
			wantInName:   "4.2K entries, capacity unreadable",
			wantSeverity: check.SeveritySkip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})
			result := finding(t, report, usageID)

			assert.Equal(t, tt.wantSeverity, result.Severity)
			assert.Contains(t, result.Name, tt.wantInName)
		})
	}
}

// The renderer prints the report header and then every finding, so a finding
// named after the check prints the check name twice.
func Test_EntryUsage_DoesNotRepeatTheCheckName(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(9, 5000, 0, 30*day)})

	for _, result := range report.Results {
		assert.NotEqual(t, report.CheckID, result.ID)
		assert.NotContains(t, result.Name, report.Name)
	}

	assert.Equal(t, "Entry Usage: 9/5.0K entries", finding(t, report, usageID).Name)
}

// A full table is a state, not a defect: a stable workload larger than max sits
// pinned at max without losing anything.
// Occupancy alone is not a defect and must not warn: a stable workload larger than
// max sits pinned there indefinitely without losing anything, and just short of max
// there is still headroom.
func Test_EntryCapacity_OccupancyAloneDoesNotWarn(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		row  db.QueryStatsCapacityRow
	}{
		{"full but nothing ever evicted", capacityRow(10000, 10000, 0, 30*day)},
		{"near capacity with headroom left", capacityRow(9900, 10000, 0, 30*day)},
		{"full with old evictions on record", capacityRow(10000, 10000, 300, 3000*day)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})
			assert.Equal(t, check.SeverityPass, finding(t, report, usageID).Severity)
		})
	}
}

// A snapshot cannot tell recent churn from historical: dealloc is cumulative, so a
// table that filled, churned, then stabilised keeps both a full table and a nonzero
// count forever. Grading either would warn permanently on a healthy instance.
func Test_EntryCapacity_HistoricalEvictionOnAFullTableDoesNotWarn(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, 300, 3000*day)})

	assert.Equal(t, check.SeverityPass, finding(t, report, usageID).Severity)
	assert.Equal(t, check.SeverityPass, finding(t, report, rateID).Severity)
	assert.Equal(t, check.SeverityPass, report.Severity)
}

func Test_EntryCapacity_BelowCapacityPasses(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(4200, 10000, 0, 30*day)})

	assert.Equal(t, check.SeverityPass, finding(t, report, usageID).Severity)
}

func Test_EvictionRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		row          db.QueryStatsCapacityRow
		wantSeverity check.Severity
		wantInName   string
	}{
		{
			name:         "no evictions",
			row:          capacityRow(4200, 10000, 0, 30*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "no evictions",
		},
		{
			// 30 events over 30 days = 1/day = 0.05x capacity/day.
			name:         "occasional churn stays below the display floor",
			row:          capacityRow(4200, 10000, 30, 30*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "<0.1x capacity/day",
		},
		{
			// 60 events over 30 days = 2/day = 0.1x capacity/day.
			name:         "measurable but tolerable churn",
			row:          capacityRow(4200, 10000, 60, 30*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "0.1x capacity/day",
		},
		{
			// 300 events over 30 days = 10/day = exactly the 0.5x threshold.
			name:         "at the threshold",
			row:          capacityRow(4200, 10000, 300, 30*day),
			wantSeverity: check.SeverityWarn,
			wantInName:   "0.5x capacity/day",
		},
		{
			// The measured production instance: 19688 events over 78 days.
			name:         "sustained eviction",
			row:          capacityRow(4200, 10000, 19688, 78*day),
			wantSeverity: check.SeverityWarn,
			wantInName:   "12.6x capacity/day",
		},
		{
			// A counter that only grows: 4000 events over three years is 3.7/day,
			// 0.18x capacity. Thresholding on dealloc itself would flag this.
			name:         "large absolute count over a long window",
			row:          capacityRow(4200, 10000, 4000, 1095*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "0.1x capacity/day",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})
			result := finding(t, report, rateID)

			assert.Equal(t, tt.wantSeverity, result.Severity)
			assert.Contains(t, result.Name, tt.wantInName)
			// Entries are held below capacity so the fill signal stays PASS and the
			// report severity reflects the rate alone.
			assert.Equal(t, tt.wantSeverity, report.Severity)
		})
	}
}

// entry_dealloc() discards Max(10, max * 5 / 100) entries per event. Below
// max = 200 the floor of 10 dominates, and max bottoms out at 100 - so a fixed
// 5% halves the reported turnover and passes a saturated instance.
//
// 60 events over 10 days at max = 100 is 6 events/day. The real batch is 10
// entries, so 60 entries/day against a capacity of 100 is 0.6x - a WARN. Under
// a fixed 5% the batch would be 5, giving 0.3x and a PASS.
func Test_EvictionRate_SmallMaxUsesTheTenEntryBatchFloor(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(100, 100, 60, 10*day)})
	result := finding(t, report, rateID)

	assert.Equal(t, check.SeverityWarn, result.Severity)
	assert.Contains(t, result.Name, "0.6x capacity/day")
	// 60 events * 10 entries, not 60 * 5.
	assert.Contains(t, result.Details, "600 entries")
}

// The batch is a true 5% once max clears 200, where 5% of max exceeds the floor.
func Test_EvictionRate_LargeMaxUsesThePercentBatch(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, 60, 10*day)})
	result := finding(t, report, rateID)

	// 6 events/day * 500 entries = 3000/day against 10000 = 0.3x.
	assert.Equal(t, check.SeverityPass, result.Severity)
	assert.Contains(t, result.Name, "0.3x capacity/day")
}

// The displayed value is truncated, not rounded: rounding would print "0.5x
// capacity/day" on a finding that passed at a 0.5 threshold.
func Test_EvictionRate_DisplayNeverOverstatesTheThreshold(t *testing.T) {
	t.Parallel()

	// 996 events over 100 days = 9.96/day * 500 entries = 4980/day against
	// 10000 = 0.498x, just under the threshold.
	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, 996, 100*day)})
	result := finding(t, report, rateID)

	assert.Equal(t, check.SeverityPass, result.Severity)
	assert.Contains(t, result.Name, "0.4x capacity/day")
	assert.NotContains(t, result.Name, "0.5")
	assert.Empty(t, result.Details)
}

// Truncation must not swallow a tenth that only looks short in binary:
// 0.7*10 is 6.999999999999999 in float64.
func Test_EvictionRate_DisplayIsExactAtTenths(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		events int64
		want   string
	}{
		{events: 140, want: "0.7x capacity/day"}, // 14/day * 500 / 10000 = 0.7
		{events: 580, want: "2.9x capacity/day"}, // 58/day * 500 / 10000 = 2.9
		{events: 660, want: "3.3x capacity/day"}, // 66/day * 500 / 10000 = 3.3
	} {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, tt.events, 10*day)})

			assert.Contains(t, finding(t, report, rateID).Name, tt.want)
		})
	}
}

// Eviction degrades observability, not the database, and the fix needs a restart.
func Test_EvictionRate_NeverFails(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, 1_000_000, 30*day)})

	assert.Equal(t, check.SeverityWarn, report.Severity)
}

func Test_EvictionRate_Details(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, 19688, 78*day)})
	result := finding(t, report, rateID)

	assert.Contains(t, result.Details, "19.7K eviction events")
	// 19688 * 0.05 * 10000 = 9,844,000 entries discarded.
	assert.Contains(t, result.Details, "9.8M entries")
	assert.Contains(t, result.Details, "capacity of 10.0K")
	assert.Contains(t, result.Details, "partition-usage")
	assert.LessOrEqual(t, strings.Count(result.Details, "\n"), 1, "details must stay short")
}

func Test_EvictionRate_NoDetailsWhenPassing(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(4200, 10000, 0, 30*day)})

	assert.Empty(t, finding(t, report, rateID).Details)
}

func Test_EvictionRate_UnusableWindowSkips(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		row  db.QueryStatsCapacityRow
	}{
		{
			name: "stats_reset is null",
			row: func() db.QueryStatsCapacityRow {
				row := capacityRow(4200, 10000, 12, 30*day)
				row.StatsReset = pgtype.Timestamptz{}
				row.SecondsSinceReset = pgtype.Float8{}
				return row
			}(),
		},
		{
			name: "window under an hour",
			row:  capacityRow(4200, 10000, 12, 600),
		},
		{
			// The batch size and the capacity the turnover is a share of both
			// derive from max, so an unreadable one leaves no rate to report.
			name: "max unreadable",
			row: func() db.QueryStatsCapacityRow {
				row := capacityRow(4200, 0, 12, 30*day)
				row.MaxEntries = pgtype.Int8{}
				return row
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})

			assert.Equal(t, check.SeveritySkip, finding(t, report, rateID).Severity)

			if tt.name == "max unreadable" {
				// max is what both findings measure against, so neither can grade
				// and the report follows them down.
				assert.Equal(t, check.SeveritySkip, finding(t, report, usageID).Severity)
				assert.Equal(t, check.SeveritySkip, report.Severity)

				return
			}

			// Entry usage needs neither the window nor max, so it still reports.
			assert.Equal(t, check.SeverityPass, finding(t, report, usageID).Severity)
			assert.Equal(t, check.SeverityPass, report.Severity)
		})
	}
}

// Nothing reads pg_stat_statements when it is absent, so no sample is truncated.
// One PASS line, no explanatory paragraph.
func Test_ExtensionUnavailable(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: false})

	// Nothing was inspected. pg_stat_statements is cluster-wide, so its absence from
	// this database says nothing about whether the shared hash is evicting, and PASS
	// would claim a capacity the check never measured.
	assert.Equal(t, check.SeveritySkip, report.Severity)
	require.Len(t, report.Results, 1)
	assert.Equal(t, usageID, report.Results[0].ID)
	assert.Equal(t, check.SeveritySkip, report.Results[0].Severity)
	// A skipped report renders the Details, not the finding name.
	assert.Contains(t, report.Results[0].Details, "not available")
}

func Test_QueryError(t *testing.T) {
	t.Parallel()

	_, err := querystatscapacity.
		New(&mockQueryer{pgssOK: true, queryErr: fmt.Errorf("connection refused")}).
		Check(context.Background())

	require.ErrorContains(t, err, "query-stats-capacity")
}

func Test_AvailabilityQueryError(t *testing.T) {
	t.Parallel()

	_, err := querystatscapacity.
		New(&mockQueryer{pgssErr: fmt.Errorf("connection refused")}).
		Check(context.Background())

	require.ErrorContains(t, err, "query-stats-capacity")
}

func Test_Metadata(t *testing.T) {
	t.Parallel()

	m := querystatscapacity.Metadata()

	assert.Equal(t, "query-stats-capacity", m.CheckID)
	assert.Equal(t, check.CategoryConfigs, m.Category)
	assert.NotEmpty(t, m.Name)
	assert.NotEmpty(t, m.Description)
	assert.NotEmpty(t, m.SQL)
	assert.NotEmpty(t, m.Readme)
}
