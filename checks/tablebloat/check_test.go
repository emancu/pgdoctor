package tablebloat_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tablebloat"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockQueryer struct {
	rows []db.TableBloatRow
	err  error
}

func (m *mockQueryer) TableBloat(ctx context.Context) ([]db.TableBloatRow, error) {
	return m.rows, m.err
}

func makeTableRow(
	tableName string,
	liveTuples, deadTuples int64,
	deadTuplePct float64,
	totalSize int64,
	lastAutovacuum, lastVacuum *time.Time,
	autovacuumCount int64,
) db.TableBloatRow {
	var deadPct pgtype.Numeric
	_ = deadPct.Scan(fmt.Sprintf("%.2f", deadTuplePct))

	row := db.TableBloatRow{
		TableName:        pgtype.Text{String: tableName, Valid: true},
		LiveTuples:       pgtype.Int8{Int64: liveTuples, Valid: true},
		DeadTuples:       pgtype.Int8{Int64: deadTuples, Valid: true},
		DeadTuplePercent: deadPct,
		TotalSizeBytes:   pgtype.Int8{Int64: totalSize, Valid: true},
		AutovacuumCount:  pgtype.Int8{Int64: autovacuumCount, Valid: true},
		VacuumCount:      pgtype.Int8{Int64: 0, Valid: true},
	}

	if lastAutovacuum != nil {
		row.LastAutovacuum = pgtype.Timestamptz{Time: *lastAutovacuum, Valid: true}
	}
	if lastVacuum != nil {
		row.LastVacuum = pgtype.Timestamptz{Time: *lastVacuum, Valid: true}
	}

	return row
}

func TestTableBloat_AllHealthy(t *testing.T) {
	t.Parallel()

	recentVacuum := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.users", 100000, 5000, 5.0, 100*1024*1024, &recentVacuum, nil, 10),
			makeTableRow("public.orders", 50000, 2000, 4.0, 50*1024*1024, &recentVacuum, nil, 8),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Len(t, report.Results, 3)
	assert.Equal(t, "high-dead-tuples", report.Results[0].ID)
	assert.Equal(t, "stale-vacuum", report.Results[1].ID)
	assert.Equal(t, "large-bloated-tables", report.Results[2].ID)
	assert.Equal(t, check.SeverityPass, report.Results[0].Severity)
	assert.Equal(t, check.SeverityPass, report.Results[1].Severity)
	assert.Equal(t, check.SeverityPass, report.Results[2].Severity)
}

func TestTableBloat_HighDeadTuples_Warning(t *testing.T) {
	t.Parallel()

	recentVacuum := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.events", 100000, 25000, 25.0, 100*1024*1024, &recentVacuum, nil, 5),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	highDeadFinding := report.Results[0]
	assert.Equal(t, "high-dead-tuples", highDeadFinding.ID)
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Severity)
	assert.Contains(t, highDeadFinding.Details, "1 table(s)")
	assert.NotNil(t, highDeadFinding.Table)
	assert.Len(t, highDeadFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Table.Rows[0].Severity)
}

func TestTableBloat_HighDeadTuples_Critical(t *testing.T) {
	t.Parallel()

	recentVacuum := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.events", 100000, 80000, 45.0, 500*1024*1024, &recentVacuum, nil, 3),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	highDeadFinding := report.Results[0]
	assert.Equal(t, check.SeverityFail, highDeadFinding.Severity)
	require.NotNil(t, highDeadFinding.Table)
	require.Len(t, highDeadFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityFail, highDeadFinding.Table.Rows[0].Severity)
}

func TestTableBloat_StaleVacuum_NeverVacuumed(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.logs", 1000000, 60000, 6.0, 200*1024*1024, nil, nil, 0),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityPass, report.Severity)

	staleVacuumFinding := report.Results[1]
	assert.Equal(t, "stale-vacuum", staleVacuumFinding.ID)
	assert.Equal(t, check.SeverityPass, staleVacuumFinding.Severity)
}

func TestTableBloat_StaleVacuum_SevenDaysOld(t *testing.T) {
	t.Parallel()

	oldVacuum := time.Now().AddDate(0, 0, -8)
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.audit", 500000, 75000, 15.0, 300*1024*1024, &oldVacuum, nil, 5),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	staleVacuumFinding := report.Results[1]
	assert.Equal(t, check.SeverityWarn, staleVacuumFinding.Severity)
}

func TestTableBloat_StaleVacuum_ThreeDaysOld(t *testing.T) {
	t.Parallel()

	oldVacuum := time.Now().AddDate(0, 0, -4)
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.sessions", 1000000, 120000, 12.0, 400*1024*1024, &oldVacuum, nil, 8),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	staleVacuumFinding := report.Results[1]
	assert.Equal(t, "stale-vacuum", staleVacuumFinding.ID)
	assert.Equal(t, check.SeverityWarn, staleVacuumFinding.Severity)
}

func TestTableBloat_LargeBloated_Warning(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	recentVacuum := time.Now().Add(-1 * time.Hour)

	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.bookings", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 20),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	largeBloatFinding := report.Results[2]
	assert.Equal(t, "large-bloated-tables", largeBloatFinding.ID)
	assert.Equal(t, check.SeverityWarn, largeBloatFinding.Severity)
	assert.Contains(t, largeBloatFinding.Details, "Found 1 large table(s)")
}

func TestTableBloat_LargeBloated_Critical(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	recentVacuum := time.Now().Add(-1 * time.Hour)

	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.events", 50000000, 15000000, 25.0, 12*oneGB, &recentVacuum, nil, 15),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	largeBloatFinding := report.Results[2]
	assert.Equal(t, check.SeverityFail, largeBloatFinding.Severity)
	require.NotNil(t, largeBloatFinding.Table)
	require.Len(t, largeBloatFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityFail, largeBloatFinding.Table.Rows[0].Severity)
}

func TestTableBloat_MixedSeverity(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	recentVacuum := time.Now().Add(-1 * time.Hour)
	oldVacuum := time.Now().AddDate(0, 0, -10)

	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			// High dead tuples - critical
			makeTableRow("public.t1", 100000, 80000, 45.0, 200*1024*1024, &recentVacuum, nil, 5),
			// High dead tuples - warning
			makeTableRow("public.t2", 100000, 25000, 25.0, 150*1024*1024, &recentVacuum, nil, 3),
			// Stale vacuum - warning (60K dead at 12% is below the 400K critical floor)
			makeTableRow("public.t3", 500000, 60000, 12.0, 300*1024*1024, &oldVacuum, nil, 2),
			// Large bloated - critical
			makeTableRow("public.t4", 100000000, 30000000, 30.0, 15*oneGB, &recentVacuum, nil, 10),
			// Large bloated - warning
			makeTableRow("public.t5", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 8),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Len(t, report.Results, 3)

	assert.Equal(t, check.SeverityFail, report.Results[0].Severity, "high-dead-tuples severity")
	assert.Equal(t, check.SeverityWarn, report.Results[1].Severity, "stale-vacuum severity")
	assert.Equal(t, check.SeverityFail, report.Results[2].Severity, "large-bloated-tables severity")

	for _, finding := range report.Results {
		assert.NotNil(t, finding.Table)
	}
}

func TestTableBloat_EdgeCases_ExactThresholds(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	const tenGB = 10 * oneGB
	recentVacuum := time.Now().Add(-1 * time.Hour)
	twoDaysAgo := time.Now().AddDate(0, 0, -2)
	fourDaysAgo := time.Now().AddDate(0, 0, -4)
	thirteenDaysAgo := time.Now().AddDate(0, 0, -13)

	tests := []struct {
		name                     string
		row                      db.TableBloatRow
		expectedHighDeadSeverity check.Severity
		expectedStaleSeverity    check.Severity
		expectedLargeSeverity    check.Severity
	}{
		{
			name:                     "exactly 20% dead - warning threshold",
			row:                      makeTableRow("public.t1", 80000, 20000, 20.0, 100*1024*1024, &recentVacuum, nil, 5),
			expectedHighDeadSeverity: check.SeverityWarn,
			expectedStaleSeverity:    check.SeverityPass,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "exactly 40% dead - critical threshold",
			row:                      makeTableRow("public.t2", 60000, 40000, 40.0, 150*1024*1024, &recentVacuum, nil, 3),
			expectedHighDeadSeverity: check.SeverityFail,
			expectedStaleSeverity:    check.SeverityPass,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "exactly 100K dead + 4 days stale - warning",
			row:                      makeTableRow("public.t3", 4900000, 100000, 2.0, 200*1024*1024, &fourDaysAgo, nil, 5),
			expectedHighDeadSeverity: check.SeverityPass,
			expectedStaleSeverity:    check.SeverityWarn,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "100K dead + 2 days stale - age gate not met",
			row:                      makeTableRow("public.t4", 4900000, 100000, 2.0, 250*1024*1024, &twoDaysAgo, nil, 2),
			expectedHighDeadSeverity: check.SeverityPass,
			expectedStaleSeverity:    check.SeverityPass,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "exactly 400K dead at 10% + 13 days stale - critical",
			row:                      makeTableRow("public.t7", 3600000, 400000, 10.0, 500*1024*1024, &thirteenDaysAgo, nil, 1),
			expectedHighDeadSeverity: check.SeverityPass,
			expectedStaleSeverity:    check.SeverityFail,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "exactly 1GB + 10% dead - warning",
			row:                      makeTableRow("public.t5", 9000000, 1000000, 10.0, oneGB, &recentVacuum, nil, 10),
			expectedHighDeadSeverity: check.SeverityPass,
			expectedStaleSeverity:    check.SeverityPass,
			expectedLargeSeverity:    check.SeverityWarn,
		},
		{
			name:                     "exactly 10GB + 20% dead - critical",
			row:                      makeTableRow("public.t6", 40000000, 10000000, 20.0, tenGB, &recentVacuum, nil, 15),
			expectedHighDeadSeverity: check.SeverityWarn, // 20% triggers high-dead too
			expectedStaleSeverity:    check.SeverityPass,
			expectedLargeSeverity:    check.SeverityFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: []db.TableBloatRow{tt.row}}
			checker := tablebloat.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.expectedHighDeadSeverity, report.Results[0].Severity, "high-dead-tuples severity")
			assert.Equal(t, tt.expectedStaleSeverity, report.Results[1].Severity, "stale-vacuum severity")
			assert.Equal(t, tt.expectedLargeSeverity, report.Results[2].Severity, "large-bloated-tables severity")
		})
	}
}

func TestTableBloat_StaleVacuum_Thresholds(t *testing.T) {
	t.Parallel()

	twoDaysAgo := time.Now().AddDate(0, 0, -2)
	fourDaysAgo := time.Now().AddDate(0, 0, -4)
	tenDaysAgo := time.Now().AddDate(0, 0, -10)
	thirteenDaysAgo := time.Now().AddDate(0, 0, -13)
	twentyDaysAgo := time.Now().AddDate(0, 0, -20)

	tests := []struct {
		name     string
		row      db.TableBloatRow
		severity check.Severity
	}{
		{
			name:     "never vacuumed with 250K dead",
			row:      makeTableRow("public.t1", 12000000, 250000, 2.0, 500*1024*1024, nil, nil, 0),
			severity: check.SeverityFail,
		},
		{
			name:     "never vacuumed with 240K dead and huge percentage",
			row:      makeTableRow("public.t2", 240000, 240000, 50.0, 500*1024*1024, nil, nil, 0),
			severity: check.SeverityWarn,
		},
		{
			name:     "400K dead at 10% and 13 days stale",
			row:      makeTableRow("public.t3", 3600000, 400000, 10.0, 500*1024*1024, &thirteenDaysAgo, nil, 1),
			severity: check.SeverityFail,
		},
		{
			name:     "400K dead at 10% but only 10 days stale",
			row:      makeTableRow("public.t4", 3600000, 400000, 10.0, 500*1024*1024, &tenDaysAgo, nil, 1),
			severity: check.SeverityWarn,
		},
		{
			name:     "1M dead at low percentage and 13 days stale",
			row:      makeTableRow("public.t5", 49000000, 1000000, 2.0, 500*1024*1024, &thirteenDaysAgo, nil, 1),
			severity: check.SeverityFail,
		},
		{
			name:     "1M dead at low percentage but only 10 days stale",
			row:      makeTableRow("public.t6", 49000000, 1000000, 2.0, 500*1024*1024, &tenDaysAgo, nil, 1),
			severity: check.SeverityWarn,
		},
		{
			name:     "9K dead at high percentage - below percentage-arm floor",
			row:      makeTableRow("public.t7", 21000, 9000, 30.0, 100*1024*1024, &twentyDaysAgo, nil, 1),
			severity: check.SeverityPass,
		},
		{
			name:     "100K dead at low percentage and 4 days stale",
			row:      makeTableRow("public.t8", 4900000, 100000, 2.0, 500*1024*1024, &fourDaysAgo, nil, 3),
			severity: check.SeverityWarn,
		},
		{
			name:     "50K dead at 15% and 4 days stale",
			row:      makeTableRow("public.t9", 283000, 50000, 15.0, 200*1024*1024, &fourDaysAgo, nil, 3),
			severity: check.SeverityWarn,
		},
		{
			name:     "50K dead at 15% but only 2 days stale",
			row:      makeTableRow("public.t10", 283000, 50000, 15.0, 200*1024*1024, &twoDaysAgo, nil, 3),
			severity: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: []db.TableBloatRow{tt.row}}
			checker := tablebloat.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			staleVacuumFinding := report.Results[1]
			assert.Equal(t, "stale-vacuum", staleVacuumFinding.ID)
			assert.Equal(t, tt.severity, staleVacuumFinding.Severity)
		})
	}
}

func TestTableBloat_FindingSeverityEscalation(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	recentVacuum := time.Now().Add(-1 * time.Hour)
	fourDaysAgo := time.Now().AddDate(0, 0, -4)

	tests := []struct {
		name          string
		rows          []db.TableBloatRow
		findingIdx    int
		severity      check.Severity
		rowSeverities []check.Severity
	}{
		{
			name: "high-dead-tuples escalates to fail with a critical row",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 100000, 80000, 45.0, 200*1024*1024, &recentVacuum, nil, 5),
				makeTableRow("public.t2", 100000, 25000, 25.0, 150*1024*1024, &recentVacuum, nil, 3),
			},
			findingIdx:    0,
			severity:      check.SeverityFail,
			rowSeverities: []check.Severity{check.SeverityFail, check.SeverityWarn},
		},
		{
			name: "high-dead-tuples stays warn with only warning rows",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 100000, 25000, 25.0, 150*1024*1024, &recentVacuum, nil, 3),
			},
			findingIdx:    0,
			severity:      check.SeverityWarn,
			rowSeverities: []check.Severity{check.SeverityWarn},
		},
		{
			name: "stale-vacuum escalates to fail with a critical row",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 15000000, 300000, 2.0, 500*1024*1024, nil, nil, 0),
				makeTableRow("public.t2", 4900000, 120000, 2.4, 400*1024*1024, &fourDaysAgo, nil, 8),
			},
			findingIdx:    1,
			severity:      check.SeverityFail,
			rowSeverities: []check.Severity{check.SeverityFail, check.SeverityWarn},
		},
		{
			name: "stale-vacuum stays warn with only warning rows",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 4900000, 120000, 2.4, 400*1024*1024, &fourDaysAgo, nil, 8),
			},
			findingIdx:    1,
			severity:      check.SeverityWarn,
			rowSeverities: []check.Severity{check.SeverityWarn},
		},
		{
			name: "large-bloated-tables escalates to fail with a critical row",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 50000000, 15000000, 25.0, 12*oneGB, &recentVacuum, nil, 15),
				makeTableRow("public.t2", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 8),
			},
			findingIdx:    2,
			severity:      check.SeverityFail,
			rowSeverities: []check.Severity{check.SeverityFail, check.SeverityWarn},
		},
		{
			name: "large-bloated-tables stays warn with only warning rows",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 8),
			},
			findingIdx:    2,
			severity:      check.SeverityWarn,
			rowSeverities: []check.Severity{check.SeverityWarn},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: tt.rows}
			checker := tablebloat.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			finding := report.Results[tt.findingIdx]
			assert.Equal(t, tt.severity, finding.Severity)
			assert.Equal(t, tt.severity, report.Severity)

			require.NotNil(t, finding.Table)
			require.Len(t, finding.Table.Rows, len(tt.rowSeverities))
			for i, rowSeverity := range tt.rowSeverities {
				assert.Equal(t, rowSeverity, finding.Table.Rows[i].Severity)
			}
		})
	}
}

func TestTableBloat_EmptyResult(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.TableBloatRow{}}
	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Len(t, report.Results, 1)
	assert.Equal(t, "table-bloat", report.Results[0].ID)
	assert.Equal(t, check.SeverityPass, report.Results[0].Severity)
	assert.Contains(t, report.Results[0].Details, "No tables with significant dead tuples found")
}

func TestTableBloat_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.TableBloatRow{}}
	checker := tablebloat.New(queryer)

	metadata := checker.Metadata()
	assert.Equal(t, "table-bloat", metadata.CheckID)
	assert.Equal(t, "Table Bloat", metadata.Name)
	assert.Equal(t, check.CategoryVacuum, metadata.Category)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
	assert.NotEmpty(t, metadata.Description)
}
