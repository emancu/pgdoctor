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
	checktest.AssertSeverityInvariant(t, report)
	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Len(t, report.Results, 2)
	assert.Equal(t, "high-dead-tuples", report.Results[0].ID)
	assert.Equal(t, "large-bloated-tables", report.Results[1].ID)
	assert.Equal(t, check.SeverityPass, report.Results[0].Severity)
	assert.Equal(t, check.SeverityPass, report.Results[1].Severity)
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
	checktest.AssertSeverityInvariant(t, report)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	highDeadFinding := report.Results[0]
	assert.Equal(t, "high-dead-tuples", highDeadFinding.ID)
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Severity)
	assert.Contains(t, highDeadFinding.Details, "1 table(s)")
	assert.NotNil(t, highDeadFinding.Table)
	assert.Len(t, highDeadFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Table.Rows[0].Severity)
}

func TestTableBloat_HighDeadTuples_ExtremeStaysWarn(t *testing.T) {
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
	checktest.AssertSeverityInvariant(t, report)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	highDeadFinding := report.Results[0]
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Severity)
	require.NotNil(t, highDeadFinding.Table)
	require.Len(t, highDeadFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityWarn, highDeadFinding.Table.Rows[0].Severity)
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
	checktest.AssertSeverityInvariant(t, report)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	largeBloatFinding := report.Results[1]
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
	checktest.AssertSeverityInvariant(t, report)
	assert.Equal(t, check.SeverityFail, report.Severity)

	largeBloatFinding := report.Results[1]
	assert.Equal(t, check.SeverityFail, largeBloatFinding.Severity)
	require.NotNil(t, largeBloatFinding.Table)
	require.Len(t, largeBloatFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityFail, largeBloatFinding.Table.Rows[0].Severity)
}

func TestTableBloat_MixedSeverity(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	recentVacuum := time.Now().Add(-1 * time.Hour)

	queryer := &mockQueryer{
		rows: []db.TableBloatRow{
			// High dead tuples - warn (extreme %, but never escalates past WARN)
			makeTableRow("public.t1", 100000, 80000, 45.0, 200*1024*1024, &recentVacuum, nil, 5),
			// High dead tuples - warning
			makeTableRow("public.t2", 100000, 25000, 25.0, 150*1024*1024, &recentVacuum, nil, 3),
			// Large bloated - critical
			makeTableRow("public.t4", 100000000, 30000000, 30.0, 15*oneGB, &recentVacuum, nil, 10),
			// Large bloated - warning
			makeTableRow("public.t5", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 8),
		},
	}

	checker := tablebloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Len(t, report.Results, 2)

	assert.Equal(t, check.SeverityWarn, report.Results[0].Severity, "high-dead-tuples severity")
	assert.Equal(t, check.SeverityFail, report.Results[1].Severity, "large-bloated-tables severity")

	for _, finding := range report.Results {
		assert.NotNil(t, finding.Table)
	}
}

func TestTableBloat_EdgeCases_ExactThresholds(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	const tenGB = 10 * oneGB
	recentVacuum := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name                     string
		row                      db.TableBloatRow
		expectedHighDeadSeverity check.Severity
		expectedLargeSeverity    check.Severity
	}{
		{
			name:                     "exactly 20% dead - warning threshold",
			row:                      makeTableRow("public.t1", 80000, 20000, 20.0, 100*1024*1024, &recentVacuum, nil, 5),
			expectedHighDeadSeverity: check.SeverityWarn,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "exactly 40% dead - still WARN, never escalates",
			row:                      makeTableRow("public.t2", 60000, 40000, 40.0, 150*1024*1024, &recentVacuum, nil, 3),
			expectedHighDeadSeverity: check.SeverityWarn,
			expectedLargeSeverity:    check.SeverityPass,
		},
		{
			name:                     "exactly 1GB + 10% dead - warning",
			row:                      makeTableRow("public.t5", 9000000, 1000000, 10.0, oneGB, &recentVacuum, nil, 10),
			expectedHighDeadSeverity: check.SeverityPass,
			expectedLargeSeverity:    check.SeverityWarn,
		},
		{
			name:                     "exactly 10GB + 20% dead - critical",
			row:                      makeTableRow("public.t6", 40000000, 10000000, 20.0, tenGB, &recentVacuum, nil, 15),
			expectedHighDeadSeverity: check.SeverityWarn, // 20% triggers high-dead too
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
			checktest.AssertSeverityInvariant(t, report)
			assert.Equal(t, tt.expectedHighDeadSeverity, report.Results[0].Severity, "high-dead-tuples severity")
			assert.Equal(t, tt.expectedLargeSeverity, report.Results[1].Severity, "large-bloated-tables severity")
		})
	}
}

func TestTableBloat_FindingSeverityEscalation(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	recentVacuum := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name          string
		rows          []db.TableBloatRow
		findingIdx    int
		severity      check.Severity
		rowSeverities []check.Severity
	}{
		{
			name: "high-dead-tuples never escalates: extreme rows stay warn",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 100000, 80000, 45.0, 200*1024*1024, &recentVacuum, nil, 5),
				makeTableRow("public.t2", 100000, 25000, 25.0, 150*1024*1024, &recentVacuum, nil, 3),
			},
			findingIdx:    0,
			severity:      check.SeverityWarn,
			rowSeverities: []check.Severity{check.SeverityWarn, check.SeverityWarn},
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
			name: "large-bloated-tables escalates to fail with a critical row",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 50000000, 15000000, 25.0, 12*oneGB, &recentVacuum, nil, 15),
				makeTableRow("public.t2", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 8),
			},
			findingIdx:    1,
			severity:      check.SeverityFail,
			rowSeverities: []check.Severity{check.SeverityFail, check.SeverityWarn},
		},
		{
			name: "large-bloated-tables stays warn with only warning rows",
			rows: []db.TableBloatRow{
				makeTableRow("public.t1", 10000000, 1500000, 15.0, 2*oneGB, &recentVacuum, nil, 8),
			},
			findingIdx:    1,
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
			checktest.AssertSeverityInvariant(t, report)
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
	checktest.AssertSeverityInvariant(t, report)
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
