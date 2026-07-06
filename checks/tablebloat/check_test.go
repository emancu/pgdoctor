package tablebloat_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tablebloat"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
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

func TestTableBloat_SeverityInvariant(t *testing.T) {
	t.Parallel()

	// A single table that trips every subcheck's critical tier: 45% dead tuples,
	// never vacuumed with >50K dead tuples, and >10GB in size.
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.big", 100000, 60000, 45.0, 11*1024*1024*1024, nil, nil, 0),
		},
	}

	report, err := tablebloat.New(queryer).Check(context.Background())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
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
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 3)
	assert.Equal(t, "high-dead-tuples", report.Results[0].ID)
	assert.Equal(t, "stale-vacuum", report.Results[1].ID)
	assert.Equal(t, "large-bloated-tables", report.Results[2].ID)
	assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
	assert.Equal(t, check.SeverityOK, report.Results[1].Severity)
	assert.Equal(t, check.SeverityOK, report.Results[2].Severity)
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
	assert.Equal(t, check.SeverityWarn, report.Severity)

	highDeadFinding := report.Results[0]
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Severity)
}

func TestTableBloat_StaleVacuum_NeverVacuumed(t *testing.T) {
	t.Parallel()

	// Never vacuumed with >50K dead tuples escalates the finding to FAIL.
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.logs", 1000000, 60000, 6.0, 200*1024*1024, nil, nil, 0),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	staleVacuumFinding := report.Results[1]
	assert.Equal(t, "stale-vacuum", staleVacuumFinding.ID)
	assert.Equal(t, check.SeverityFail, staleVacuumFinding.Severity)
	assert.Contains(t, staleVacuumFinding.Details, "not vacuumed recently")

	require.NotNil(t, staleVacuumFinding.Table)
	assert.Equal(t,
		[]string{"Table", "Size", "Dead %", "Dead Tuples", "Last Vacuum", "Autovacuum Count"},
		staleVacuumFinding.Table.Headers)
	// Table column is schema-qualified; row severity matches the escalated finding.
	assert.Equal(t, "public.logs", staleVacuumFinding.Table.Rows[0].Cells[0])
	assert.Equal(t, check.SeverityFail, staleVacuumFinding.Table.Rows[0].Severity)
	checktest.AssertSeverityInvariant(t, report)
}

func TestTableBloat_StaleVacuum_FailEscalation(t *testing.T) {
	t.Parallel()

	fourDaysAgo := time.Now().AddDate(0, 0, -4)
	twentySixDaysAgo := time.Now().AddDate(0, 0, -26)

	// One WARN row (12% dead, 4 days stale) and one FAIL row (>50K dead, >25 days
	// stale). The finding severity must derive from the worst row (FAIL), and the
	// worst row must sort first.
	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			makeTableRow("public.warn_table", 880000, 120000, 12.0, 300*1024*1024, &fourDaysAgo, nil, 8),
			makeTableRow("public.fail_table", 940000, 200000, 6.0, 500*1024*1024, &twentySixDaysAgo, nil, 2),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	staleVacuumFinding := report.Results[1]
	assert.Equal(t, "stale-vacuum", staleVacuumFinding.ID)
	assert.Equal(t, check.SeverityFail, staleVacuumFinding.Severity)

	require.NotNil(t, staleVacuumFinding.Table)
	require.Len(t, staleVacuumFinding.Table.Rows, 2)
	// Worst-first: the FAIL table (more dead tuples) sorts ahead of the WARN table.
	assert.Equal(t, "public.fail_table", staleVacuumFinding.Table.Rows[0].Cells[0])
	assert.Equal(t, check.SeverityFail, staleVacuumFinding.Table.Rows[0].Severity)
	assert.Equal(t, "public.warn_table", staleVacuumFinding.Table.Rows[1].Cells[0])
	assert.Equal(t, check.SeverityWarn, staleVacuumFinding.Table.Rows[1].Severity)

	checktest.AssertSeverityInvariant(t, report)
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
	assert.Equal(t, check.SeverityWarn, report.Severity)

	largeBloatFinding := report.Results[2]
	assert.Equal(t, check.SeverityWarn, largeBloatFinding.Severity)
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
			// Stale vacuum - critical
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
	assert.Equal(t, check.SeverityWarn, report.Severity)
	assert.Len(t, report.Results, 3)

	// All three subchecks should have findings
	for _, finding := range report.Results {
		assert.NotEqual(t, check.SeverityOK, finding.Severity)
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
	tenDaysAgo := time.Now().AddDate(0, 0, -10)
	twentySixDaysAgo := time.Now().AddDate(0, 0, -26)
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)

	tests := []struct {
		name                     string
		row                      db.TableBloatRow
		expectedHighDeadSeverity check.Severity
		expectedStaleSeverity    check.Severity
		expectedLargeSeverity    check.Severity
	}{
		{
			name:                     "20% dead, recently vacuumed",
			row:                      makeTableRow("public.t1", 80000, 20000, 20.0, 100*1024*1024, &recentVacuum, nil, 5),
			expectedHighDeadSeverity: check.SeverityWarn,
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			name:                     "40% dead, recently vacuumed",
			row:                      makeTableRow("public.t2", 60000, 40000, 40.0, 150*1024*1024, &recentVacuum, nil, 3),
			expectedHighDeadSeverity: check.SeverityWarn,
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			// Dead pressure present (11%) but only 2 days stale: under the 3-day WARN gate.
			name:                     "11% dead, vacuumed 2 days ago - within window",
			row:                      makeTableRow("public.t3", 900000, 100000, 11.0, 200*1024*1024, &twoDaysAgo, nil, 5),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			// WARN via ratio arm: >10% dead AND >3 days stale.
			name:                     "12% dead, vacuumed 4 days ago - warn ratio",
			row:                      makeTableRow("public.t4", 880000, 120000, 12.0, 300*1024*1024, &fourDaysAgo, nil, 2),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityWarn,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			// WARN via absolute arm: >100K dead AND >3 days stale, even at low ratio.
			name:                     "5% dead but 150K dead, vacuumed 4 days ago - warn absolute",
			row:                      makeTableRow("public.t5", 2850000, 150000, 5.0, 300*1024*1024, &fourDaysAgo, nil, 4),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityWarn,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			// Neither dead-pressure arm met (8% ratio, 60K dead < 100K): stays OK.
			name:                     "8% dead, 60K dead, vacuumed 10 days ago - below gates",
			row:                      makeTableRow("public.t6", 690000, 60000, 8.0, 100*1024*1024, &tenDaysAgo, nil, 3),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			// FAIL: >50K dead AND >25 days stale.
			name:                     "60K dead, vacuumed 26 days ago - fail",
			row:                      makeTableRow("public.t7", 940000, 60000, 6.0, 100*1024*1024, &twentySixDaysAgo, nil, 2),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityFail,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			// 50K dead is not > 50K: the FAIL dead gate is strict, so stays OK.
			name:                     "exactly 50K dead, vacuumed 30 days ago - below fail gate",
			row:                      makeTableRow("public.t8", 780000, 50000, 6.0, 100*1024*1024, &thirtyDaysAgo, nil, 2),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityOK,
		},
		{
			name:                     "1GB + 10% dead, recently vacuumed - large warn",
			row:                      makeTableRow("public.t9", 9000000, 1000000, 10.0, oneGB, &recentVacuum, nil, 10),
			expectedHighDeadSeverity: check.SeverityOK,
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityWarn,
		},
		{
			name:                     "10GB + 20% dead, recently vacuumed",
			row:                      makeTableRow("public.t10", 40000000, 10000000, 20.0, tenGB, &recentVacuum, nil, 15),
			expectedHighDeadSeverity: check.SeverityWarn, // 20% triggers high-dead too
			expectedStaleSeverity:    check.SeverityOK,
			expectedLargeSeverity:    check.SeverityWarn,
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
			checktest.AssertSeverityInvariant(t, report)
		})
	}
}

func TestTableBloat_EmptyResult(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.TableBloatRow{}}
	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 1)
	assert.Equal(t, "table-bloat", report.Results[0].ID)
	assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
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
