package tablevacuumhealth_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tablevacuumhealth"
	"github.com/emancu/pgdoctor/db"
)

const (
	findingIDAutovacuumDisabled = "autovacuum-disabled"
	findingIDLargeTableDefaults = "large-table-defaults"
	findingIDVacuumStale        = "vacuum-stale"
	findingIDAnalyzeNeeded      = "analyze-needed"
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
			AutovacuumCount:  pgtype.Int8{Int64: 0, Valid: true},
			Reloptions:       pgtype.Text{String: "", Valid: false},
			NModSinceAnalyze: pgtype.Int8{Int64: 0, Valid: true},
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

func (b *rowBuilder) withVacuumCount(count int64) *rowBuilder {
	b.row.AutovacuumCount = pgtype.Int8{Int64: count, Valid: true}
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

func (b *rowBuilder) withAnalyzeCount(count int64) *rowBuilder {
	b.row.AutoanalyzeCount = pgtype.Int8{Int64: count, Valid: true}
	return b
}

func (b *rowBuilder) withInsSinceVacuum(inserts int64) *rowBuilder {
	b.row.NInsSinceVacuum = pgtype.Int8{Int64: inserts, Valid: true}
	return b
}

func (b *rowBuilder) build() db.TableVacuumHealthRow {
	return b.row
}

func TestTableVacuumHealth_AllHealthy(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.users").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 4) // 4 subchecks now

	for _, finding := range report.Results {
		assert.Equal(t, check.SeverityOK, finding.Severity)
	}
}

func TestTableVacuumHealth_AutovacuumDisabled_NoTables(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.users").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var disabledFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDAutovacuumDisabled {
			disabledFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, disabledFinding)
	assert.Equal(t, check.SeverityOK, disabledFinding.Severity)
	assert.Contains(t, disabledFinding.Details, "No tables found")
}

func TestTableVacuumHealth_AutovacuumDisabled_Found(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.staging_table").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withReloptions("autovacuum_enabled=false").
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var disabledFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDAutovacuumDisabled {
			disabledFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, disabledFinding)
	assert.Equal(t, check.SeverityWarn, disabledFinding.Severity)
	assert.Contains(t, disabledFinding.Details, "1 table(s)")
	assert.Contains(t, disabledFinding.Details, "public.staging_table")
}

func TestTableVacuumHealth_LargeTableDefaults_NoTables(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.small_table").
				withRows(10000). // Too small to be considered "large"
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeTableDefaults {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	assert.Equal(t, check.SeverityOK, largeFinding.Severity)
	assert.Contains(t, largeFinding.Details, "No large tables")
}

func TestTableVacuumHealth_LargeTableDefaults_WithCustomSettings(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.large_table").
				withRows(5_000_000). // Large table
				withSize(1024 * 1024 * 1024).
				withDeadTuples(100_000).
				withVacuumCount(50).
				withReloptions("autovacuum_vacuum_scale_factor=0.01"). // Custom setting
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeTableDefaults {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	assert.Equal(t, check.SeverityOK, largeFinding.Severity)
}

func TestTableVacuumHealth_LargeTableDefaults_UsingDefaults_Warning(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.users").
				withRows(2_000_000). // Large but <10M
				withSize(1024 * 1024 * 500).
				withDeadTuples(50_000).
				withVacuumCount(100).
				withLastAutovacuum(recentTime).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeTableDefaults {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	assert.Equal(t, check.SeverityWarn, largeFinding.Severity)
	assert.Contains(t, largeFinding.Details, "1 large table(s)")
	assert.NotNil(t, largeFinding.Table)
	assert.Equal(t, check.SeverityWarn, largeFinding.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_LargeTableDefaults_VeryLarge_Fail(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.events").
				withRows(50_000_000). // Very large (>10M)
				withSize(1024 * 1024 * 1024 * 10).
				withDeadTuples(5_000_000).
				withVacuumCount(200).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeTableDefaults {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	assert.Equal(t, check.SeverityWarn, largeFinding.Severity)
	assert.Equal(t, check.SeverityFail, largeFinding.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_LargeTableDefaults_PendingWorkIncludesInserts(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.events").
				withRows(2_000_000).
				withSize(1024 * 1024 * 1024).
				withDeadTuples(10_000).
				withInsSinceVacuum(50_000). // PG14+ inserts
				withVacuumCount(100).
				withLastAutovacuum(recentTime).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeTableDefaults {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	assert.NotNil(t, largeFinding.Table)
	// Pending Work column should show 60K (10K dead + 50K inserts)
	assert.Equal(t, "60.0K", largeFinding.Table.Rows[0].Cells[3])
}

func TestTableVacuumHealth_VacuumStale_AllFresh(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.users").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var staleFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDVacuumStale {
			staleFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, staleFinding)
	assert.Equal(t, check.SeverityOK, staleFinding.Severity)
	assert.Contains(t, staleFinding.Details, "within the last 7 days")
}

func TestTableVacuumHealth_VacuumStale_Warning(t *testing.T) {
	t.Parallel()

	staleTime := time.Now().Add(-10 * 24 * time.Hour) // 10 days ago
	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.old_table").
				withRows(50000).
				withSize(1024 * 1024 * 10).
				withDeadTuples(5000).
				withLastVacuumAny(staleTime).
				withLastAnalyzeAny(staleTime).
				build(),
			makeRow("public.fresh_table").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var staleFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDVacuumStale {
			staleFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, staleFinding)
	assert.Equal(t, check.SeverityWarn, staleFinding.Severity)
	assert.Contains(t, staleFinding.Details, "1 table(s)")
	assert.NotNil(t, staleFinding.Table)
	assert.Len(t, staleFinding.Table.Rows, 1)
	assert.Equal(t, check.SeverityWarn, staleFinding.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_VacuumStale_Fail(t *testing.T) {
	t.Parallel()

	veryStaleTime := time.Now().Add(-30 * 24 * time.Hour) // 30 days ago
	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.forgotten_table").
				withRows(100000).
				withSize(1024 * 1024 * 100).
				withDeadTuples(50000).
				withLastVacuumAny(veryStaleTime).
				withLastAnalyzeAny(veryStaleTime).
				build(),
			makeRow("public.fresh_table").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var staleFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDVacuumStale {
			staleFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, staleFinding)
	assert.Equal(t, check.SeverityWarn, staleFinding.Severity)
	assert.NotNil(t, staleFinding.Table)
	assert.Equal(t, check.SeverityFail, staleFinding.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_VacuumStale_NeverVacuumed(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.never_vacuumed").
				withRows(100000).
				withSize(1024 * 1024 * 100).
				withDeadTuples(50000).
				build(), // No vacuum times set
			makeRow("public.fresh_table").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var staleFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDVacuumStale {
			staleFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, staleFinding)
	assert.Equal(t, check.SeverityWarn, staleFinding.Severity)
}

func TestTableVacuumHealth_VacuumStale_SkipTinyTables(t *testing.T) {
	t.Parallel()

	veryStaleTime := time.Now().Add(-30 * 24 * time.Hour) // 30 days ago
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.tiny_table").
				withRows(500). // Below 1000 threshold
				withSize(1024).
				withDeadTuples(50).
				withLastVacuumAny(veryStaleTime).
				withLastAnalyzeAny(veryStaleTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var staleFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDVacuumStale {
			staleFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, staleFinding)
	assert.Equal(t, check.SeverityOK, staleFinding.Severity)
}

func TestTableVacuumHealth_AnalyzeNeeded_NoTables(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.users").
				withRows(10000).
				withSize(1024 * 1024).
				withDeadTuples(100).
				withModSinceAnalyze(5000). // Below 100K threshold
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var analyzeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDAnalyzeNeeded {
			analyzeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, analyzeFinding)
	assert.Equal(t, check.SeverityOK, analyzeFinding.Severity)
	assert.Contains(t, analyzeFinding.Details, "No tables found")
}

func TestTableVacuumHealth_AnalyzeNeeded_Warning(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.busy_table").
				withRows(50000).
				withSize(1024 * 1024 * 10).
				withDeadTuples(1000).
				withModSinceAnalyze(150_000). // Between 100K and 500K
				withAnalyzeCount(50).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var analyzeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDAnalyzeNeeded {
			analyzeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, analyzeFinding)
	assert.Equal(t, check.SeverityWarn, analyzeFinding.Severity)
	assert.Contains(t, analyzeFinding.Details, "1 table(s)")
	assert.NotNil(t, analyzeFinding.Table)
	assert.Equal(t, check.SeverityWarn, analyzeFinding.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_AnalyzeNeeded_Fail(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.very_busy_table").
				withRows(100000).
				withSize(1024 * 1024 * 50).
				withDeadTuples(5000).
				withModSinceAnalyze(600_000). // Above 500K threshold
				withAnalyzeCount(100).
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	var analyzeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDAnalyzeNeeded {
			analyzeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, analyzeFinding)
	assert.Equal(t, check.SeverityWarn, analyzeFinding.Severity)
	assert.NotNil(t, analyzeFinding.Table)
	assert.Equal(t, check.SeverityFail, analyzeFinding.Table.Rows[0].Severity)
}

func TestTableVacuumHealth_AnalyzeNeeded_SkipTinyTables(t *testing.T) {
	t.Parallel()

	recentTime := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		rows: []db.TableVacuumHealthRow{
			makeRow("public.tiny_table").
				withRows(500). // Below 1000 threshold
				withSize(1024).
				withDeadTuples(50).
				withModSinceAnalyze(600_000). // Would fail if not skipped
				withLastVacuumAny(recentTime).
				withLastAnalyzeAny(recentTime).
				build(),
		},
	}

	checker := tablevacuumhealth.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var analyzeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDAnalyzeNeeded {
			analyzeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, analyzeFinding)
	assert.Equal(t, check.SeverityOK, analyzeFinding.Severity)
}

func TestTableVacuumHealth_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{
		err: expectedErr,
	}

	checker := tablevacuumhealth.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "table-vacuum-health")
}

func TestTableVacuumHealth_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{}
	checker := tablevacuumhealth.New(queryer)
	metadata := checker.Metadata()

	assert.Equal(t, "table-vacuum-health", metadata.CheckID)
	assert.Equal(t, "Table Vacuum Health", metadata.Name)
	assert.Equal(t, check.CategoryVacuum, metadata.Category)
	assert.NotEmpty(t, metadata.Description)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
}
