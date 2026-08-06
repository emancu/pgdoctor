package freezeage_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/freezeage"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Finding IDs under test.
const (
	idDatabaseFreeze    = "database-freeze-age"
	idTableFreeze       = "table-freeze-age"
	idDatabaseMultixact = "database-multixact-age"
	idTableMultixact    = "table-multixact-age"
)

// Thresholds at stock GUCs, restated so a formula change has to be deliberate:
// WARN = 2 x effective trigger, FAIL = min(4 x trigger, failsafe age). An age just
// below the trigger is the healthy peak of the sawtooth, not a warning.
const (
	defaultTrigger   = int64(200_000_000)
	defaultMxTrigger = int64(400_000_000)
	xidWarn          = int64(400_000_000)
	xidFail          = int64(800_000_000)
	mxidWarn         = int64(800_000_000)
	mxidFail         = int64(1_600_000_000)
)

// pgtype helper constructors.

func pgInt8(i int64) pgtype.Int8 { return pgtype.Int8{Int64: i, Valid: true} }

func pgTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

type mockQueryer struct {
	dbRow     db.DatabaseFreezeAgeRow
	tableRows []db.TableFreezeAgeRow
	dbErr     error
	tableErr  error
}

func (m *mockQueryer) DatabaseFreezeAge(context.Context) (db.DatabaseFreezeAgeRow, error) {
	if m.dbErr != nil {
		return db.DatabaseFreezeAgeRow{}, m.dbErr
	}
	return m.dbRow, nil
}

func (m *mockQueryer) TableFreezeAge(context.Context) ([]db.TableFreezeAgeRow, error) {
	if m.tableErr != nil {
		return nil, m.tableErr
	}
	return m.tableRows, nil
}

// Scenario builders: the connected database.

func database(name string, freezeAge int64) db.DatabaseFreezeAgeRow {
	return db.DatabaseFreezeAgeRow{
		DatabaseName:          name,
		FrozenXid:             "1000",
		FreezeAge:             freezeAge,
		MinMultixactID:        "1",
		MultixactAge:          0,
		FreezeMaxAge:          pgInt8(defaultTrigger),
		MultixactFreezeMaxAge: pgInt8(defaultMxTrigger),
		FailsafeAge:           pgInt8(1_600_000_000),
		MultixactFailsafeAge:  pgInt8(1_600_000_000),
	}
}

func databaseWithMultixact(name string, multixactAge int64) db.DatabaseFreezeAgeRow {
	row := database(name, 1000)
	row.MultixactAge = multixactAge
	return row
}

// neverUsedMultixactDatabase mirrors what the query emits for datminmxid = 0: the
// SQL guard turns mxid_age('0') = 2147483647 into 0.
func neverUsedMultixactDatabase(name string) db.DatabaseFreezeAgeRow {
	row := database(name, 1000)
	row.MinMultixactID = "0"
	row.MultixactAge = 0
	return row
}

// Scenario builders: VACUUM targets.

func relation(name string, freezeAge int64) db.TableFreezeAgeRow {
	return db.TableFreezeAgeRow{
		VacuumTarget:                   name,
		WorstRelation:                  name,
		GroupedRelations:               1,
		ToastRelations:                 0,
		Relkind:                        "r",
		Relpages:                       100,
		SizeBytesEst:                   100 * 8192,
		FreezeAge:                      freezeAge,
		MultixactAge:                   0,
		EffectiveFreezeMaxAge:          defaultTrigger,
		EffectiveMultixactFreezeMaxAge: defaultMxTrigger,
		FailsafeAge:                    1_600_000_000,
		MultixactFailsafeAge:           1_600_000_000,
		XidReloption:                   0,
		MultixactReloption:             0,
		LastAutovacuum:                 pgTime(time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)),
		AutovacuumCount:                7,
		VacuumCount:                    1,
		TotalAboveFloor:                1,
	}
}

func relationWithMultixact(name string, multixactAge int64) db.TableFreezeAgeRow {
	row := relation(name, 1000)
	row.MultixactAge = multixactAge
	return row
}

// neverVacuumedRelation has relpages = 0, which is "not measured yet", not empty.
func neverVacuumedRelation(name string, freezeAge int64) db.TableFreezeAgeRow {
	row := relation(name, freezeAge)
	row.Relpages = 0
	row.SizeBytesEst = 0
	row.LastAutovacuum = pgtype.Timestamptz{}
	row.AutovacuumCount = 0
	row.VacuumCount = 0
	return row
}

// toastGroup is what the query emits when a TOAST relation pulled its parent in:
// one row per VACUUM target, the TOAST name kept only for Debug.
func toastGroup(parent, toastRelation string, freezeAge int64) db.TableFreezeAgeRow {
	row := relation(parent, freezeAge)
	row.WorstRelation = toastRelation
	row.GroupedRelations = 2
	row.ToastRelations = 1
	return row
}

// loweredTriggerRelation carries a per-table autovacuum_freeze_max_age reloption,
// which can only lower the effective trigger.
func loweredTriggerRelation(name string, freezeAge, reloption int64) db.TableFreezeAgeRow {
	row := relation(name, freezeAge)
	row.EffectiveFreezeMaxAge = reloption
	row.XidReloption = reloption
	return row
}

// loweredMultixactTriggerRelation is the MultiXact mirror.
func loweredMultixactTriggerRelation(name string, multixactAge, reloption int64) db.TableFreezeAgeRow {
	row := relationWithMultixact(name, multixactAge)
	row.EffectiveMultixactFreezeMaxAge = reloption
	row.MultixactReloption = reloption
	return row
}

// Helpers.

// healthy is a queryer with nothing wrong anywhere, so a test can vary one axis.
func healthy() *mockQueryer {
	return &mockQueryer{
		dbRow:     database("appdb", 1000),
		tableRows: []db.TableFreezeAgeRow{},
	}
}

func run(t *testing.T, queryer *mockQueryer) *check.Report {
	t.Helper()

	report, err := freezeage.New(queryer).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	return report
}

func findingByID(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()

	for _, finding := range report.Results {
		if finding.ID == id {
			return finding
		}
	}
	t.Fatalf("finding %q not found in %d results", id, len(report.Results))

	return check.Finding{}
}

// TestFreezeAge_HealthyProductionShape is the case that matters most: a real
// instance whose relations sit just below their trigger because that is the top of
// the normal sawtooth. Every finding must PASS and nothing may render a table.
func TestFreezeAge_HealthyProductionShape(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		// age(datfrozenxid) at 99.95% of the 200M trigger.
		dbRow: database("appdb", 199_900_000),
		// Relations aged 199.2M-199.9M, all just under the trigger. The reporting
		// floor keeps them out of the result set in production; classify them PASS
		// anyway, so a floor change cannot resurrect the false WARN.
		tableRows: []db.TableFreezeAgeRow{
			relation("public.bookings", 199_900_000),
			relation("public.appointments", 199_600_000),
			relation("public.customers", 199_200_000),
		},
	}

	report := run(t, queryer)

	require.Equal(t, check.SeverityPass, report.Severity, "a healthy instance must not report WARN")
	require.Len(t, report.Results, 4)
	for _, finding := range report.Results {
		assert.Equal(t, check.SeverityPass, finding.Severity, "finding %s", finding.ID)
		assert.Nil(t, finding.Table, "finding %s must not render a table when healthy", finding.ID)
	}

	assert.Contains(t, findingByID(t, report, idDatabaseFreeze).Details, "1.0×")
	assert.Contains(t, findingByID(t, report, idDatabaseFreeze).Details, "normal sawtooth")
	assert.Contains(t, findingByID(t, report, idTableFreeze).Details, "normal sawtooth")
}

func TestFreezeAge_AllSubchecksReported(t *testing.T) {
	t.Parallel()

	report := run(t, healthy())

	require.Len(t, report.Results, 4)
	for _, id := range []string{idDatabaseFreeze, idTableFreeze, idDatabaseMultixact, idTableMultixact} {
		assert.Equal(t, check.SeverityPass, findingByID(t, report, id).Severity, "finding %s", id)
	}
	assert.Equal(t, check.SeverityPass, report.Severity)
}

func TestFreezeAge_DatabaseFreezeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		age      int64
		severity check.Severity
	}{
		{name: "at the trigger is the healthy sawtooth peak", age: defaultTrigger, severity: check.SeverityPass},
		{name: "below 2x trigger", age: xidWarn - 1, severity: check.SeverityPass},
		{name: "at 2x trigger", age: xidWarn, severity: check.SeverityWarn},
		{name: "below 4x trigger", age: xidFail - 1, severity: check.SeverityWarn},
		{name: "at 4x trigger", age: xidFail, severity: check.SeverityFail},
		{name: "saturated age", age: 2_147_483_647, severity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.dbRow = database("appdb", tt.age)

			finding := findingByID(t, run(t, queryer), idDatabaseFreeze)
			assert.Equal(t, tt.severity, finding.Severity, "age=%d", tt.age)
		})
	}
}

func TestFreezeAge_DatabaseMultixactBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		age      int64
		severity check.Severity
	}{
		{name: "at the trigger is the healthy sawtooth peak", age: defaultMxTrigger, severity: check.SeverityPass},
		{name: "below 2x trigger", age: mxidWarn - 1, severity: check.SeverityPass},
		{name: "at 2x trigger", age: mxidWarn, severity: check.SeverityWarn},
		{name: "at multixact failsafe", age: mxidFail, severity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.dbRow = databaseWithMultixact("appdb", tt.age)

			finding := findingByID(t, run(t, queryer), idDatabaseMultixact)
			assert.Equal(t, tt.severity, finding.Severity, "multixact age=%d", tt.age)
		})
	}
}

func TestFreezeAge_TableFreezeBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		age      int64
		severity check.Severity
	}{
		{name: "at the trigger is the healthy sawtooth peak", age: defaultTrigger, severity: check.SeverityPass},
		{name: "below 2x trigger", age: xidWarn - 1, severity: check.SeverityPass},
		{name: "at 2x trigger", age: xidWarn, severity: check.SeverityWarn},
		{name: "at 4x trigger", age: xidFail, severity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.tableRows = []db.TableFreezeAgeRow{relation("public.bookings", tt.age)}

			finding := findingByID(t, run(t, queryer), idTableFreeze)
			assert.Equal(t, tt.severity, finding.Severity, "age=%d", tt.age)
		})
	}
}

func TestFreezeAge_TableMultixactBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		age      int64
		severity check.Severity
	}{
		{name: "at the trigger is the healthy sawtooth peak", age: defaultMxTrigger, severity: check.SeverityPass},
		{name: "below 2x trigger", age: mxidWarn - 1, severity: check.SeverityPass},
		{name: "at 2x trigger", age: mxidWarn, severity: check.SeverityWarn},
		{name: "at multixact failsafe", age: mxidFail, severity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.tableRows = []db.TableFreezeAgeRow{relationWithMultixact("public.bookings", tt.age)}

			finding := findingByID(t, run(t, queryer), idTableMultixact)
			assert.Equal(t, tt.severity, finding.Severity, "multixact age=%d", tt.age)
		})
	}
}

// A zero datminmxid/relminmxid must never FAIL: mxid_age('0'::xid) returns
// 2147483647 and the SQL guard is the only thing standing between that and a
// fabricated emergency.
func TestFreezeAge_ZeroMultixactIsNotAnEmergency(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRow = neverUsedMultixactDatabase("appdb")
	queryer.tableRows = []db.TableFreezeAgeRow{relationWithMultixact("public.bookings", 0)}

	report := run(t, queryer)

	assert.Equal(t, check.SeverityPass, findingByID(t, report, idDatabaseMultixact).Severity)
	assert.Equal(t, check.SeverityPass, findingByID(t, report, idTableMultixact).Severity)
	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Contains(t, freezeage.Metadata().SQL, "<> '0'::xid", "the SQL guard must not be dropped")
}

func TestFreezeAge_ToastCollapsesIntoParent(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.tableRows = []db.TableFreezeAgeRow{
		toastGroup("public.bookings", "pg_toast.pg_toast_16452", xidWarn),
	}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	require.Len(t, finding.Table.Rows, 1, "the TOAST relation and its parent are one row")
	assert.Equal(t, "public.bookings", finding.Table.Rows[0].Cells[0], "the operator sees the actionable name")
	for _, row := range finding.Table.Rows {
		assert.NotContains(t, strings.Join(row.Cells, " "), "pg_toast", "TOAST names are unactionable in the table")
	}
	assert.Contains(t, finding.Debug, "pg_toast.pg_toast_16452", "but Debug names what contributed")
	assert.Contains(t, finding.Debug, "toast=1")
}

func TestFreezeAge_NeverVacuumedSizeIsUnknownNotZero(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.tableRows = []db.TableFreezeAgeRow{neverVacuumedRelation("public.fresh", xidWarn)}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.NotNil(t, finding.Table)
	assert.Equal(t, "unknown", finding.Table.Rows[0].Cells[4])
	assert.NotContains(t, finding.Table.Rows[0].Cells[4], "0B")
	assert.Equal(t, "never", finding.Table.Rows[0].Cells[5])
}

func TestFreezeAge_MultipleColumnReplacesPercentage(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	row := relation("public.bookings", 420_000_000) // 2.1x the 200M trigger
	row.Relpages = 54_000_000
	row.SizeBytesEst = 54_000_000 * 8192
	row.TotalAboveFloor = 1847
	queryer.tableRows = []db.TableFreezeAgeRow{row}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.NotNil(t, finding.Table)
	assert.Equal(t, "Multiple", finding.Table.Headers[2])
	assert.Equal(t, "2.1×", finding.Table.Rows[0].Cells[2])
	assert.Equal(t, "200.0M", finding.Table.Rows[0].Cells[3])
	for _, cell := range finding.Table.Rows[0].Cells {
		assert.NotContains(t, cell, "%", "a percentage of the trigger has no dynamic range")
	}
	assert.Contains(t, finding.Details, "public.bookings")
	assert.Contains(t, finding.Details, "is at 2.1× its anti-wraparound trigger")
	assert.Contains(t, finding.Details, "1847 target(s) above the reporting floor, worst 1 shown")
}

func TestFreezeAge_ReloptionLowersEffectiveTrigger(t *testing.T) {
	t.Parallel()

	// 210M is the healthy sawtooth at the 200M GUC (WARN at 400M) but past WARN
	// once a reloption lowers the trigger to 100M (WARN at 200M).
	const age = int64(210_000_000)

	withoutOverride := healthy()
	withoutOverride.tableRows = []db.TableFreezeAgeRow{relation("public.bookings", age)}
	assert.Equal(t, check.SeverityPass, findingByID(t, run(t, withoutOverride), idTableFreeze).Severity)

	withOverride := healthy()
	withOverride.tableRows = []db.TableFreezeAgeRow{loweredTriggerRelation("public.bookings", age, 100_000_000)}

	finding := findingByID(t, run(t, withOverride), idTableFreeze)
	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	assert.Equal(t, "100.0M (reloption)", finding.Table.Rows[0].Cells[3])
	assert.Equal(t, "2.1×", finding.Table.Rows[0].Cells[2])
}

func TestFreezeAge_MultixactReloptionLowersEffectiveTrigger(t *testing.T) {
	t.Parallel()

	// 210M is harmless at the 400M GUC (WARN at 800M) but past WARN once a
	// reloption lowers the trigger to 100M (WARN at 200M).
	const age = int64(210_000_000)

	withoutOverride := healthy()
	withoutOverride.tableRows = []db.TableFreezeAgeRow{relationWithMultixact("public.bookings", age)}
	assert.Equal(t, check.SeverityPass, findingByID(t, run(t, withoutOverride), idTableMultixact).Severity)

	withOverride := healthy()
	withOverride.tableRows = []db.TableFreezeAgeRow{
		loweredMultixactTriggerRelation("public.bookings", age, 100_000_000),
	}

	finding := findingByID(t, run(t, withOverride), idTableMultixact)
	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	assert.Equal(t, "100.0M (reloption)", finding.Table.Rows[0].Cells[3])
	assert.Contains(t, finding.Details, "MultiXacts against")
}

func TestFreezeAge_MixedSeverityAggregates(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRow: database("appdb", xidWarn),
		tableRows: []db.TableFreezeAgeRow{
			relation("public.bookings", xidFail),
			relation("public.audits", xidWarn),
		},
	}

	report := run(t, queryer)

	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Equal(t, check.SeverityWarn, findingByID(t, report, idDatabaseFreeze).Severity)
	assert.Equal(t, check.SeverityFail, findingByID(t, report, idTableFreeze).Severity)
	assert.Equal(t, check.SeverityPass, findingByID(t, report, idTableMultixact).Severity)
	assert.Len(t, findingByID(t, report, idTableFreeze).Table.Rows, 2)
}

// A high age is a "you are close to the cliff" signal; finding the transaction or
// slot pinning the xmin horizon is `houston dba xmin`, so the remediation text has
// to point there rather than guess at PIDs.
func TestFreezeAge_FlaggedDetailsPointAtInvestigation(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRow = database("appdb", xidWarn)

	finding := findingByID(t, run(t, queryer), idDatabaseFreeze)

	require.Equal(t, check.SeverityWarn, finding.Severity)
	assert.Contains(t, finding.Details, "houston dba xmin")
}

func TestFreezeAge_DatabaseQueryError(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbErr = fmt.Errorf("connection refused")

	_, err := freezeage.New(queryer).Check(context.Background())

	require.ErrorContains(t, err, "freeze-age")
	require.ErrorContains(t, err, "databases")
}

func TestFreezeAge_TableQueryError(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.tableErr = fmt.Errorf("statement timeout")

	_, err := freezeage.New(queryer).Check(context.Background())

	require.ErrorContains(t, err, "freeze-age")
	require.ErrorContains(t, err, "tables")
}

func TestFreezeAge_Metadata(t *testing.T) {
	t.Parallel()

	metadata := freezeage.Metadata()

	assert.Equal(t, "freeze-age", metadata.CheckID)
	assert.Equal(t, "Transaction ID Freeze Age", metadata.Name)
	assert.Equal(t, check.CategoryVacuum, metadata.Category)
	assert.NotEmpty(t, metadata.Description)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
	assert.Contains(t, metadata.Description, "transaction ID")
	assert.Contains(t, metadata.Description, "wraparound")
	assert.Equal(t, metadata, freezeage.New(healthy()).Metadata())

	for _, name := range []string{"DatabaseFreezeAge", "TableFreezeAge"} {
		assert.Contains(t, metadata.SQL, "-- name: "+name)
	}
	assert.NotContains(t, metadata.SQL, "XminHorizonBlockers", "live pin investigation belongs to houston dba xmin")
	// The floor must track the WARN multiple or relations are filtered out before
	// Go can classify them.
	assert.Contains(t, metadata.SQL, "r.freeze_age >= 2 * r.effective_freeze_max_age")
	assert.Contains(t, metadata.SQL, "r.multixact_age >= 2 * r.effective_multixact_freeze_max_age")
	// Only the connected database is reported.
	assert.Contains(t, metadata.SQL, "WHERE d.datname = current_database()")
}
