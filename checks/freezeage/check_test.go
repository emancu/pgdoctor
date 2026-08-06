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

// Stock GUC triggers. WARN is 2x the trigger and FAIL min(4x, failsafe age), so an
// age just below the trigger is the healthy peak of the sawtooth, not a warning.
const (
	defaultTrigger   = int64(200_000_000)
	defaultMxTrigger = int64(400_000_000)
	failsafeAge      = int64(1_600_000_000)
)

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
		EffectiveFreezeMaxAge:          defaultTrigger,
		EffectiveMultixactFreezeMaxAge: defaultMxTrigger,
		FailsafeAge:                    failsafeAge,
		MultixactFailsafeAge:           failsafeAge,
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
	require.Len(t, report.Results, 4)
	for _, id := range []string{idDatabaseFreeze, idTableFreeze, idDatabaseMultixact, idTableMultixact} {
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
func TestFreezeAge_ReloptionLowersEffectiveTrigger(t *testing.T) {
	t.Parallel()

	// 210M: healthy at both GUCs (WARN 400M / 800M), past WARN at a 100M override.
	const age = int64(210_000_000)

	xid, xidLowered := relation("public.bookings", age), relation("public.bookings", age)
	xidLowered.EffectiveFreezeMaxAge, xidLowered.XidReloption = 100_000_000, 100_000_000

	mx, mxLowered := relationWithMultixact("public.bookings", age), relationWithMultixact("public.bookings", age)
	mxLowered.EffectiveMultixactFreezeMaxAge, mxLowered.MultixactReloption = 100_000_000, 100_000_000

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

func TestFreezeAge_QueryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, wantIn string
		fail         func(*mockQueryer)
	}{
		{name: "database", wantIn: "databases", fail: func(q *mockQueryer) { q.dbErr = fmt.Errorf("connection refused") }},
		{name: "table", wantIn: "tables", fail: func(q *mockQueryer) { q.tableErr = fmt.Errorf("statement timeout") }},
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

	for _, name := range []string{"DatabaseFreezeAge", "TableFreezeAge"} {
		assert.Contains(t, metadata.SQL, "-- name: "+name)
	}
	assert.NotContains(t, metadata.SQL, "XminHorizonBlockers", "live pin investigation belongs to houston dba xmin")
	// The floor must track the WARN multiple or relations are filtered out before
	// Go can classify them.
	assert.Contains(t, metadata.SQL, "r.freeze_age >= 2 * r.effective_freeze_max_age")
	assert.Contains(t, metadata.SQL, "r.multixact_age >= 2 * r.effective_multixact_freeze_max_age")
	assert.Contains(t, metadata.SQL, "WHERE d.datname = current_database()")
}
