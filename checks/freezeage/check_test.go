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
	idHorizonBlockers   = "horizon-blockers"
	idDoomLoop          = "doom-loop"
)

// Thresholds at stock GUCs, restated here so a formula change has to be
// deliberate. Both counters use the same derivation:
//
//	WARN = min(vacuum_[multixact_]freeze_table_age, 0.95 * effective trigger)
//	FAIL = min(4 * effective trigger, vacuum_[multixact_]failsafe_age)
//
// MultiXact WARN lands on 150M — vacuum_multixact_freeze_table_age — not on the
// 400M trigger, exactly like the XID path.
const (
	xidWarn        = int64(150_000_000)
	xidFail        = int64(800_000_000)
	mxidWarn       = int64(150_000_000)
	mxidFail       = int64(1_600_000_000)
	blockerWarn    = int64(50_000_000)
	blockerFail    = int64(200_000_000)
	defaultTrigger = int64(200_000_000)
	defaultMxTrig  = int64(400_000_000)
)

// pgtype helper constructors.

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func pgInt8(i int64) pgtype.Int8 { return pgtype.Int8{Int64: i, Valid: true} }

func pgBool(b bool) pgtype.Bool { return pgtype.Bool{Bool: b, Valid: true} }

func pgTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

type mockQueryer struct {
	dbRows      []db.DatabaseFreezeAgeRow
	tableRows   []db.TableFreezeAgeRow
	blockerRows []db.XminHorizonBlockersRow
	dbErr       error
	tableErr    error
	blockerErr  error
}

func (m *mockQueryer) DatabaseFreezeAge(context.Context) ([]db.DatabaseFreezeAgeRow, error) {
	if m.dbErr != nil {
		return nil, m.dbErr
	}
	return m.dbRows, nil
}

func (m *mockQueryer) TableFreezeAge(context.Context) ([]db.TableFreezeAgeRow, error) {
	if m.tableErr != nil {
		return nil, m.tableErr
	}
	return m.tableRows, nil
}

func (m *mockQueryer) XminHorizonBlockers(context.Context) ([]db.XminHorizonBlockersRow, error) {
	if m.blockerErr != nil {
		return nil, m.blockerErr
	}
	return m.blockerRows, nil
}

// Scenario builders: databases.

// database is a connectable, current database at the given XID age with stock GUCs.
func database(name string, freezeAge int64) db.DatabaseFreezeAgeRow {
	return db.DatabaseFreezeAgeRow{
		DatabaseName:            name,
		Datallowconn:            true,
		IsCurrentDatabase:       true,
		FrozenXid:               "1000",
		FreezeAge:               freezeAge,
		MinMultixactID:          "1",
		MultixactAge:            0,
		FreezeMaxAge:            pgInt8(defaultTrigger),
		MultixactFreezeMaxAge:   pgInt8(defaultMxTrig),
		FreezeTableAge:          pgInt8(150_000_000),
		MultixactFreezeTableAge: pgInt8(150_000_000),
		FreezeMinAge:            pgInt8(50_000_000),
		FailsafeAge:             pgInt8(1_600_000_000),
		MultixactFailsafeAge:    pgInt8(1_600_000_000),
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

// noConnectDatabase is template0-shaped: counted towards the cluster XID limit but
// not fixable with a plain VACUUM.
func noConnectDatabase(name string, freezeAge int64) db.DatabaseFreezeAgeRow {
	row := database(name, freezeAge)
	row.Datallowconn = false
	row.IsCurrentDatabase = false
	return row
}

// Scenario builders: relations.

func relation(name string, freezeAge int64) db.TableFreezeAgeRow {
	return db.TableFreezeAgeRow{
		TableName:                      name,
		VacuumTarget:                   name,
		Relkind:                        "r",
		Relpages:                       100,
		SizeBytesEst:                   100 * 8192,
		FrozenXid:                      "1000",
		FreezeAge:                      freezeAge,
		MinMultixactID:                 "1",
		MultixactAge:                   0,
		EffectiveFreezeMaxAge:          pgInt8(defaultTrigger),
		EffectiveMultixactFreezeMaxAge: pgInt8(defaultMxTrig),
		FreezeTableAge:                 pgInt8(150_000_000),
		MultixactFreezeTableAge:        pgInt8(150_000_000),
		FailsafeAge:                    pgInt8(1_600_000_000),
		MultixactFailsafeAge:           pgInt8(1_600_000_000),
		XidReloption:                   pgInt8(0),
		MultixactReloption:             pgInt8(0),
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

// neverUsedMultixactRelation mirrors relminmxid = 0 after the SQL guard.
func neverUsedMultixactRelation(name string) db.TableFreezeAgeRow {
	row := relation(name, 1000)
	row.MinMultixactID = "0"
	row.MultixactAge = 0
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

// toastRelation is what an aging TOAST relation looks like: vacuumed through its
// parent, never by its own name.
func toastRelation(toastName, parent string, freezeAge int64) db.TableFreezeAgeRow {
	row := relation(toastName, freezeAge)
	row.Relkind = "t"
	row.VacuumTarget = parent
	return row
}

// loweredTriggerRelation carries a per-table autovacuum_freeze_max_age reloption,
// which can only lower the effective trigger.
func loweredTriggerRelation(name string, freezeAge, reloption int64) db.TableFreezeAgeRow {
	row := relation(name, freezeAge)
	row.EffectiveFreezeMaxAge = pgInt8(reloption)
	row.XidReloption = pgInt8(reloption)
	return row
}

// loweredMultixactTriggerRelation is the MultiXact mirror: a per-table
// autovacuum_multixact_freeze_max_age reloption lowering the effective trigger.
func loweredMultixactTriggerRelation(name string, multixactAge, reloption int64) db.TableFreezeAgeRow {
	row := relationWithMultixact(name, multixactAge)
	row.EffectiveMultixactFreezeMaxAge = pgInt8(reloption)
	row.MultixactReloption = pgInt8(reloption)
	return row
}

// Scenario builders: horizon blockers.

func blockerRow(source, object, pinKind string, age int64) db.XminHorizonBlockersRow {
	return db.XminHorizonBlockersRow{
		Source:             pgText(source),
		Object:             pgText(object),
		PinKind:            pgText(pinKind),
		PinnedXid:          pgText("12345"),
		PinnedXidAge:       pgInt8(age),
		HorizonScope:       pgText("data+catalog"),
		DurationSeconds:    pgInt8(3600),
		DurationEstimated:  pgBool(false),
		PrivilegeMasked:    pgBool(false),
		Inactive:           pgBool(false),
		Details:            pgText("appdb app_user worker [idle in transaction] SELECT 1"),
		MaxSlotWalKeepSize: pgText("-1"),
	}
}

func backendPin(pid string, age int64) db.XminHorizonBlockersRow {
	return blockerRow("backend", pid, "backend_xmin", age)
}

func autovacuumPin(pid string, age int64) db.XminHorizonBlockersRow {
	row := blockerRow("autovacuum", pid, "backend_xmin", age)
	row.Details = pgText("appdb postgres [active] autovacuum: VACUUM public.bookings (to prevent wraparound)")
	return row
}

func standbyFeedbackPin(pid string, age int64) db.XminHorizonBlockersRow {
	return blockerRow("standby_feedback", pid, "backend_xmin", age)
}

func concurrentIndexPin(pid string, age int64) db.XminHorizonBlockersRow {
	row := blockerRow("backend", pid, "backend_xmin", age)
	row.Details = pgText("appdb app_user psql [active] CREATE INDEX CONCURRENTLY idx_bookings_x ON public.bookings (x)")
	return row
}

func slotPin(name string, age int64, active bool) db.XminHorizonBlockersRow {
	row := blockerRow("logical_slot", name, "slot_catalog_xmin", age)
	row.HorizonScope = pgText("catalog")
	row.DurationSeconds = pgtype.Int8{}
	row.Inactive = pgBool(!active)
	state := "active pid=42"
	if !active {
		state = "INACTIVE"
	}
	row.Details = pgText("appdb pgoutput " + state + " wal_status=reserved")
	return row
}

func preparedXactPin(gid string, age int64) db.XminHorizonBlockersRow {
	return blockerRow("prepared_xact", gid, "prepared_xid", age)
}

func maskedPin(pid string, age int64) db.XminHorizonBlockersRow {
	row := blockerRow("backend", pid, "backend_xmin", age)
	row.PrivilegeMasked = pgBool(true)
	row.DurationEstimated = pgBool(true)
	row.Details = pgText("<insufficient privilege>")
	return row
}

// Helpers.

// healthy is a queryer with nothing wrong anywhere, so a test can vary one axis.
func healthy() *mockQueryer {
	return &mockQueryer{
		dbRows:      []db.DatabaseFreezeAgeRow{database("appdb", 1000)},
		tableRows:   []db.TableFreezeAgeRow{},
		blockerRows: []db.XminHorizonBlockersRow{},
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

func TestFreezeAge_AllSubchecksReported(t *testing.T) {
	t.Parallel()

	report := run(t, healthy())

	require.Len(t, report.Results, 6)
	for _, id := range []string{
		idDatabaseFreeze, idTableFreeze, idDatabaseMultixact,
		idTableMultixact, idHorizonBlockers, idDoomLoop,
	} {
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
		{name: "below aggressive scan threshold", age: xidWarn - 1, severity: check.SeverityPass},
		{name: "at aggressive scan threshold", age: xidWarn, severity: check.SeverityWarn},
		{name: "below 4x trigger", age: xidFail - 1, severity: check.SeverityWarn},
		{name: "at 4x trigger", age: xidFail, severity: check.SeverityFail},
		{name: "saturated age", age: 2_147_483_647, severity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.dbRows = []db.DatabaseFreezeAgeRow{database("appdb", tt.age)}

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
		{name: "below aggressive scan threshold", age: mxidWarn - 1, severity: check.SeverityPass},
		{name: "at aggressive scan threshold", age: mxidWarn, severity: check.SeverityWarn},
		{name: "at the trigger itself", age: defaultMxTrig, severity: check.SeverityWarn},
		{name: "below multixact failsafe", age: mxidFail - 1, severity: check.SeverityWarn},
		{name: "at multixact failsafe", age: mxidFail, severity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.dbRows = []db.DatabaseFreezeAgeRow{databaseWithMultixact("appdb", tt.age)}

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
		{name: "below aggressive scan threshold", age: xidWarn - 1, severity: check.SeverityPass},
		{name: "at aggressive scan threshold", age: xidWarn, severity: check.SeverityWarn},
		{name: "below 4x trigger", age: xidFail - 1, severity: check.SeverityWarn},
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
		{name: "below aggressive scan threshold", age: mxidWarn - 1, severity: check.SeverityPass},
		{name: "at aggressive scan threshold", age: mxidWarn, severity: check.SeverityWarn},
		{name: "at the trigger itself", age: defaultMxTrig, severity: check.SeverityWarn},
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
	queryer.dbRows = []db.DatabaseFreezeAgeRow{neverUsedMultixactDatabase("appdb")}
	queryer.tableRows = []db.TableFreezeAgeRow{neverUsedMultixactRelation("public.bookings")}

	report := run(t, queryer)

	assert.Equal(t, check.SeverityPass, findingByID(t, report, idDatabaseMultixact).Severity)
	assert.Equal(t, check.SeverityPass, findingByID(t, report, idTableMultixact).Severity)
	assert.Equal(t, check.SeverityPass, report.Severity)
}

func TestFreezeAge_NoConnectDatabaseIsReportedAndLabelled(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRows = []db.DatabaseFreezeAgeRow{
		database("appdb", 1000),
		noConnectDatabase("template0", xidWarn+1),
	}

	finding := findingByID(t, run(t, queryer), idDatabaseFreeze)

	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	require.Len(t, finding.Table.Rows, 1)
	assert.Equal(t, "template0", finding.Table.Rows[0].Cells[0])
	assert.Contains(t, finding.Table.Rows[0].Cells[4], "plain VACUUM cannot fix")
	assert.Contains(t, finding.Details, "template0 is 50.0M XIDs from its anti-wraparound trigger")
	assert.Contains(t, finding.Details, "ALLOW_CONNECTIONS true")
	assert.Contains(t, finding.Details, "AWS support case")
}

func TestFreezeAge_NoConnectDatabaseVisibleWhenHealthy(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRows = []db.DatabaseFreezeAgeRow{
		database("appdb", 1000),
		noConnectDatabase("template0", 120_000_000),
	}

	finding := findingByID(t, run(t, queryer), idDatabaseFreeze)

	require.Equal(t, check.SeverityPass, finding.Severity)
	assert.Contains(t, finding.Details, "template0", "no-connect databases must never be hidden")
	assert.Contains(t, finding.Details, "2 database(s)")
}

func TestFreezeAge_ToastParentIsTheVacuumTarget(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.tableRows = []db.TableFreezeAgeRow{
		toastRelation("pg_toast.pg_toast_16452", "public.bookings", xidWarn),
	}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	row := finding.Table.Rows[0]
	assert.Equal(t, "pg_toast.pg_toast_16452", row.Cells[0], "raw TOAST relname stays visible")
	assert.Equal(t, "public.bookings", row.Cells[6], "vacuum target is the parent")
	assert.Contains(t, finding.Debug, "TOAST relation - vacuum its parent public.bookings")
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

func TestFreezeAge_ReloptionLowersEffectiveTrigger(t *testing.T) {
	t.Parallel()

	// 96M is harmless at the 200M GUC (WARN at 150M) but past WARN once a
	// reloption lowers the trigger to 100M (WARN at 95M).
	const age = int64(96_000_000)

	withoutOverride := healthy()
	withoutOverride.tableRows = []db.TableFreezeAgeRow{relation("public.bookings", age)}
	assert.Equal(t, check.SeverityPass, findingByID(t, run(t, withoutOverride), idTableFreeze).Severity)

	withOverride := healthy()
	withOverride.tableRows = []db.TableFreezeAgeRow{
		loweredTriggerRelation("public.bookings", age, 100_000_000),
	}

	finding := findingByID(t, run(t, withOverride), idTableFreeze)
	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	assert.Contains(t, finding.Table.Rows[0].Cells[3], "of 100.0M")
	assert.Contains(t, finding.Table.Rows[0].Cells[3], "reloption")
}

func TestFreezeAge_MultixactReloptionLowersEffectiveTrigger(t *testing.T) {
	t.Parallel()

	// 96M is harmless at the 400M GUC (WARN at 150M, the multixact
	// aggressive-scan point) but past WARN once a reloption lowers the trigger to
	// 100M (WARN at 0.95 * 100M = 95M).
	const age = int64(96_000_000)

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
	assert.Contains(t, finding.Table.Rows[0].Cells[3], "of 100.0M")
	assert.Contains(t, finding.Table.Rows[0].Cells[3], "reloption")
	assert.Contains(t, finding.Details, "MultiXacts from its anti-wraparound trigger")
}

func TestFreezeAge_HeadlineLeadsWithHeadroom(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	row := relation("public.bookings", 169_000_000)
	row.Relpages = 54_000_000 // ~412GiB at 8KiB pages
	row.SizeBytesEst = 54_000_000 * 8192
	row.TotalAboveFloor = 1847
	queryer.tableRows = []db.TableFreezeAgeRow{row}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	assert.Contains(t, finding.Details, "public.bookings")
	assert.Contains(t, finding.Details, "31.0M XIDs from its anti-wraparound trigger")
	assert.Contains(t, finding.Details, "(85% consumed)")
	assert.Contains(t, finding.Details, "1847 relation(s) above the reporting floor, worst 1 shown")
}

func TestFreezeAge_PastTriggerHeadroomIsNotNegative(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.tableRows = []db.TableFreezeAgeRow{relation("public.bookings", 250_000_000)}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.NotNil(t, finding.Table)
	assert.Equal(t, "PAST trigger", finding.Table.Rows[0].Cells[2])
	assert.Contains(t, finding.Details, "PAST its anti-wraparound trigger")
	assert.Contains(t, finding.Table.Rows[0].Cells[3], "125% of 200.0M")
}

func TestFreezeAge_BlockerBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		row      db.XminHorizonBlockersRow
		severity check.Severity
	}{
		{
			name:     "young pin is triage context only",
			row:      backendPin("4242", blockerWarn-1),
			severity: check.SeverityInfo,
		},
		{
			name:     "at vacuum_freeze_min_age",
			row:      backendPin("4242", blockerWarn),
			severity: check.SeverityWarn,
		},
		{
			name:     "below trigger",
			row:      backendPin("4242", blockerFail-1),
			severity: check.SeverityWarn,
		},
		{
			name:     "at trigger",
			row:      backendPin("4242", blockerFail),
			severity: check.SeverityFail,
		},
		{
			name:     "prepared transaction at trigger",
			row:      preparedXactPin("stuck-gid", blockerFail),
			severity: check.SeverityFail,
		},
		{
			name:     "autovacuum is capped at INFO even when ancient",
			row:      autovacuumPin("99", 900_000_000),
			severity: check.SeverityInfo,
		},
		{
			name:     "standby feedback is capped at WARN",
			row:      standbyFeedbackPin("77", 900_000_000),
			severity: check.SeverityWarn,
		},
		{
			name:     "concurrent index build is capped at WARN",
			row:      concurrentIndexPin("88", 900_000_000),
			severity: check.SeverityWarn,
		},
		{
			name:     "inactive slot fails at any age",
			row:      slotPin("debezium", 1_000, false),
			severity: check.SeverityFail,
		},
		{
			name:     "active slot below the gate is context",
			row:      slotPin("debezium", 1_000, true),
			severity: check.SeverityInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.blockerRows = []db.XminHorizonBlockersRow{tt.row}

			finding := findingByID(t, run(t, queryer), idHorizonBlockers)
			assert.Equal(t, tt.severity, finding.Severity)
			require.NotNil(t, finding.Table)
			require.Len(t, finding.Table.Rows, 1)
			assert.Equal(t, tt.severity, finding.Table.Rows[0].Severity)
		})
	}
}

func TestFreezeAge_InactiveSlotCrossReferencesWalKeepSize(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.blockerRows = []db.XminHorizonBlockersRow{slotPin("debezium_cdc", 1_000, false)}

	finding := findingByID(t, run(t, queryer), idHorizonBlockers)

	require.Equal(t, check.SeverityFail, finding.Severity)
	assert.Contains(t, finding.Details, "debezium_cdc")
	assert.Contains(t, finding.Details, "INACTIVE")
	assert.Contains(t, finding.Details, "max_slot_wal_keep_size is -1")
	assert.Contains(t, finding.Details, "never self-invalidates")
	// Still below the trigger, so no doom loop is guaranteed yet.
	assert.Equal(t, check.SeverityPass, findingByID(t, run(t, queryer), idDoomLoop).Severity)
}

func TestFreezeAge_ReconciliationPointsAtVacuumThroughput(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRows = []db.DatabaseFreezeAgeRow{database("appdb", 300_000_000)}
	queryer.blockerRows = []db.XminHorizonBlockersRow{backendPin("4242", 10_000_000)}

	finding := findingByID(t, run(t, queryer), idHorizonBlockers)

	assert.Contains(t, finding.Details, "no live blocker explains the horizon")
	assert.Contains(t, finding.Details, "vacuum throughput or scheduling")
	assert.Contains(t, finding.Details, "Stop hunting for a PID")
}

func TestFreezeAge_ReconciliationWhenPinExplainsHorizon(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRows = []db.DatabaseFreezeAgeRow{database("appdb", 300_000_000)}
	queryer.blockerRows = []db.XminHorizonBlockersRow{backendPin("4242", 290_000_000)}

	finding := findingByID(t, run(t, queryer), idHorizonBlockers)

	assert.Contains(t, finding.Details, "the pin plausibly explains the horizon")
	assert.NotContains(t, finding.Details, "Stop hunting for a PID")
}

func TestFreezeAge_PrivilegeMaskingIsSurfaced(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.blockerRows = []db.XminHorizonBlockersRow{maskedPin("4242", 1_000)}

	finding := findingByID(t, run(t, queryer), idHorizonBlockers)

	assert.Contains(t, finding.Details, "privilege-masked")
	assert.Contains(t, finding.Details, "GRANT pg_monitor")
	assert.NotEqual(t, check.SeverityPass, finding.Severity, "masked visibility must not look healthy")
	require.NotNil(t, finding.Table)
	assert.Contains(t, finding.Table.Rows[0].Cells[5], "from backend_start",
		"a duration derived from backend_start overstates the pin and must say so")
}

func TestFreezeAge_NoBlockersStillReconciles(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRows = []db.DatabaseFreezeAgeRow{database("appdb", 300_000_000)}

	finding := findingByID(t, run(t, queryer), idHorizonBlockers)

	require.Equal(t, check.SeverityPass, finding.Severity)
	assert.Contains(t, finding.Details, "Nothing pins the xmin horizon")
	assert.Contains(t, finding.Details, "no live blocker explains the horizon")
}

func TestFreezeAge_DoomLoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		row         db.XminHorizonBlockersRow
		severity    check.Severity
		remediation string
	}{
		{
			name:     "pin below trigger",
			row:      backendPin("4242", blockerFail-1),
			severity: check.SeverityPass,
		},
		{
			name:        "backend at trigger",
			row:         backendPin("4242", blockerFail),
			severity:    check.SeverityFail,
			remediation: "SELECT pg_cancel_backend(4242); then, if it survives, SELECT pg_terminate_backend(4242);",
		},
		{
			name:        "logical slot at trigger",
			row:         slotPin("debezium_cdc", blockerFail, true),
			severity:    check.SeverityFail,
			remediation: "SELECT pg_drop_replication_slot('debezium_cdc');",
		},
		{
			name:        "prepared transaction at trigger",
			row:         preparedXactPin("stuck-gid", blockerFail),
			severity:    check.SeverityFail,
			remediation: "ROLLBACK PREPARED 'stuck-gid';",
		},
		{
			name:        "standby feedback at trigger",
			row:         standbyFeedbackPin("77", blockerFail),
			severity:    check.SeverityFail,
			remediation: "hot_standby_feedback",
		},
		{
			name:        "concurrent index build at trigger",
			row:         concurrentIndexPin("88", blockerFail),
			severity:    check.SeverityFail,
			remediation: "do not cancel blindly",
		},
		{
			name:     "autovacuum is never a doom loop cause",
			row:      autovacuumPin("99", 900_000_000),
			severity: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.blockerRows = []db.XminHorizonBlockersRow{tt.row}

			finding := findingByID(t, run(t, queryer), idDoomLoop)
			require.Equal(t, tt.severity, finding.Severity)

			if tt.remediation == "" {
				assert.Nil(t, finding.Table)
				return
			}

			require.NotNil(t, finding.Table)
			require.Len(t, finding.Table.Rows, 1)
			assert.Contains(t, finding.Table.Rows[0].Cells[3], tt.remediation)
			assert.Contains(t, finding.Details, "non-cancellable")
		})
	}
}

// Mass kills are their own incident. Every emitted command must name one object.
func TestFreezeAge_DoomLoopEmitsSinglePIDCommandsOnly(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.blockerRows = []db.XminHorizonBlockersRow{
		backendPin("4242", blockerFail),
		preparedXactPin("stuck-gid", blockerFail),
	}

	finding := findingByID(t, run(t, queryer), idDoomLoop)

	require.NotNil(t, finding.Table)
	for _, row := range finding.Table.Rows {
		assert.NotContains(t, strings.ToUpper(row.Cells[3]), "FROM PG_STAT_ACTIVITY",
			"never emit a set-valued kill")
	}
}

func TestFreezeAge_MixedSeverityAggregates(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			database("appdb", xidWarn),
			noConnectDatabase("template0", 1000),
		},
		tableRows: []db.TableFreezeAgeRow{
			relation("public.bookings", xidFail),
			relation("public.audits", xidWarn),
		},
		blockerRows: []db.XminHorizonBlockersRow{backendPin("4242", 1_000)},
	}

	report := run(t, queryer)

	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Equal(t, check.SeverityWarn, findingByID(t, report, idDatabaseFreeze).Severity)
	assert.Equal(t, check.SeverityFail, findingByID(t, report, idTableFreeze).Severity)
	assert.Equal(t, check.SeverityPass, findingByID(t, report, idTableMultixact).Severity)
	assert.Equal(t, check.SeverityInfo, findingByID(t, report, idHorizonBlockers).Severity)
	assert.Len(t, findingByID(t, report, idTableFreeze).Table.Rows, 2)
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

func TestFreezeAge_HorizonQueryError(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.blockerErr = fmt.Errorf("permission denied for view pg_replication_slots")

	_, err := freezeage.New(queryer).Check(context.Background())

	require.ErrorContains(t, err, "freeze-age")
	require.ErrorContains(t, err, "horizon")
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

	// The three queries the check depends on must all be embedded.
	for _, name := range []string{"DatabaseFreezeAge", "TableFreezeAge", "XminHorizonBlockers"} {
		assert.Contains(t, metadata.SQL, "-- name: "+name)
	}
}
