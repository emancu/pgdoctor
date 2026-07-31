package tablevacuumhealth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tablevacuumhealth"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	findingIDAutovacuumDisabled = "autovacuum-disabled"
	findingIDLargeTableDefaults = "large-table-defaults"
	findingIDVacuumStale        = "vacuum-stale"
)

type mockQueryer struct {
	rows []db.TableVacuumHealthRow
	err  error
}

func (m *mockQueryer) TableVacuumHealth(context.Context) ([]db.TableVacuumHealthRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

type rowBuilder struct {
	row db.TableVacuumHealthRow
}

func makeRow(tableName string) *rowBuilder {
	return &rowBuilder{
		row: db.TableVacuumHealthRow{
			TableName:        pgtype.Text{String: tableName, Valid: true},
			EstimatedRows:    pgtype.Int8{Int64: 0, Valid: true},
			TableSizeBytes:   pgtype.Int8{Int64: 0, Valid: true},
			NDeadTup:         pgtype.Int8{Int64: 0, Valid: true},
			VacuumCount:      pgtype.Int8{Int64: 0, Valid: true},
			AutovacuumCount:  pgtype.Int8{Int64: 0, Valid: true},
			Reloptions:       pgtype.Text{String: "", Valid: false},
			NModSinceAnalyze: pgtype.Int8{Int64: 0, Valid: true},
			AnalyzeCount:     pgtype.Int8{Int64: 0, Valid: true},
			AutoanalyzeCount: pgtype.Int8{Int64: 0, Valid: true},
			NInsSinceVacuum:  pgtype.Int8{Int64: 0, Valid: true},
		},
	}
}

func (b *rowBuilder) withRows(rows int64) *rowBuilder {
	b.row.EstimatedRows = pgtype.Int8{Int64: rows, Valid: true}
	return b
}

func (b *rowBuilder) withSize(sizeBytes int64) *rowBuilder {
	b.row.TableSizeBytes = pgtype.Int8{Int64: sizeBytes, Valid: true}
	return b
}

func (b *rowBuilder) withDeadTuples(deadTup int64) *rowBuilder {
	b.row.NDeadTup = pgtype.Int8{Int64: deadTup, Valid: true}
	return b
}

func (b *rowBuilder) withReloptions(reloptions string) *rowBuilder {
	b.row.Reloptions = pgtype.Text{String: reloptions, Valid: reloptions != ""}
	return b
}

func (b *rowBuilder) withLastAutovacuum(t time.Time) *rowBuilder {
	b.row.LastAutovacuum = pgtype.Timestamptz{Time: t, Valid: true}
	return b
}

// withVacuumCount sets autovacuum_count.
func (b *rowBuilder) withVacuumCount(count int64) *rowBuilder {
	b.row.AutovacuumCount = pgtype.Int8{Int64: count, Valid: true}
	return b
}

// withManualVacuumCount sets the manual vacuum_count.
func (b *rowBuilder) withManualVacuumCount(count int64) *rowBuilder {
	b.row.VacuumCount = pgtype.Int8{Int64: count, Valid: true}
	return b
}

func (b *rowBuilder) withLastVacuumAny(t time.Time) *rowBuilder {
	b.row.LastVacuumAny = pgtype.Timestamptz{Time: t, Valid: true}
	return b
}

func (b *rowBuilder) withLastAnalyzeAny(t time.Time) *rowBuilder {
	b.row.LastAnalyzeAny = pgtype.Timestamptz{Time: t, Valid: true}
	return b
}

func (b *rowBuilder) withModSinceAnalyze(mods int64) *rowBuilder {
	b.row.NModSinceAnalyze = pgtype.Int8{Int64: mods, Valid: true}
	return b
}

// withAnalyzeCount sets autoanalyze_count.
func (b *rowBuilder) withAnalyzeCount(count int64) *rowBuilder {
	b.row.AutoanalyzeCount = pgtype.Int8{Int64: count, Valid: true}
	return b
}

// withManualAnalyzeCount sets the manual analyze_count.
func (b *rowBuilder) withManualAnalyzeCount(count int64) *rowBuilder {
	b.row.AnalyzeCount = pgtype.Int8{Int64: count, Valid: true}
	return b
}

func (b *rowBuilder) withInsSinceVacuum(inserts int64) *rowBuilder {
	b.row.NInsSinceVacuum = pgtype.Int8{Int64: inserts, Valid: true}
	return b
}

func (b *rowBuilder) build() db.TableVacuumHealthRow {
	return b.row
}

func runCheck(t *testing.T, rows []db.TableVacuumHealthRow) *check.Report {
	t.Helper()

	checker := tablevacuumhealth.New(&mockQueryer{rows: rows})
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

func findingByID(t *testing.T, report *check.Report, id string) *check.Finding {
	t.Helper()

	for i := range report.Results {
		if report.Results[i].ID == id {
			return &report.Results[i]
		}
	}
	t.Fatalf("finding %q not found", id)
	return nil
}

// Time offsets shared across staleness tests. The check computes its own
// time.Now() slightly after these are captured, so boundaries carry a 1-minute
// margin (far larger than any execution delay) to stay deterministic.
var (
	recent      = time.Now().Add(-1 * time.Hour)
	staleWarn   = time.Now().Add(-10 * 24 * time.Hour) // >7d, <25d
	staleFail   = time.Now().Add(-30 * 24 * time.Hour) // >25d
	justUnder7d = time.Now().Add(-(7*24*time.Hour - time.Minute))
	justPast7d  = time.Now().Add(-(7*24*time.Hour + time.Minute))
	justUnder25 = time.Now().Add(-(25*24*time.Hour - time.Minute))
	justPast25d = time.Now().Add(-(25*24*time.Hour + time.Minute))
)

func TestTableVacuumHealth_AllHealthy(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.users").
			withRows(10000).
			withSize(1024 * 1024).
			withDeadTuples(100).
			withLastVacuumAny(recent).
			withLastAnalyzeAny(recent).
			build(),
	})

	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Len(t, report.Results, 3)
	for _, finding := range report.Results {
		assert.Equal(t, check.SeverityPass, finding.Severity)
	}
}

func TestTableVacuumHealth_AutovacuumDisabled_Found(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.staging_table").
			withRows(10000).
			withReloptions("autovacuum_enabled=false").
			withLastVacuumAny(recent).
			withLastAnalyzeAny(recent).
			build(),
	})

	disabled := findingByID(t, report, findingIDAutovacuumDisabled)
	assert.Equal(t, check.SeverityWarn, disabled.Severity)
	assert.Contains(t, disabled.Details, "public.staging_table")
}

func TestTableVacuumHealth_LargeTableDefaults_UsingDefaults_Warning(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.users").
			withRows(2_000_000).
			withSize(1024 * 1024 * 500).
			withDeadTuples(50_000).
			withVacuumCount(100).
			withLastAutovacuum(recent).
			withLastVacuumAny(recent).
			withLastAnalyzeAny(recent).
			build(),
	})

	large := findingByID(t, report, findingIDLargeTableDefaults)
	assert.Equal(t, check.SeverityWarn, large.Severity)
	require.NotNil(t, large.Table)
	assert.Equal(t, check.SeverityWarn, large.Table.Rows[0].Severity)
}

// --- vacuum-stale ---

func TestTableVacuumHealth_VacuumStale_AllFresh(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.users").
			withRows(10_000_000).
			withReloptions("autovacuum_vacuum_scale_factor=0.01"). // keep large-table-defaults quiet
			withDeadTuples(1_000_000).                             // lots of work, but fresh
			withLastVacuumAny(recent).
			withLastAnalyzeAny(recent).
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	assert.Equal(t, check.SeverityPass, stale.Severity)
	assert.Nil(t, stale.Table)
}

func TestTableVacuumHealth_VacuumStale_VacuumArmWarning(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.busy").
			withRows(2_000_000).
			withSize(1024 * 1024 * 100).
			withDeadTuples(200_000).
			withInsSinceVacuum(50_000). // vacuum work = 250K exactly
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(recent). // analyze fresh -> only vacuum arm trips
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	assert.Equal(t, check.SeverityWarn, stale.Severity)
	require.NotNil(t, stale.Table)
	require.Len(t, stale.Table.Rows, 1)
	assert.Equal(t, check.SeverityWarn, stale.Table.Rows[0].Severity)
	assert.Equal(t, "250.0K", stale.Table.Rows[0].Cells[3])
}

func TestTableVacuumHealth_VacuumStale_VacuumArmFail(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.forgotten").
			withRows(5_000_000).
			withDeadTuples(500_000). // >= 500K
			withLastVacuumAny(staleFail).
			withLastAnalyzeAny(recent).
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	assert.Equal(t, check.SeverityFail, stale.Severity)
	assert.Equal(t, check.SeverityFail, stale.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_VacuumStale_AnalyzeArmOnly(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.append_only").
			withRows(3_000_000).
			withDeadTuples(0). // no vacuum work at all
			withModSinceAnalyze(300_000).
			withLastVacuumAny(recent).     // vacuum fresh
			withLastAnalyzeAny(staleWarn). // analyze arm trips
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	assert.Equal(t, check.SeverityWarn, stale.Severity)
	require.Len(t, stale.Table.Rows, 1)
	assert.Equal(t, "300.0K", stale.Table.Rows[0].Cells[3])
}

func TestTableVacuumHealth_VacuumStale_ZeroWorkStaleNotListed(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.dormant").
			withRows(9_000_000).
			withDeadTuples(0).
			withInsSinceVacuum(0).
			withModSinceAnalyze(0).
			withLastVacuumAny(staleFail).  // ancient
			withLastAnalyzeAny(staleFail). // ancient
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	assert.Equal(t, check.SeverityPass, stale.Severity)
	assert.Nil(t, stale.Table)
}

func TestTableVacuumHealth_VacuumStale_NeverVacuumedWithWork(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.never").
			withRows(2_000_000).
			withDeadTuples(600_000). // >= 500K FAIL floor
			build(),                 // no vacuum/analyze timestamps -> infinitely stale
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	assert.Equal(t, check.SeverityFail, stale.Severity)
	require.Len(t, stale.Table.Rows, 1)
	assert.Equal(t, "never", stale.Table.Rows[0].Cells[4]) // Last Vacuum
	assert.Equal(t, "never", stale.Table.Rows[0].Cells[5]) // Last Analyze
}

func TestTableVacuumHealth_VacuumStale_WarnAgeBoundary(t *testing.T) {
	t.Parallel()

	// Just under 7 days is NOT past the ">7 days" cutoff; just past it is.
	notStale := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(300_000).
			withLastVacuumAny(justUnder7d).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityPass, findingByID(t, notStale, findingIDVacuumStale).Severity)

	stale := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(300_000).
			withLastVacuumAny(justPast7d).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityWarn, findingByID(t, stale, findingIDVacuumStale).Severity)
}

func TestTableVacuumHealth_VacuumStale_WarnWorkBoundary(t *testing.T) {
	t.Parallel()

	// 249,999 does not meet the 250K floor; 250,000 does.
	below := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(249_999).
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityPass, findingByID(t, below, findingIDVacuumStale).Severity)

	atFloor := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(250_000).
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityWarn, findingByID(t, atFloor, findingIDVacuumStale).Severity)
}

func TestTableVacuumHealth_VacuumStale_FailAgeBoundary(t *testing.T) {
	t.Parallel()

	// Just under 25 days with FAIL-level work stays WARN; just past becomes FAIL.
	atEdge := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(500_000).
			withLastVacuumAny(justUnder25).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityWarn, findingByID(t, atEdge, findingIDVacuumStale).Severity)

	past := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(500_000).
			withLastVacuumAny(justPast25d).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityFail, findingByID(t, past, findingIDVacuumStale).Severity)
}

func TestTableVacuumHealth_VacuumStale_FailWorkBoundary(t *testing.T) {
	t.Parallel()

	// 499,999 stays WARN at the fail age; 500,000 becomes FAIL.
	below := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(499_999).
			withLastVacuumAny(staleFail).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityWarn, findingByID(t, below, findingIDVacuumStale).Severity)

	atFloor := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.edge").
			withRows(1_000_000).
			withDeadTuples(500_000).
			withLastVacuumAny(staleFail).
			withLastAnalyzeAny(recent).
			build(),
	})
	assert.Equal(t, check.SeverityFail, findingByID(t, atFloor, findingIDVacuumStale).Severity)
}

func TestTableVacuumHealth_VacuumStale_PendingWorkIsLargerArm(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.mixed").
			withRows(2_000_000).
			withDeadTuples(100_000).
			withInsSinceVacuum(200_000).  // vacuum work = 300K
			withModSinceAnalyze(400_000). // analyze work = 400K (larger)
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(staleWarn).
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	require.Len(t, stale.Table.Rows, 1)
	assert.Equal(t, "400.0K", stale.Table.Rows[0].Cells[3])
}

func TestTableVacuumHealth_VacuumStale_CountsInParens(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.counted").
			withRows(2_000_000).
			withDeadTuples(300_000).
			withManualVacuumCount(3).
			withVacuumCount(40). // autovacuum_count -> total 43
			withModSinceAnalyze(300_000).
			withManualAnalyzeCount(2).
			withAnalyzeCount(18). // autoanalyze_count -> total 20
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(staleWarn).
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	require.Len(t, stale.Table.Rows, 1)
	assert.Contains(t, stale.Table.Rows[0].Cells[4], "(43)")
	assert.Contains(t, stale.Table.Rows[0].Cells[5], "(20)")
}

func TestTableVacuumHealth_VacuumStale_SortedWorstFirst(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableVacuumHealthRow{
		makeRow("public.warn_small").
			withRows(1_000_000).
			withDeadTuples(260_000).
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(recent).
			build(),
		makeRow("public.fail_big").
			withRows(5_000_000).
			withDeadTuples(900_000).
			withLastVacuumAny(staleFail).
			withLastAnalyzeAny(recent).
			build(),
		makeRow("public.warn_big").
			withRows(3_000_000).
			withDeadTuples(400_000).
			withLastVacuumAny(staleWarn).
			withLastAnalyzeAny(recent).
			build(),
	})

	stale := findingByID(t, report, findingIDVacuumStale)
	require.Len(t, stale.Table.Rows, 3)
	// FAIL first, then WARN rows by descending pending work.
	assert.Equal(t, "public.fail_big", stale.Table.Rows[0].Cells[0])
	assert.Equal(t, "public.warn_big", stale.Table.Rows[1].Cells[0])
	assert.Equal(t, "public.warn_small", stale.Table.Rows[2].Cells[0])
}

func TestTableVacuumHealth_QueryError(t *testing.T) {
	t.Parallel()

	checker := tablevacuumhealth.New(&mockQueryer{err: fmt.Errorf("database connection error")})
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "table-vacuum-health")
}

func TestTableVacuumHealth_Metadata(t *testing.T) {
	t.Parallel()

	metadata := tablevacuumhealth.New(&mockQueryer{}).Metadata()

	assert.Equal(t, "table-vacuum-health", metadata.CheckID)
	assert.Equal(t, "Table Vacuum Health", metadata.Name)
	assert.Equal(t, check.CategoryVacuum, metadata.Category)
	assert.NotEmpty(t, metadata.Description)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
}
