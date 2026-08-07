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
	capacityID = "query-stats-capacity"
	rateID     = "statement-eviction-rate"

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

func Test_EntryCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		row        db.QueryStatsCapacityRow
		wantInName string
	}{
		{
			name:       "entries against max",
			row:        capacityRow(4200, 10000, 0, 30*day),
			wantInName: "4.2K/10.0K entries",
		},
		{
			name:       "at capacity",
			row:        capacityRow(10000, 10000, 0, 30*day),
			wantInName: "10.0K/10.0K entries",
		},
		{
			name: "max unreadable falls back to the bare count",
			row: func() db.QueryStatsCapacityRow {
				row := capacityRow(4200, 0, 0, 30*day)
				row.MaxEntries = pgtype.Int8{}
				return row
			}(),
			wantInName: "4.2K entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})
			result := finding(t, report, capacityID)

			assert.Equal(t, check.SeverityPass, result.Severity)
			assert.Contains(t, result.Name, tt.wantInName)
		})
	}
}

// A full table is a state, not a defect: a stable workload larger than max sits
// pinned at max without losing anything.
func Test_EntryCapacity_FullTableAloneDoesNotEscalate(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: true, row: capacityRow(10000, 10000, 0, 30*day)})

	assert.Equal(t, check.SeverityPass, report.Severity)
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
			row:          capacityRow(10000, 10000, 30, 30*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "<0.1x capacity/day",
		},
		{
			// 60 events over 30 days = 2/day = 0.1x capacity/day.
			name:         "measurable but tolerable churn",
			row:          capacityRow(10000, 10000, 60, 30*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "0.1x capacity/day",
		},
		{
			// 300 events over 30 days = 10/day = exactly the 0.5x threshold.
			name:         "at the threshold",
			row:          capacityRow(10000, 10000, 300, 30*day),
			wantSeverity: check.SeverityWarn,
			wantInName:   "0.5x capacity/day",
		},
		{
			// The measured production instance: 19688 events over 78 days.
			name:         "sustained eviction",
			row:          capacityRow(10000, 10000, 19688, 78*day),
			wantSeverity: check.SeverityWarn,
			wantInName:   "12.6x capacity/day",
		},
		{
			// A counter that only grows: 4000 events over three years is 3.7/day,
			// 0.2x capacity. Thresholding on dealloc itself would flag this.
			name:         "large absolute count over a long window",
			row:          capacityRow(10000, 10000, 4000, 1095*day),
			wantSeverity: check.SeverityPass,
			wantInName:   "0.2x capacity/day",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})
			result := finding(t, report, rateID)

			assert.Equal(t, tt.wantSeverity, result.Severity)
			assert.Contains(t, result.Name, tt.wantInName)
			assert.Equal(t, tt.wantSeverity, report.Severity)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := run(t, &mockQueryer{pgssOK: true, row: tt.row})

			assert.Equal(t, check.SeveritySkip, finding(t, report, rateID).Severity)
			// The capacity finding needs no window, so the report still has a result.
			assert.Equal(t, check.SeverityPass, report.Severity)
			assert.Equal(t, check.SeverityPass, finding(t, report, capacityID).Severity)
		})
	}
}

// Nothing reads pg_stat_statements when it is absent, so no sample is truncated.
// One PASS line, no explanatory paragraph.
func Test_ExtensionUnavailable(t *testing.T) {
	t.Parallel()

	report := run(t, &mockQueryer{pgssOK: false})

	assert.Equal(t, check.SeverityPass, report.Severity)
	require.Len(t, report.Results, 1)
	assert.Equal(t, capacityID, report.Results[0].ID)
	assert.NotContains(t, report.Results[0].Details, "\n")
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
