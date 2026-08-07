// Package freezeage implements checks for PostgreSQL transaction ID wraparound risk.
package freezeage

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

// Finding IDs.
const (
	findingDatabaseFreeze    = "database-freeze-age"
	findingTableFreeze       = "table-freeze-age"
	findingDatabaseMultixact = "database-multixact-age"
	findingTableMultixact    = "table-multixact-age"
)

const (
	// age() and mxid_age() saturate at 2^31-1, so no threshold above this is meaningful.
	maxAge = int64(2147483647)

	// Thresholds are multiples of the effective anti-wraparound trigger
	// (autovacuum_[multixact_]freeze_max_age), because a healthy relation's age is a
	// sawtooth whose PEAK is that trigger: a low-churn relation has no dead tuples,
	// so nothing vacuums it until the trigger fires, freezes it, and the age drops.
	// Sitting just below the trigger is therefore the normal top of the cycle, not a
	// problem. Age *exceeding* the trigger is the signal, because past that point
	// the non-cancellable anti-wraparound vacuum is running and should be pulling
	// the age back down.
	warnTriggerMultiplier = int64(2)

	// FAIL at 4x the trigger, capped at the counter's failsafe age — where the cost
	// delay is disabled and index vacuuming is skipped entirely. 2B is not a usable
	// anchor: the documented recovery from the hard stop is single-user mode, which
	// does not exist on RDS.
	failTriggerMultiplier = int64(4)
)

// Documented GUC defaults, used when a value is missing from pg_settings.
const (
	defaultFreezeMaxAge          = int64(200_000_000)
	defaultMultixactFreezeMaxAge = int64(400_000_000)
	defaultFailsafeAge           = int64(1_600_000_000)
	defaultMultixactFailsafeAge  = int64(1_600_000_000)
)

const unknownSize = "unknown"

type FreezeAgeQueries interface {
	DatabaseFreezeAge(context.Context) (db.DatabaseFreezeAgeRow, error)
	TableFreezeAge(context.Context) ([]db.TableFreezeAgeRow, error)
}

type checker struct {
	queries FreezeAgeQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryVacuum,
		CheckID:     "freeze-age",
		Name:        "Transaction ID Freeze Age",
		Description: "Monitors transaction ID age to prevent wraparound issues",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries FreezeAgeQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	dbRow, err := c.queries.DatabaseFreezeAge(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (databases): %w", check.CategoryVacuum, report.CheckID, err)
	}

	tableRows, err := c.queries.TableFreezeAge(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (tables): %w", check.CategoryVacuum, report.CheckID, err)
	}

	gucs := settingsFrom(dbRow)

	checkDatabaseAge(dbRow, gucs, report, xidCounter)
	checkDatabaseAge(dbRow, gucs, report, multixactCounter)
	checkTableAge(tableRows, report, xidCounter)
	checkTableAge(tableRows, report, multixactCounter)

	return report, nil
}

// settings holds the cluster GUCs the thresholds derive from. Each counter has its
// own trigger and its own failsafe age.
type settings struct {
	freezeMaxAge          int64
	multixactFreezeMaxAge int64
	failsafeAge           int64
	multixactFailsafeAge  int64
}

func settingsFrom(row db.DatabaseFreezeAgeRow) settings {
	return settings{
		freezeMaxAge:          orDefault(row.FreezeMaxAge, defaultFreezeMaxAge),
		multixactFreezeMaxAge: orDefault(row.MultixactFreezeMaxAge, defaultMultixactFreezeMaxAge),
		failsafeAge:           orDefault(row.FailsafeAge, defaultFailsafeAge),
		multixactFailsafeAge:  orDefault(row.MultixactFailsafeAge, defaultMultixactFailsafeAge),
	}
}

func orDefault(value pgtype.Int8, fallback int64) int64 {
	if !value.Valid || value.Int64 <= 0 {
		return fallback
	}
	return value.Int64
}

type thresholds struct {
	warn int64
	fail int64
}

func deriveThresholds(trigger, failsafeAge int64) thresholds {
	fail := trigger * failTriggerMultiplier
	if failsafeAge > 0 && failsafeAge < fail {
		fail = failsafeAge
	}
	return thresholds{warn: clampAge(trigger * warnTriggerMultiplier), fail: clampAge(fail)}
}

func clampAge(age int64) int64 {
	return min(age, maxAge)
}

func severityFor(age int64, t thresholds) check.Severity {
	switch {
	case age >= t.fail:
		return check.SeverityFail
	case age >= t.warn:
		return check.SeverityWarn
	default:
		return check.SeverityPass
	}
}

// counter is one of the two independent wraparound clocks. Both have their own
// counter, trigger and wall, so both get their own subchecks.
type counter struct {
	dbFindingID    string
	dbFindingName  string
	relFindingID   string
	relFindingName string
	unit           string
	dbAge          func(db.DatabaseFreezeAgeRow) int64
	trigger        func(settings) int64
	failsafe       func(settings) int64
	relAge         func(db.TableFreezeAgeRow) int64
	relTrigger     func(db.TableFreezeAgeRow) int64
	relFailsafe    func(db.TableFreezeAgeRow) int64
	relOverridden  func(db.TableFreezeAgeRow) bool
}

var xidCounter = counter{
	dbFindingID:    findingDatabaseFreeze,
	dbFindingName:  "Database Freeze Age",
	relFindingID:   findingTableFreeze,
	relFindingName: "Table Freeze Age",
	unit:           "XIDs",
	dbAge:          func(r db.DatabaseFreezeAgeRow) int64 { return r.FreezeAge },
	trigger:        func(s settings) int64 { return s.freezeMaxAge },
	failsafe:       func(s settings) int64 { return s.failsafeAge },
	relAge:         func(r db.TableFreezeAgeRow) int64 { return r.FreezeAge },
	relTrigger:     func(r db.TableFreezeAgeRow) int64 { return orDefault(r.EffectiveFreezeMaxAge, defaultFreezeMaxAge) },
	relFailsafe:    func(r db.TableFreezeAgeRow) int64 { return orDefault(r.FailsafeAge, defaultFailsafeAge) },
	relOverridden:  func(r db.TableFreezeAgeRow) bool { return r.XidReloption.Valid && r.XidReloption.Int64 > 0 },
}

var multixactCounter = counter{
	dbFindingID:    findingDatabaseMultixact,
	dbFindingName:  "Database MultiXact Age",
	relFindingID:   findingTableMultixact,
	relFindingName: "Table MultiXact Age",
	unit:           "MultiXacts",
	dbAge:          func(r db.DatabaseFreezeAgeRow) int64 { return r.MultixactAge },
	trigger:        func(s settings) int64 { return s.multixactFreezeMaxAge },
	failsafe:       func(s settings) int64 { return s.multixactFailsafeAge },
	relAge:         func(r db.TableFreezeAgeRow) int64 { return r.MultixactAge },
	relTrigger: func(r db.TableFreezeAgeRow) int64 {
		return orDefault(r.EffectiveMultixactFreezeMaxAge, defaultMultixactFreezeMaxAge)
	},
	relFailsafe: func(r db.TableFreezeAgeRow) int64 {
		return orDefault(r.MultixactFailsafeAge, defaultMultixactFailsafeAge)
	},
	relOverridden: func(r db.TableFreezeAgeRow) bool {
		return r.MultixactReloption.Valid && r.MultixactReloption.Int64 > 0
	},
}

// ageRow is a counter-agnostic view of one database or VACUUM target, so both
// clocks and both scopes share one renderer.
type ageRow struct {
	name       string
	age        int64
	trigger    int64
	overridden bool
	severity   check.Severity
	size       string
	lastVacuum string
}

// multiple is the headline number: 1x is the healthy peak of the sawtooth, 2x
// WARN, 4x FAIL. A percentage of the trigger read 99-100% on every healthy
// relation and looked like an emergency.
func (r ageRow) multiple() float64 {
	if r.trigger <= 0 {
		return 0
	}
	return float64(r.age) / float64(r.trigger)
}

func (r ageRow) multipleCell() string {
	return fmt.Sprintf("%.1f×", r.multiple())
}

func (r ageRow) triggerCell() string {
	cell := check.FormatNumber(r.trigger)
	if r.overridden {
		cell += " (reloption)"
	}
	return cell
}

func (r ageRow) headline(unit string) string {
	subject := r.name
	if r.size != "" {
		subject = fmt.Sprintf("%s (%s)", r.name, r.size)
	}
	return fmt.Sprintf(
		"%s is at %s its anti-wraparound trigger (%s %s against %s)",
		subject, r.multipleCell(), check.FormatNumber(r.age), unit, check.FormatNumber(r.trigger),
	)
}

// worst returns the row furthest past its own trigger. Rows arrive ranked by the
// higher of the two clocks, which is not necessarily this counter's order.
func worst(rows []ageRow) ageRow {
	worstRow := rows[0]
	for _, row := range rows[1:] {
		if row.multiple() > worstRow.multiple() {
			worstRow = row
		}
	}
	return worstRow
}

func checkDatabaseAge(row db.DatabaseFreezeAgeRow, s settings, report *check.Report, c counter) {
	trigger := c.trigger(s)
	limits := deriveThresholds(trigger, c.failsafe(s))
	item := ageRow{
		name:     row.DatabaseName,
		age:      c.dbAge(row),
		trigger:  trigger,
		severity: severityFor(c.dbAge(row), limits),
	}

	details := item.headline(c.unit)
	if item.severity == check.SeverityPass {
		details += fmt.Sprintf(
			"; ages near the trigger are the normal sawtooth, WARN starts at %s",
			check.FormatNumber(limits.warn),
		)
	} else {
		// This check answers "how close to the cliff"; finding the transaction, slot
		// or prepared xact pinning the xmin horizon is `houston dba xmin`.
		details += ". Autovacuum is not keeping up: find what pins the xmin horizon with `houston dba xmin`, " +
			"then look at vacuum throughput (workers, cost delay)"
	}

	report.AddFinding(check.Finding{
		ID:       c.dbFindingID,
		Name:     c.dbFindingName,
		Severity: item.severity,
		Details:  details,
		Debug:    fmt.Sprintf("age=%d trigger=%d warn=%d fail=%d", item.age, trigger, limits.warn, limits.fail),
	})
}

func checkTableAge(rows []db.TableFreezeAgeRow, report *check.Report, c counter) {
	var flagged []ageRow
	var debug []string
	maxSeverity := check.SeverityPass
	totalAboveFloor := int64(0)

	for _, row := range rows {
		totalAboveFloor = max(totalAboveFloor, row.TotalAboveFloor)

		trigger := c.relTrigger(row)
		age := c.relAge(row)
		severity := severityFor(age, deriveThresholds(trigger, c.relFailsafe(row)))
		if severity == check.SeverityPass {
			continue
		}

		item := ageRow{
			name:       row.VacuumTarget,
			age:        age,
			trigger:    trigger,
			overridden: c.relOverridden(row),
			severity:   severity,
			size:       formatSizeEstimate(row),
			lastVacuum: formatVacuumTime(row),
		}
		flagged = append(flagged, item)
		debug = append(debug, groupDebug(row, item))
		maxSeverity = max(maxSeverity, severity)
	}

	if len(flagged) == 0 {
		report.AddFinding(check.Finding{
			ID:       c.relFindingID,
			Name:     c.relFindingName,
			Severity: check.SeverityPass,
			Details: fmt.Sprintf(
				"No VACUUM target is past %dx its anti-wraparound trigger; ages near the trigger are the normal sawtooth",
				warnTriggerMultiplier,
			),
		})
		return
	}

	tableRows := make([]check.TableRow, 0, len(flagged))
	for _, item := range flagged {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				item.name,
				check.FormatNumber(item.age),
				item.multipleCell(),
				item.triggerCell(),
				item.size,
				item.lastVacuum,
			},
			Severity: item.severity,
		})
	}

	details := worst(flagged).headline(c.unit)
	if totalAboveFloor > int64(len(rows)) {
		details += fmt.Sprintf(". %d target(s) above the reporting floor, worst %d shown", totalAboveFloor, len(rows))
	}

	report.AddFinding(check.Finding{
		ID:       c.relFindingID,
		Name:     c.relFindingName,
		Severity: maxSeverity,
		Details:  details,
		Table: &check.Table{
			Headers: []string{"Vacuum Target", "Age", "Multiple", "Trigger", "Size (est)", "Last Vacuum"},
			Rows:    tableRows,
		},
		Debug: strings.Join(debug, "; "),
	})
}

// groupDebug names the relations behind a target, including the TOAST relation
// that may have contributed the worst age. TOAST names are unactionable on their
// own, so they stay out of the table.
func groupDebug(row db.TableFreezeAgeRow, item ageRow) string {
	return fmt.Sprintf(
		"%s: age=%d trigger=%d multiple=%.2f relations=%d toast=%d worst=%s relkind=%s",
		row.VacuumTarget, item.age, item.trigger, item.multiple(),
		row.GroupedRelations, row.ToastRelations, row.WorstRelation, row.Relkind,
	)
}

// formatSizeEstimate renders the lock-free relpages estimate. relpages is 0 until
// the first VACUUM/ANALYZE, and reporting "0 B" for a multi-terabyte table would
// be worse than admitting ignorance.
func formatSizeEstimate(row db.TableFreezeAgeRow) string {
	if row.Relpages <= 0 {
		return unknownSize
	}
	return check.FormatBytes(row.SizeBytesEst)
}

func formatVacuumTime(row db.TableFreezeAgeRow) string {
	if row.LastAutovacuum.Valid {
		return row.LastAutovacuum.Time.Format("2006-01-02 15:04")
	}
	if row.LastVacuum.Valid {
		return row.LastVacuum.Time.Format("2006-01-02 15:04") + " (manual)"
	}
	return "never"
}
