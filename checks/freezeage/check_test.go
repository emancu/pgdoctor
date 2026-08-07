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
	idHorizonPin        = "horizon-pin"
)

// Stock GUC triggers. WARN is 2x the trigger and FAIL min(4x, failsafe age), so an
// age just below the trigger is the healthy peak of the sawtooth, not a warning.
const (
	defaultTrigger   = int64(200_000_000)
	defaultMxTrigger = int64(400_000_000)
	failsafeAge      = int64(1_600_000_000)
)

func pgInt8(i int64) pgtype.Int8 { return pgtype.Int8{Int64: i, Valid: true} }

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func pgBool(b bool) pgtype.Bool { return pgtype.Bool{Bool: b, Valid: true} }

func pgTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

type mockQueryer struct {
	dbRow     db.DatabaseFreezeAgeRow
	tableRows []db.TableFreezeAgeRow
	pinRows   []db.HorizonPinsRow
	dbErr     error
	tableErr  error
	pinErr    error
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

func (m *mockQueryer) HorizonPins(context.Context) ([]db.HorizonPinsRow, error) {
	if m.pinErr != nil {
		return nil, m.pinErr
	}
	return m.pinRows, nil
}

// Scenario builders.

func database(name string, freezeAge int64) db.DatabaseFreezeAgeRow {
	return db.DatabaseFreezeAgeRow{
		DatabaseName:          name,
		FrozenXid:             "1000",
		FreezeAge:             freezeAge,
		MinMultixactID:        "1",
		FreezeMaxAge:          pgInt8(defaultTrigger),
		MultixactFreezeMaxAge: pgInt8(defaultMxTrigger),
		FailsafeAge:           pgInt8(failsafeAge),
		MultixactFailsafeAge:  pgInt8(failsafeAge),
	}
}

// databaseWithTrigger overrides autovacuum_freeze_max_age, whose GUC minimum is
// 100000 — well below the coincidence tolerance floor.
func databaseWithTrigger(name string, freezeAge, trigger int64) db.DatabaseFreezeAgeRow {
	row := database(name, freezeAge)
	row.FreezeMaxAge = pgInt8(trigger)

	return row
}

func databaseWithMultixact(name string, multixactAge int64) db.DatabaseFreezeAgeRow {
	row := database(name, 1000)
	row.MultixactAge = multixactAge
	return row
}

func relation(name string, freezeAge int64) db.TableFreezeAgeRow {
	return db.TableFreezeAgeRow{
		VacuumTarget:                   name,
		WorstRelation:                  name,
		GroupedRelations:               1,
		Relkind:                        "r",
		Relpages:                       100,
		SizeBytesEst:                   100 * 8192,
		FreezeAge:                      freezeAge,
		EffectiveFreezeMaxAge:          pgInt8(defaultTrigger),
		EffectiveMultixactFreezeMaxAge: pgInt8(defaultMxTrigger),
		FailsafeAge:                    pgInt8(failsafeAge),
		MultixactFailsafeAge:           pgInt8(failsafeAge),
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

// slot mirrors what HorizonPins returns for a replication slot: a logical slot
// normally pins catalog_xmin, a physical one xmin.
func slot(source, name, walStatus string, age int64, active bool) db.HorizonPinsRow {
	pinColumn, state := "slot_xmin", "inactive"
	if source == "logical_slot" {
		pinColumn = "slot_catalog_xmin"
	}
	if active {
		state = "active"
	}

	return db.HorizonPinsRow{
		Source:     pgText(source),
		ObjectName: pgText(name),
		PinColumn:  pgText(pinColumn),
		PinAge:     pgInt8(age),
		Active:     pgBool(active),
		WalStatus:  pgText(walStatus),
		Detail: pgText(fmt.Sprintf("%s slot %s, %s, WAL %s",
			strings.TrimSuffix(source, "_slot"), name, state, walStatus)),
	}
}

func activeLogicalSlot(name string, age int64) db.HorizonPinsRow {
	return slot("logical_slot", name, "reserved", age, true)
}

func activePhysicalSlot(name string, age int64) db.HorizonPinsRow {
	return slot("physical_slot", name, "reserved", age, true)
}

func preparedXact(gid string, age int64) db.HorizonPinsRow {
	return db.HorizonPinsRow{
		Source:     pgText("prepared_xact"),
		ObjectName: pgText(gid),
		PinColumn:  pgText("prepared_xid"),
		PinAge:     pgInt8(age),
		Active:     pgBool(true),
		WalStatus:  pgText("unknown"),
		Detail:     pgText(fmt.Sprintf("prepared transaction %s on appdb, owner app, prepared 3 days ago", gid)),
	}
}

func healthy() *mockQueryer {
	return &mockQueryer{dbRow: database("appdb", 1000), tableRows: []db.TableFreezeAgeRow{}}
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

// TestFreezeAge_Boundaries covers both clocks at both scopes. WARN is 2x the
// effective trigger and FAIL min(4x, failsafe age), so 4 x 400M caps at the
// multixact failsafe. An age at the trigger is the healthy peak of the sawtooth.
func TestFreezeAge_Boundaries(t *testing.T) {
	t.Parallel()

	scenarios := []struct {
		name    string
		finding string
		trigger int64
		fail    int64
		apply   func(*mockQueryer, int64)
	}{
		{
			name: "database xid", finding: idDatabaseFreeze, trigger: defaultTrigger, fail: 4 * defaultTrigger,
			apply: func(q *mockQueryer, age int64) { q.dbRow = database("appdb", age) },
		},
		{
			name: "database multixact", finding: idDatabaseMultixact, trigger: defaultMxTrigger, fail: failsafeAge,
			apply: func(q *mockQueryer, age int64) { q.dbRow = databaseWithMultixact("appdb", age) },
		},
		{
			name: "table xid", finding: idTableFreeze, trigger: defaultTrigger, fail: 4 * defaultTrigger,
			apply: func(q *mockQueryer, age int64) {
				q.tableRows = []db.TableFreezeAgeRow{relation("public.bookings", age)}
			},
		},
		{
			name: "table multixact", finding: idTableMultixact, trigger: defaultMxTrigger, fail: failsafeAge,
			apply: func(q *mockQueryer, age int64) {
				q.tableRows = []db.TableFreezeAgeRow{relationWithMultixact("public.bookings", age)}
			},
		},
	}

	for _, s := range scenarios {
		cases := []struct {
			name string
			age  int64
			want check.Severity
		}{
			{name: "at the trigger is the healthy sawtooth peak", age: s.trigger, want: check.SeverityPass},
			{name: "just below WARN", age: 2*s.trigger - 1, want: check.SeverityPass},
			{name: "at WARN", age: 2 * s.trigger, want: check.SeverityWarn},
			{name: "at FAIL", age: s.fail, want: check.SeverityFail},
			// age() saturates at 2^31-1, so thresholds are clamped at or below it.
			{name: "saturated age", age: 2_147_483_647, want: check.SeverityFail},
		}

		for _, tt := range cases {
			t.Run(s.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				queryer := healthy()
				s.apply(queryer, tt.age)

				finding := findingByID(t, run(t, queryer), s.finding)
				assert.Equal(t, tt.want, finding.Severity, "age=%d", tt.age)
			})
		}
	}
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
	require.Len(t, report.Results, 5)
	for _, id := range []string{idDatabaseFreeze, idTableFreeze, idDatabaseMultixact, idTableMultixact, idHorizonPin} {
		finding := findingByID(t, report, id)
		assert.Equal(t, check.SeverityPass, finding.Severity, "finding %s", id)
		assert.Nil(t, finding.Table, "finding %s must not render a table when healthy", id)
	}

	assert.Contains(t, findingByID(t, report, idDatabaseFreeze).Details, "1.0×")
	assert.Contains(t, findingByID(t, report, idDatabaseFreeze).Details, "normal sawtooth")
	assert.Contains(t, findingByID(t, report, idTableFreeze).Details, "normal sawtooth")
}

// A zero datminmxid/relminmxid must never FAIL: mxid_age('0'::xid) returns
// 2147483647 and the SQL guard is the only thing standing between that and a
// fabricated emergency.
func TestFreezeAge_ZeroMultixactIsNotAnEmergency(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.dbRow = databaseWithMultixact("appdb", 0)
	queryer.dbRow.MinMultixactID = "0"
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
		toastGroup("public.bookings", "pg_toast.pg_toast_16452", 2*defaultTrigger),
	}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)
	require.Len(t, finding.Table.Rows, 1, "the TOAST relation and its parent are one row")
	assert.Equal(t, "public.bookings", finding.Table.Rows[0].Cells[0], "the operator sees the actionable name")
	assert.NotContains(t, strings.Join(finding.Table.Rows[0].Cells, " "), "pg_toast",
		"TOAST names are unactionable in the table")
	assert.Contains(t, finding.Debug, "pg_toast.pg_toast_16452", "but Debug names what contributed")
	assert.Contains(t, finding.Debug, "toast=1")
}

func TestFreezeAge_NeverVacuumedSizeIsUnknownNotZero(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.tableRows = []db.TableFreezeAgeRow{neverVacuumedRelation("public.fresh", 2*defaultTrigger)}

	finding := findingByID(t, run(t, queryer), idTableFreeze)

	require.NotNil(t, finding.Table)
	assert.Equal(t, "unknown", finding.Table.Rows[0].Cells[4])
	assert.Equal(t, "never", finding.Table.Rows[0].Cells[5])
}

// The Multiple column replaced a percentage of the trigger, which read 99-100% on
// every healthy relation and had no dynamic range.
func TestFreezeAge_MultipleColumnAndTruncation(t *testing.T) {
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
		assert.NotContains(t, cell, "%")
	}
	assert.Contains(t, finding.Details, "public.bookings")
	assert.Contains(t, finding.Details, "is at 2.1× its anti-wraparound trigger")
	assert.Contains(t, finding.Details, "1847 target(s) above the reporting floor, worst 1 shown")
}

// A reloption can only lower the effective trigger, so an age that is the healthy
// sawtooth against the GUC becomes a WARN against the override.
// A trigger high enough that 2 x trigger exceeds the age() ceiling makes FAIL
// (capped at the failsafe) lower than WARN (clamped to the ceiling). Such a
// relation must still be classified, and the SQL floor must admit it — flooring
// at a raw 2 x trigger would put the cutoff at 2.4B, which age() never reaches.
func TestFreezeAge_FloorAdmitsClampedFail(t *testing.T) {
	t.Parallel()

	const highTrigger = int64(1_200_000_000) // WARN clamps to 2147483647, FAIL = 1.6B failsafe.

	row := relation("public.bookings", 1_700_000_000)
	row.EffectiveFreezeMaxAge = pgInt8(highTrigger)

	q := healthy()
	q.tableRows = []db.TableFreezeAgeRow{row}

	finding := findingByID(t, run(t, q), idTableFreeze)
	assert.Equal(t, check.SeverityFail, finding.Severity,
		"1.7B is past the 1.6B failsafe, so it is a FAIL even though WARN clamped above it")
}

func TestFreezeAge_ReloptionLowersEffectiveTrigger(t *testing.T) {
	t.Parallel()

	// 210M: healthy at both GUCs (WARN 400M / 800M), past WARN at a 100M override.
	const age = int64(210_000_000)

	xid, xidLowered := relation("public.bookings", age), relation("public.bookings", age)
	xidLowered.EffectiveFreezeMaxAge, xidLowered.XidReloption = pgInt8(100_000_000), pgInt8(100_000_000)

	mx, mxLowered := relationWithMultixact("public.bookings", age), relationWithMultixact("public.bookings", age)
	mxLowered.EffectiveMultixactFreezeMaxAge, mxLowered.MultixactReloption =
		pgInt8(100_000_000), pgInt8(100_000_000)

	tests := []struct {
		name, finding, unit string
		plain, lowered      db.TableFreezeAgeRow
	}{
		{name: "xid", finding: idTableFreeze, unit: "XIDs against", plain: xid, lowered: xidLowered},
		{name: "multixact", finding: idTableMultixact, unit: "MultiXacts against", plain: mx, lowered: mxLowered},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plain := healthy()
			plain.tableRows = []db.TableFreezeAgeRow{tt.plain}
			assert.Equal(t, check.SeverityPass, findingByID(t, run(t, plain), tt.finding).Severity)

			lowered := healthy()
			lowered.tableRows = []db.TableFreezeAgeRow{tt.lowered}

			finding := findingByID(t, run(t, lowered), tt.finding)
			require.Equal(t, check.SeverityWarn, finding.Severity)
			require.NotNil(t, finding.Table)
			assert.Equal(t, "100.0M (reloption)", finding.Table.Rows[0].Cells[3])
			assert.Equal(t, "2.1×", finding.Table.Rows[0].Cells[2])
			assert.Contains(t, finding.Details, tt.unit)
		})
	}
}

func TestFreezeAge_MixedSeverityAggregates(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRow: database("appdb", 2*defaultTrigger),
		tableRows: []db.TableFreezeAgeRow{
			relation("public.bookings", 4*defaultTrigger),
			relation("public.audits", 2*defaultTrigger),
		},
	}

	report := run(t, queryer)

	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Equal(t, check.SeverityWarn, findingByID(t, report, idDatabaseFreeze).Severity)
	assert.Equal(t, check.SeverityFail, findingByID(t, report, idTableFreeze).Severity)
	assert.Equal(t, check.SeverityPass, findingByID(t, report, idTableMultixact).Severity)
	assert.Len(t, findingByID(t, report, idTableFreeze).Table.Rows, 2)
	// Finding what pins the xmin horizon is `houston dba xmin`, not this check.
	assert.Contains(t, findingByID(t, report, idDatabaseFreeze).Details, "houston dba xmin")
}

// TestFreezeAge_HorizonPinHealthyProductionState is the case that matters most for
// this finding: the pin shape measured on a healthy production instance. Six ACTIVE
// slots hold a recent xmin while the database sits at the top of its sawtooth, and
// none of them is level with that age — PASS, and no table.
func TestFreezeAge_HorizonPinHealthyProductionState(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		// age(datfrozenxid) at 99.95% of the 200M trigger.
		dbRow: database("appdb", 199_900_000),
		pinRows: []db.HorizonPinsRow{
			activeLogicalSlot("debezium_bookings", 35_300),
			activeLogicalSlot("debezium_customers", 35_300),
			activeLogicalSlot("debezium_payments", 22_100),
			activePhysicalSlot("replica_1", 1_100),
			activePhysicalSlot("replica_2", 797),
			activePhysicalSlot("replica_3", 364),
		},
	}

	report := run(t, queryer)
	finding := findingByID(t, report, idHorizonPin)

	require.Equal(t, check.SeverityPass, finding.Severity, "active slots holding a recent xmin are the healthy shape")
	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Nil(t, finding.Table, "a PASS must not render a table")
	assert.Contains(t, finding.Details, "6 durable pin(s)")
	assert.Contains(t, finding.Details, "oldest 35.3K transactions")
	assert.Contains(t, finding.Details, "199.9M transactions")
	assert.NotContains(t, finding.Details, "tune it", "199.9M against a 200M trigger is the sawtooth peak")
	assert.NotContains(t, finding.Details, "drop")
}

func TestFreezeAge_HorizonPinNoPins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dbAge     int64
		wantIn    string
		wantNotIn string
	}{
		{
			name: "healthy age", dbAge: 1000,
			wantIn: "No replication slot or prepared transaction is pinning the xmin horizon", wantNotIn: "tune it",
		},
		{
			// The discriminator: nothing durable is holding the horizon, so the age is
			// throughput and no amount of killing will move it.
			name: "high age with nothing to kill", dbAge: 3 * defaultTrigger,
			wantIn: "this is autovacuum throughput, tune it", wantNotIn: "advance or drop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.dbRow = database("appdb", tt.dbAge)

			finding := findingByID(t, run(t, queryer), idHorizonPin)

			assert.Equal(t, check.SeverityPass, finding.Severity)
			assert.Nil(t, finding.Table)
			assert.Contains(t, finding.Details, tt.wantIn)
			assert.NotContains(t, finding.Details, tt.wantNotIn)
		})
	}
}

// An inactive slot never advances on its own, so invalidated WAL makes it pure
// liability at any pin age, while reserved WAL needs a floor: a consumer that
// reconnects in seconds is normal churn.
func TestFreezeAge_HorizonPinInactiveSlot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, walStatus string
		age             int64
		active          bool
		want            check.Severity
	}{
		{name: "lost WAL at a trivial pin age", walStatus: "lost", age: 5, want: check.SeverityFail},
		{name: "unreserved WAL at a trivial pin age", walStatus: "unreserved", age: 5, want: check.SeverityFail},
		{name: "reserved WAL below the floor", walStatus: "reserved", age: 999_999, want: check.SeverityPass},
		{name: "reserved WAL at the floor", walStatus: "reserved", age: 1_000_000, want: check.SeverityWarn},
		{name: "extended WAL at the floor", walStatus: "extended", age: 1_000_000, want: check.SeverityWarn},
		{name: "lost WAL but active is exempt", walStatus: "lost", age: 5, active: true, want: check.SeverityPass},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.pinRows = []db.HorizonPinsRow{slot("logical_slot", "debezium_bookings", tt.walStatus, tt.age, tt.active)}

			finding := findingByID(t, run(t, queryer), idHorizonPin)

			assert.Equal(t, tt.want, finding.Severity, "wal_status=%s age=%d active=%t", tt.walStatus, tt.age, tt.active)
		})
	}
}

// WARN at 1x the trigger, a full sawtooth period before the age itself warns:
// past that point every anti-wraparound vacuum completes without freezing past the
// pin. FAIL at min(4x trigger, failsafe age), which is 800M at stock GUCs.
func TestFreezeAge_HorizonPinAgeThresholds(t *testing.T) {
	t.Parallel()

	sources := []struct {
		name string
		row  func(int64) db.HorizonPinsRow
	}{
		{name: "active slot", row: func(age int64) db.HorizonPinsRow { return activeLogicalSlot("debezium_bookings", age) }},
		{name: "prepared xact", row: func(age int64) db.HorizonPinsRow { return preparedXact("gid-xa-42", age) }},
	}

	cases := []struct {
		name string
		age  int64
		want check.Severity
	}{
		{name: "just below the trigger", age: defaultTrigger - 1, want: check.SeverityPass},
		{name: "at the trigger", age: defaultTrigger, want: check.SeverityWarn},
		{name: "just below FAIL", age: 4*defaultTrigger - 1, want: check.SeverityWarn},
		{name: "at FAIL", age: 4 * defaultTrigger, want: check.SeverityFail},
	}

	for _, source := range sources {
		for _, tt := range cases {
			t.Run(source.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				queryer := healthy()
				queryer.pinRows = []db.HorizonPinsRow{source.row(tt.age)}

				finding := findingByID(t, run(t, queryer), idHorizonPin)

				assert.Equal(t, tt.want, finding.Severity, "age=%d", tt.age)
			})
		}
	}
}

// The database age only escalates this finding when a pin is level with it, which
// is the difference between "kill something" and "tune something".
func TestFreezeAge_HorizonPinCoincidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dbRow   db.DatabaseFreezeAgeRow
		pin     db.HorizonPinsRow
		want    check.Severity
		wantIn  string
		wantOut string
	}{
		{
			name:  "a pin level with the age is the cause",
			dbRow: database("appdb", 600_000_000),
			pin:   activeLogicalSlot("debezium_bookings", 595_000_000),
			want:  check.SeverityWarn, wantIn: "this pin is holding the horizon — advance or drop it",
			wantOut: "tune it",
		},
		{
			name:  "the same age with a young pin is throughput",
			dbRow: database("appdb", 600_000_000),
			pin:   activeLogicalSlot("debezium_bookings", 1_000),
			want:  check.SeverityPass, wantIn: "this is autovacuum throughput, tune it",
			wantOut: "advance or drop",
		},
		{
			// Coincidence changes the message, never the severity: a pin old enough to
			// be level with a WARN-level age has already crossed the 1x-trigger pin
			// threshold, so escalating on the age too would be redundant.
			name:  "coincidence alone does not escalate",
			dbRow: databaseWithTrigger("appdb", 10_000_000, 5_000_000),
			pin:   activeLogicalSlot("debezium_bookings", 1_000_000),
			want:  check.SeverityPass, wantIn: "durable pin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.dbRow = tt.dbRow
			queryer.pinRows = []db.HorizonPinsRow{tt.pin}

			finding := findingByID(t, run(t, queryer), idHorizonPin)

			require.Equal(t, tt.want, finding.Severity)
			assert.Contains(t, finding.Details, tt.wantIn)
			if tt.wantOut != "" {
				assert.NotContains(t, finding.Details, tt.wantOut)
			}
		})
	}
}

// Remediation is single-object by construction: a set-valued command over slots or
// prepared transactions is how an incident becomes an outage.
func TestFreezeAge_HorizonPinRemediation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		pin    db.HorizonPinsRow
		wantIn []string
	}{
		{
			name: "logical slot names one slot and defers the caveat to the README",
			pin:  slot("logical_slot", "debezium_bookings", "lost", 5, false),
			wantIn: []string{
				"SELECT pg_drop_replication_slot('debezium_bookings')",
				"see the README before dropping it",
			},
		},
		{
			name:   "physical slot names one slot",
			pin:    slot("physical_slot", "replica_1", "lost", 5, false),
			wantIn: []string{"SELECT pg_drop_replication_slot('replica_1')"},
		},
		{
			name:   "prepared transaction names its gid",
			pin:    preparedXact("gid-xa-42", 4*defaultTrigger),
			wantIn: []string{"ROLLBACK PREPARED 'gid-xa-42'"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			queryer.pinRows = []db.HorizonPinsRow{tt.pin}

			finding := findingByID(t, run(t, queryer), idHorizonPin)

			require.Equal(t, check.SeverityFail, finding.Severity)
			for _, want := range tt.wantIn {
				assert.Contains(t, finding.Details, want)
			}
			assert.NotContains(t, finding.Details, "XID", "user-facing prose says transactions")
			// The property this test exists for: never a set-valued command.
			for _, forbidden := range []string{"FROM pg_replication_slots", "FROM pg_prepared_xacts", "WHERE"} {
				assert.NotContains(t, finding.Details, forbidden, "remediation must name one object")
			}
		})
	}
}

// The table is deliberately narrow: four columns an engineer who is not a DBA can
// read, capped at the three oldest pins in brief output.
func TestFreezeAge_HorizonPinTable(t *testing.T) {
	t.Parallel()

	queryer := healthy()
	queryer.pinRows = []db.HorizonPinsRow{
		slot("logical_slot", "debezium_bookings", "lost", 900_000_000, false),
		activePhysicalSlot("replica_1", 300_000_000),
		preparedXact("gid-xa-42", 200_000),
		activeLogicalSlot("debezium_payments", 1_000),
	}

	finding := findingByID(t, run(t, queryer), idHorizonPin)

	require.Equal(t, check.SeverityFail, finding.Severity)
	require.NotNil(t, finding.Table)
	assert.Equal(t, []string{"Source", "Object", "Pin Age (transactions)", "Status"}, finding.Table.Headers)
	assert.Equal(t, 3, finding.Table.MaxRowsBrief, "brief output shows the three oldest, verbose shows all")
	require.Len(t, finding.Table.Rows, 4)
	assert.Equal(t,
		[]string{"logical slot", "debezium_bookings", "900.0M", "inactive, WAL lost"}, finding.Table.Rows[0].Cells)
	assert.Equal(t, check.SeverityFail, finding.Table.Rows[0].Severity)
	assert.Equal(t,
		[]string{"physical slot", "replica_1", "300.0M", "active, WAL reserved"}, finding.Table.Rows[1].Cells)
	assert.Equal(t,
		[]string{"prepared transaction", "gid-xa-42", "200.0K", "prepared, uncommitted"}, finding.Table.Rows[2].Cells)
	assert.Equal(t, check.SeverityPass, finding.Table.Rows[3].Severity)
	assert.Contains(t, finding.Debug, "coincident=")
}

func TestFreezeAge_QueryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, wantIn string
		fail         func(*mockQueryer)
	}{
		{name: "database", wantIn: "databases", fail: func(q *mockQueryer) { q.dbErr = fmt.Errorf("connection refused") }},
		{name: "table", wantIn: "tables", fail: func(q *mockQueryer) { q.tableErr = fmt.Errorf("statement timeout") }},
		{
			name: "horizon pins", wantIn: "horizon pins",
			fail: func(q *mockQueryer) { q.pinErr = fmt.Errorf("permission denied") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := healthy()
			tt.fail(queryer)

			_, err := freezeage.New(queryer).Check(context.Background())

			require.ErrorContains(t, err, "freeze-age")
			require.ErrorContains(t, err, tt.wantIn)
		})
	}
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

	for _, name := range []string{"DatabaseFreezeAge", "TableFreezeAge", "HorizonPins"} {
		assert.Contains(t, metadata.SQL, "-- name: "+name)
	}
	assert.NotContains(t, metadata.SQL, "XminHorizonBlockers", "live pin investigation belongs to houston dba xmin")
	// horizon-pin reads DURABLE pins only. Live state resolves itself between the
	// read and the report, which is why it belongs to houston dba xmin instead.
	for _, view := range []string{"pg_stat_activity", "pg_stat_progress_vacuum", "pg_locks"} {
		assert.NotContains(t, metadata.SQL, view, "live state belongs to houston dba xmin, not this check")
	}
	for _, view := range []string{"pg_catalog.pg_replication_slots", "pg_catalog.pg_prepared_xacts"} {
		assert.Contains(t, metadata.SQL, view)
	}
	assert.NotContains(t, metadata.SQL, "inactive_since", "inactive_since is PG17+ and PG14 is the floor")
	// The floor must be the LOWER of the WARN and FAIL thresholds Go applies, not
	// WARN alone: a high trigger clamps WARN to the age() ceiling while FAIL stays
	// at the failsafe, so a raw 2 * trigger floor would be unreachable and would
	// discard relations Go would FAIL. See TestFreezeAge_FloorAdmitsClampedFail.
	assert.Contains(t, metadata.SQL,
		"r.freeze_age >= least(2 * r.effective_freeze_max_age, r.failsafe_age, 2147483647)")
	assert.Contains(t, metadata.SQL, "2 * r.effective_multixact_freeze_max_age, r.multixact_failsafe_age, 2147483647")
	assert.Contains(t, metadata.SQL, "WHERE d.datname = current_database()")
}
