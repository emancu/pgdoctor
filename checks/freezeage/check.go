// Package freezeage implements checks for PostgreSQL transaction ID wraparound risk.
package freezeage

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
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
	findingHorizonBlockers   = "horizon-blockers"
	findingDoomLoop          = "doom-loop"
)

// Threshold derivation. Nothing here is a number we invented: every threshold is
// derived from the GUCs that change PostgreSQL's own behaviour.
const (
	// age() and mxid_age() saturate at 2^31-1, so no threshold above this is meaningful.
	maxAge = int64(2147483647)

	// PostgreSQL starts an *aggressive* (whole-relation) scan at
	// min(vacuum_freeze_table_age, 0.95 * autovacuum_freeze_max_age). That is the
	// point the database changes behaviour, so that is where we WARN. Healthy
	// relations oscillate between vacuum_freeze_min_age and this value.
	aggressiveScanFraction = 0.95

	// FAIL at 4x the effective trigger, capped at vacuum_failsafe_age — the real
	// cliff, where the cost delay is disabled and index vacuuming is skipped
	// entirely. 2B is not a usable anchor: the documented recovery from the hard
	// stop is single-user mode, which does not exist on RDS.
	failTriggerMultiplier = int64(4)

	// A pin younger than vacuum_freeze_min_age cannot block any freezing a vacuum
	// would have done anyway, so that is the floor for a WARN. At ~1,000 XID/s a
	// pin needs ~14 hours to reach 50M, which makes this a duration filter by
	// construction: a 5-minute transaction never qualifies.
	blockerWarnTriggerFraction = 0.25
)

// Documented GUC defaults, used when a value is missing from pg_settings.
const (
	defaultFreezeMaxAge          = int64(200_000_000)
	defaultMultixactFreezeMaxAge = int64(400_000_000)
	defaultFreezeTableAge        = int64(150_000_000)
	defaultFreezeMinAge          = int64(50_000_000)
	defaultFailsafeAge           = int64(1_600_000_000)
)

// Blocker source values produced by XminHorizonBlockers.
const (
	sourceAutovacuum      = "autovacuum"
	sourceStandbyFeedback = "standby_feedback"
	sourceLogicalSlot     = "logical_slot"
	sourcePhysicalSlot    = "physical_slot"
	sourcePreparedXact    = "prepared_xact"
)

const unknownSize = "unknown"

type FreezeAgeQueries interface {
	DatabaseFreezeAge(context.Context) ([]db.DatabaseFreezeAgeRow, error)
	TableFreezeAge(context.Context) ([]db.TableFreezeAgeRow, error)
	XminHorizonBlockers(context.Context) ([]db.XminHorizonBlockersRow, error)
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

	dbRows, err := c.queries.DatabaseFreezeAge(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (databases): %w", check.CategoryVacuum, report.CheckID, err)
	}

	tableRows, err := c.queries.TableFreezeAge(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (tables): %w", check.CategoryVacuum, report.CheckID, err)
	}

	blockerRows, err := c.queries.XminHorizonBlockers(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (horizon): %w", check.CategoryVacuum, report.CheckID, err)
	}

	gucs := settingsFrom(dbRows)

	checkDatabaseAge(dbRows, report, xidCounter)
	checkDatabaseAge(dbRows, report, multixactCounter)
	checkTableAge(tableRows, report, xidCounter)
	checkTableAge(tableRows, report, multixactCounter)
	checkHorizonBlockers(blockerRows, gucs, currentDatabaseAge(dbRows), report)
	checkDoomLoop(blockerRows, gucs, report)

	return report, nil
}

// settings holds the cluster GUCs the thresholds are derived from.
type settings struct {
	freezeMaxAge          int64
	multixactFreezeMaxAge int64
	freezeTableAge        int64
	freezeMinAge          int64
	failsafeAge           int64
}

// defaultSettings is the documented GUC baseline, used when pg_settings did not
// report a value and for relation rows (which carry their own effective trigger).
func defaultSettings() settings {
	return settings{
		freezeMaxAge:          defaultFreezeMaxAge,
		multixactFreezeMaxAge: defaultMultixactFreezeMaxAge,
		freezeTableAge:        defaultFreezeTableAge,
		freezeMinAge:          defaultFreezeMinAge,
		failsafeAge:           defaultFailsafeAge,
	}
}

func settingsFrom(rows []db.DatabaseFreezeAgeRow) settings {
	s := defaultSettings()
	if len(rows) == 0 {
		return s
	}

	row := rows[0] // Every row carries the same cluster-wide GUC snapshot.
	s.freezeMaxAge = orDefault(row.FreezeMaxAge.Int64, row.FreezeMaxAge.Valid, s.freezeMaxAge)
	s.multixactFreezeMaxAge = orDefault(row.MultixactFreezeMaxAge.Int64, row.MultixactFreezeMaxAge.Valid, s.multixactFreezeMaxAge)
	s.freezeTableAge = orDefault(row.FreezeTableAge.Int64, row.FreezeTableAge.Valid, s.freezeTableAge)
	s.freezeMinAge = orDefault(row.FreezeMinAge.Int64, row.FreezeMinAge.Valid, s.freezeMinAge)
	s.failsafeAge = orDefault(row.FailsafeAge.Int64, row.FailsafeAge.Valid, s.failsafeAge)

	return s
}

func orDefault(value int64, valid bool, fallback int64) int64 {
	if !valid || value <= 0 {
		return fallback
	}
	return value
}

// currentDatabaseAge returns age(datfrozenxid) for the connected database, which
// is the horizon the relation-level numbers are measured against.
func currentDatabaseAge(rows []db.DatabaseFreezeAgeRow) int64 {
	for _, row := range rows {
		if row.IsCurrentDatabase {
			return row.FreezeAge
		}
	}
	return 0
}

// thresholds are per relation/database, never global.
type thresholds struct {
	warn int64
	fail int64
}

// xidThresholds mirrors PostgreSQL's own behaviour changes: WARN where it starts
// scanning aggressively, FAIL where the failsafe strips vacuum down to bare
// freezing.
func xidThresholds(trigger, freezeTableAge, failsafeAge int64) thresholds {
	warn := int64(math.Round(aggressiveScanFraction * float64(trigger)))
	if freezeTableAge > 0 && freezeTableAge < warn {
		warn = freezeTableAge
	}

	fail := trigger * failTriggerMultiplier
	if failsafeAge > 0 && failsafeAge < fail {
		fail = failsafeAge
	}

	return thresholds{warn: clampAge(warn), fail: clampAge(fail)}
}

// multixactThresholds uses the trigger itself as WARN (400M at defaults). There
// is no separate "begin aggressive scan" number to read for MultiXacts, and RDS
// exposes no multixact CloudWatch metric at all, so reaching the trigger — the
// point anti-wraparound, non-cancellable vacuum starts — is the signal.
func multixactThresholds(trigger, failsafeAge int64) thresholds {
	fail := trigger * failTriggerMultiplier
	if failsafeAge > 0 && failsafeAge < fail {
		fail = failsafeAge
	}

	return thresholds{warn: clampAge(trigger), fail: clampAge(fail)}
}

func clampAge(age int64) int64 {
	if age > maxAge {
		return maxAge
	}
	return age
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

// counter describes one of the two independent wraparound clocks. Both have their
// own counter, trigger and wall, so both get their own subchecks.
type counter struct {
	dbFindingID    string
	dbFindingName  string
	relFindingID   string
	relFindingName string
	unit           string
	dbAge          func(db.DatabaseFreezeAgeRow) int64
	relAge         func(db.TableFreezeAgeRow) int64
	dbThresholds   func(db.DatabaseFreezeAgeRow, settings) (int64, thresholds)
	relThresholds  func(db.TableFreezeAgeRow, settings) (int64, thresholds)
	relOverride    func(db.TableFreezeAgeRow) int64
}

var xidCounter = counter{
	dbFindingID:    findingDatabaseFreeze,
	dbFindingName:  "Database Freeze Age",
	relFindingID:   findingTableFreeze,
	relFindingName: "Table Freeze Age",
	unit:           "XIDs",
	dbAge:          func(r db.DatabaseFreezeAgeRow) int64 { return r.FreezeAge },
	relAge:         func(r db.TableFreezeAgeRow) int64 { return r.FreezeAge },
	dbThresholds: func(_ db.DatabaseFreezeAgeRow, s settings) (int64, thresholds) {
		return s.freezeMaxAge, xidThresholds(s.freezeMaxAge, s.freezeTableAge, s.failsafeAge)
	},
	relThresholds: func(r db.TableFreezeAgeRow, s settings) (int64, thresholds) {
		trigger := orDefault(r.EffectiveFreezeMaxAge.Int64, r.EffectiveFreezeMaxAge.Valid, s.freezeMaxAge)
		freezeTableAge := orDefault(r.FreezeTableAge.Int64, r.FreezeTableAge.Valid, s.freezeTableAge)
		failsafeAge := orDefault(r.FailsafeAge.Int64, r.FailsafeAge.Valid, s.failsafeAge)
		return trigger, xidThresholds(trigger, freezeTableAge, failsafeAge)
	},
	relOverride: func(r db.TableFreezeAgeRow) int64 { return r.XidReloption.Int64 },
}

var multixactCounter = counter{
	dbFindingID:    findingDatabaseMultixact,
	dbFindingName:  "Database MultiXact Age",
	relFindingID:   findingTableMultixact,
	relFindingName: "Table MultiXact Age",
	unit:           "MultiXacts",
	dbAge:          func(r db.DatabaseFreezeAgeRow) int64 { return r.MultixactAge },
	relAge:         func(r db.TableFreezeAgeRow) int64 { return r.MultixactAge },
	dbThresholds: func(_ db.DatabaseFreezeAgeRow, s settings) (int64, thresholds) {
		return s.multixactFreezeMaxAge, multixactThresholds(s.multixactFreezeMaxAge, s.failsafeAge)
	},
	relThresholds: func(r db.TableFreezeAgeRow, s settings) (int64, thresholds) {
		trigger := orDefault(
			r.EffectiveMultixactFreezeMaxAge.Int64,
			r.EffectiveMultixactFreezeMaxAge.Valid,
			s.multixactFreezeMaxAge,
		)
		failsafeAge := orDefault(r.FailsafeAge.Int64, r.FailsafeAge.Valid, s.failsafeAge)
		return trigger, multixactThresholds(trigger, failsafeAge)
	},
	relOverride: func(r db.TableFreezeAgeRow) int64 { return r.MultixactReloption.Int64 },
}

// ageRow is a counter-agnostic view of one database or relation, so both clocks
// and both scopes share one renderer.
type ageRow struct {
	name       string
	target     string
	age        int64
	trigger    int64
	overridden bool
	severity   check.Severity
	size       string
	lastVacuum string
	note       string
}

func (r ageRow) headroom() int64 {
	if r.age >= r.trigger {
		return 0
	}
	return r.trigger - r.age
}

func (r ageRow) percentConsumed() int64 {
	if r.trigger <= 0 {
		return 0
	}
	return int64(math.Round(float64(r.age) / float64(r.trigger) * 100))
}

// triggerCell reports the trigger the percentage is measured against — the
// effective trigger, never 2B. At 200M the old "% to Limit" column read 10%,
// i.e. it looked calm at exactly the moment the incident starts.
func (r ageRow) triggerCell() string {
	cell := fmt.Sprintf("%d%% of %s", r.percentConsumed(), check.FormatNumber(r.trigger))
	if r.overridden {
		cell += " (reloption)"
	}
	return cell
}

func (r ageRow) headroomCell(unit string) string {
	if r.age >= r.trigger {
		return "PAST trigger"
	}
	return check.FormatNumber(r.headroom()) + " " + unit
}

// headline leads with headroom, not age: "how much room is left" is the number an
// operator acts on.
func (r ageRow) headline(unit string) string {
	// Size is the blast radius, and only relations have one.
	subject := r.name
	if r.size != "" {
		subject = fmt.Sprintf("%s (%s)", r.name, r.size)
	}

	suffix := fmt.Sprintf("(%d%% consumed)", r.percentConsumed())
	if r.age >= r.trigger {
		return fmt.Sprintf("%s is PAST its anti-wraparound trigger %s", subject, suffix)
	}

	return fmt.Sprintf(
		"%s is %s %s from its anti-wraparound trigger %s",
		subject, check.FormatNumber(r.headroom()), unit, suffix,
	)
}

// worst returns the row that has consumed the largest fraction of its own
// trigger. Rows arrive ordered by XID age, which is the wrong order for the
// MultiXact subchecks and for relations with different effective triggers.
func worst(rows []ageRow) ageRow {
	worstRow := rows[0]
	for _, row := range rows[1:] {
		if row.percentConsumed() > worstRow.percentConsumed() {
			worstRow = row
		}
	}
	return worstRow
}

func checkDatabaseAge(rows []db.DatabaseFreezeAgeRow, report *check.Report, c counter) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       c.dbFindingID,
			Name:     c.dbFindingName,
			Severity: check.SeverityPass,
			Details:  "No databases returned by pg_database",
		})
		return
	}

	s := settingsFrom(rows)

	var flagged []ageRow
	var oldest ageRow
	maxSeverity := check.SeverityPass

	for _, row := range rows {
		trigger, t := c.dbThresholds(row, s)
		item := ageRow{
			name:     row.DatabaseName,
			age:      c.dbAge(row),
			trigger:  trigger,
			severity: severityFor(c.dbAge(row), t),
			note:     databaseNote(row),
		}

		if oldest.name == "" || item.percentConsumed() > oldest.percentConsumed() {
			oldest = item
		}
		if item.severity == check.SeverityPass {
			continue
		}

		flagged = append(flagged, item)
		if item.severity > maxSeverity {
			maxSeverity = item.severity
		}
	}

	if len(flagged) == 0 {
		report.AddFinding(check.Finding{
			ID:       c.dbFindingID,
			Name:     c.dbFindingName,
			Severity: check.SeverityPass,
			Details: fmt.Sprintf(
				"All %d database(s) below their trigger. Oldest: %s at %s %s (%s)",
				len(rows), oldest.name, check.FormatNumber(oldest.age), c.unit, oldest.triggerCell(),
			),
			Debug: fmt.Sprintf("trigger=%d thresholds=%+v", oldest.trigger, thresholdsDebug(rows, s, c)),
		})
		return
	}

	tableRows := make([]check.TableRow, 0, len(flagged))
	for _, item := range flagged {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				item.name,
				check.FormatNumber(item.age),
				item.headroomCell(c.unit),
				item.triggerCell(),
				item.note,
			},
			Severity: item.severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       c.dbFindingID,
		Name:     c.dbFindingName,
		Severity: maxSeverity,
		Details:  fmt.Sprintf("%s. %s", worst(flagged).headline(c.unit), noConnectionsAdvice(flagged)),
		Table: &check.Table{
			Headers: []string{"Database", "Age", "Headroom", "% of Trigger", "Note"},
			Rows:    tableRows,
		},
		Debug: fmt.Sprintf("thresholds=%+v", thresholdsDebug(rows, s, c)),
	})
}

// databaseNote labels rows a plain VACUUM cannot fix. They are never hidden: the
// cluster XID limit is min(datfrozenxid) over every row of pg_database, so
// template0 and rdsadmin count even though they reject connections.
func databaseNote(row db.DatabaseFreezeAgeRow) string {
	if !row.Datallowconn {
		return "no connections allowed - plain VACUUM cannot fix"
	}
	if row.IsCurrentDatabase {
		return "connected database"
	}
	return "connect to VACUUM"
}

func noConnectionsAdvice(flagged []ageRow) string {
	for _, item := range flagged {
		if !strings.HasPrefix(item.note, "no connections") {
			continue
		}
		return "Databases marked \"no connections allowed\" cannot be vacuumed as-is: for template0 run " +
			"ALTER DATABASE template0 ALLOW_CONNECTIONS true; VACUUM FREEZE; " +
			"ALTER DATABASE template0 ALLOW_CONNECTIONS false; " +
			"for rdsadmin open an AWS support case - it is not reachable by customers"
	}
	return "Connect to each database and run VACUUM (FREEZE)"
}

func thresholdsDebug(rows []db.DatabaseFreezeAgeRow, s settings, c counter) thresholds {
	if len(rows) == 0 {
		_, t := c.dbThresholds(db.DatabaseFreezeAgeRow{}, s)
		return t
	}
	_, t := c.dbThresholds(rows[0], s)
	return t
}

func checkTableAge(rows []db.TableFreezeAgeRow, report *check.Report, c counter) {
	var flagged []ageRow
	maxSeverity := check.SeverityPass
	var totalAboveFloor int64

	for _, row := range rows {
		if row.TotalAboveFloor > totalAboveFloor {
			totalAboveFloor = row.TotalAboveFloor
		}

		trigger, t := c.relThresholds(row, defaultSettings())
		age := c.relAge(row)
		severity := severityFor(age, t)
		if severity == check.SeverityPass {
			continue
		}

		flagged = append(flagged, ageRow{
			name:       row.TableName,
			target:     row.VacuumTarget,
			age:        age,
			trigger:    trigger,
			overridden: c.relOverride(row) > 0,
			severity:   severity,
			size:       formatSizeEstimate(row),
			lastVacuum: formatVacuumTime(row),
			note:       relkindNote(row),
		})
		if severity > maxSeverity {
			maxSeverity = severity
		}
	}

	if len(flagged) == 0 {
		report.AddFinding(check.Finding{
			ID:       c.relFindingID,
			Name:     c.relFindingName,
			Severity: check.SeverityPass,
			Details: "No relation has reached its anti-wraparound trigger threshold " +
				"(the query only returns relations at or above it)",
		})
		return
	}

	tableRows := make([]check.TableRow, 0, len(flagged))
	for _, item := range flagged {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				item.name,
				check.FormatNumber(item.age),
				item.headroomCell(c.unit),
				item.triggerCell(),
				item.size,
				item.lastVacuum,
				item.target,
			},
			Severity: item.severity,
		})
	}

	details := worst(flagged).headline(c.unit)
	if totalAboveFloor > int64(len(rows)) {
		details += fmt.Sprintf(
			". %d relation(s) above the reporting floor, worst %d shown",
			totalAboveFloor, len(rows),
		)
	}

	report.AddFinding(check.Finding{
		ID:       c.relFindingID,
		Name:     c.relFindingName,
		Severity: maxSeverity,
		Details:  details,
		Table: &check.Table{
			Headers: []string{"Relation", "Age", "Headroom", "% of Trigger", "Size (est)", "Last Vacuum", "Vacuum Target"},
			Rows:    tableRows,
		},
		Debug: relationsDebug(flagged),
	})
}

// relkindNote explains why a relation nobody wrote is in the list.
func relkindNote(row db.TableFreezeAgeRow) string {
	switch row.Relkind {
	case "t":
		return "TOAST relation - vacuum its parent " + row.VacuumTarget
	case "m":
		return "materialized view - only REFRESH or VACUUM advances it"
	default:
		return ""
	}
}

// formatSizeEstimate renders the lock-free relpages estimate. relpages is only
// refreshed by VACUUM/ANALYZE, so 0 means "never measured" — reporting "0 B" for
// a multi-terabyte table would be worse than admitting ignorance.
func formatSizeEstimate(row db.TableFreezeAgeRow) string {
	if row.Relpages <= 0 {
		return unknownSize
	}
	return check.FormatBytes(row.SizeBytesEst)
}

func relationsDebug(flagged []ageRow) string {
	parts := make([]string, 0, len(flagged))
	for _, item := range flagged {
		parts = append(parts, fmt.Sprintf(
			"%s: age=%d trigger=%d override=%t note=%q",
			item.name, item.age, item.trigger, item.overridden, item.note,
		))
	}
	return strings.Join(parts, "; ")
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

// blocker is a normalized view of one xmin-horizon pin.
type blocker struct {
	source     string
	object     string
	pinKind    string
	age        int64
	scope      string
	duration   string
	masked     bool
	inactive   bool
	details    string
	severity   check.Severity
	slotWalCap string
}

func newBlocker(row db.XminHorizonBlockersRow, s settings) blocker {
	b := blocker{
		source:     row.Source.String,
		object:     row.Object.String,
		pinKind:    row.PinKind.String,
		age:        row.PinnedXidAge.Int64,
		scope:      row.HorizonScope.String,
		masked:     row.PrivilegeMasked.Bool,
		inactive:   row.Inactive.Bool,
		details:    row.Details.String,
		slotWalCap: row.MaxSlotWalKeepSize.String,
	}
	b.duration = formatPinDuration(row)
	b.severity = blockerSeverity(b, s)

	return b
}

func formatPinDuration(row db.XminHorizonBlockersRow) string {
	if !row.DurationSeconds.Valid {
		return "-"
	}
	out := check.FormatDurationSec(row.DurationSeconds.Int64)
	if row.DurationEstimated.Bool {
		// xact_start was NULL (masked, or the backend has no transaction yet), so
		// the duration was measured from backend_start and overstates the pin.
		out += " (from backend_start)"
	}
	return out
}

func (b blocker) isSlot() bool {
	return b.source == sourceLogicalSlot || b.source == sourcePhysicalSlot
}

// isConcurrentIndexBuild spots CREATE INDEX CONCURRENTLY / REINDEX CONCURRENTLY,
// which hold an xmin for their whole run. Cancelling one leaves an invalid index
// behind, so it is never a blind kill.
func (b blocker) isConcurrentIndexBuild() bool {
	upper := strings.ToUpper(b.details)
	if !strings.Contains(upper, "CONCURRENTLY") {
		return false
	}
	return strings.Contains(upper, "CREATE INDEX") || strings.Contains(upper, "REINDEX")
}

// blockerWarnAt is max(vacuum_freeze_min_age, 0.25 * effective trigger).
func blockerWarnAt(s settings) int64 {
	return max(s.freezeMinAge, int64(math.Round(blockerWarnTriggerFraction*float64(s.freezeMaxAge))))
}

func blockerSeverity(b blocker, s settings) check.Severity {
	severity := check.SeverityInfo
	switch {
	case b.age >= s.freezeMaxAge:
		severity = check.SeverityFail
	case b.age >= blockerWarnAt(s):
		severity = check.SeverityWarn
	}

	// An inactive slot's pin is monotonic by construction: nothing will ever
	// advance it, so age is irrelevant.
	if b.isSlot() && b.inactive {
		severity = check.SeverityFail
	}

	return capSeverity(b, severity)
}

// capSeverity applies the "is this actionable from here" caps.
func capSeverity(b blocker, severity check.Severity) check.Severity {
	// Autovacuum is the cure. Killing it is the classic 3am mistake.
	if b.source == sourceAutovacuum {
		return check.SeverityInfo
	}
	// The fix for standby feedback is on the replica, and cancelling a concurrent
	// index build leaves an invalid index. Neither is a page-someone action.
	if b.source == sourceStandbyFeedback || b.isConcurrentIndexBuild() {
		if severity > check.SeverityWarn {
			return check.SeverityWarn
		}
	}
	return severity
}

func checkHorizonBlockers(rows []db.XminHorizonBlockersRow, s settings, horizonAge int64, report *check.Report) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       findingHorizonBlockers,
			Name:     "Xmin Horizon Blockers",
			Severity: check.SeverityPass,
			Details: "Nothing pins the xmin horizon: no backend xid/xmin, no replication slot xmin, " +
				"no prepared transaction. " + reconcile(0, horizonAge, s),
		})
		return
	}

	blockers := make([]blocker, 0, len(rows))
	maxSeverity := check.SeverityInfo
	oldest := int64(0)
	masked := 0

	for _, row := range rows {
		b := newBlocker(row, s)
		blockers = append(blockers, b)

		if b.age > oldest {
			oldest = b.age
		}
		if b.masked {
			masked++
		}
		if b.severity > maxSeverity {
			maxSeverity = b.severity
		}
	}

	tableRows := make([]check.TableRow, 0, len(blockers))
	for _, b := range blockers {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				b.source,
				b.object,
				b.pinKind,
				check.FormatNumber(b.age),
				b.scope,
				b.duration,
				b.details,
			},
			Severity: b.severity,
		})
	}

	details := fmt.Sprintf(
		"%d object(s) pin the xmin horizon; oldest pin is %s XIDs old. %s",
		len(blockers), check.FormatNumber(oldest), reconcile(oldest, horizonAge, s),
	)
	if inactive := inactiveSlots(blockers); inactive != "" {
		details += " " + inactive
	}
	if masked > 0 {
		details += fmt.Sprintf(
			" %d pin(s) are privilege-masked (query shows <insufficient privilege>, state/backend_type/xact_start NULL); "+
				"run GRANT pg_monitor TO <pgdoctor role> for full attribution - this is missing visibility, not health.",
			masked,
		)
	}

	report.AddFinding(check.Finding{
		ID:       findingHorizonBlockers,
		Name:     "Xmin Horizon Blockers",
		Severity: maxSeverity,
		Details:  details,
		Table: &check.Table{
			Headers: []string{"Source", "Object", "Pin", "Pin Age", "Scope", "Duration", "Detail"},
			Rows:    tableRows,
		},
		Debug: fmt.Sprintf(
			"warn_at=%d fail_at=%d horizon_age=%d",
			blockerWarnAt(s), s.freezeMaxAge, horizonAge,
		),
	})
}

// reconcile is the part of this finding a DBA reads first: if the oldest pin is
// much younger than age(datfrozenxid), no live blocker explains the horizon and
// there is no PID to kill — the problem is vacuum throughput or scheduling.
func reconcile(oldestPin, horizonAge int64, s settings) string {
	if horizonAge < s.freezeMinAge {
		return fmt.Sprintf("age(datfrozenxid) is %s XIDs - nothing to reconcile.", check.FormatNumber(horizonAge))
	}
	if oldestPin*2 >= horizonAge {
		return fmt.Sprintf(
			"This is consistent with age(datfrozenxid) = %s XIDs: the pin plausibly explains the horizon.",
			check.FormatNumber(horizonAge),
		)
	}
	return fmt.Sprintf(
		"age(datfrozenxid) is %s XIDs but the oldest pin is only %s XIDs old, so no live blocker explains the horizon: "+
			"this is vacuum throughput or scheduling (workers, cost delay, a table never reached), not something to kill. "+
			"Stop hunting for a PID.",
		check.FormatNumber(horizonAge), check.FormatNumber(oldestPin),
	)
}

func inactiveSlots(blockers []blocker) string {
	var names []string
	capValue := ""
	for _, b := range blockers {
		if !b.isSlot() || !b.inactive {
			continue
		}
		names = append(names, b.object)
		capValue = b.slotWalCap
	}
	if len(names) == 0 {
		return ""
	}

	msg := fmt.Sprintf(
		"Slot(s) %s are INACTIVE and still hold a pin, which nothing will advance.",
		strings.Join(names, ", "),
	)
	if capValue == "-1" {
		msg += " max_slot_wal_keep_size is -1 (the RDS default), so the slot never self-invalidates and the pin is permanent."
	} else if capValue != "" {
		msg += fmt.Sprintf(" max_slot_wal_keep_size is %s, so the slot invalidates once it exceeds that budget.", capValue)
	}

	return msg
}

// checkDoomLoop is predictive and needs no runtime observation: if a pin is at or
// past the effective trigger, the next anti-wraparound vacuum is guaranteed to
// finish without advancing relfrozenxid past the pin, and relation_needs_vacanalyze
// re-queues the relation within autovacuum_naptime. That loop is non-cancellable,
// full-table, and runs indefinitely while age keeps climbing.
func checkDoomLoop(rows []db.XminHorizonBlockersRow, s settings, report *check.Report) {
	var doomed []blocker

	for _, row := range rows {
		b := newBlocker(row, s)
		// Autovacuum's own xmin is the cure in progress, not a cause.
		if b.source == sourceAutovacuum {
			continue
		}
		if b.age >= s.freezeMaxAge {
			doomed = append(doomed, b)
		}
	}

	if len(doomed) == 0 {
		report.AddFinding(check.Finding{
			ID:       findingDoomLoop,
			Name:     "Vacuum Doom Loop",
			Severity: check.SeverityPass,
			Details: fmt.Sprintf(
				"No pin is at or past the %s XID anti-wraparound trigger, so an anti-wraparound vacuum can still "+
					"advance relfrozenxid",
				check.FormatNumber(s.freezeMaxAge),
			),
		})
		return
	}

	tableRows := make([]check.TableRow, 0, len(doomed))
	for _, b := range doomed {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				b.source,
				b.object,
				check.FormatNumber(b.age),
				doomLoopRemediation(b),
			},
			Severity: check.SeverityFail,
		})
	}

	report.AddFinding(check.Finding{
		ID:       findingDoomLoop,
		Name:     "Vacuum Doom Loop",
		Severity: check.SeverityFail,
		Details: fmt.Sprintf(
			"%d pin(s) are at or past the %s XID anti-wraparound trigger. Any anti-wraparound vacuum will now complete "+
				"WITHOUT advancing relfrozenxid past the pin and be re-queued within autovacuum_naptime: a "+
				"non-cancellable, full-table loop that never ends while age keeps climbing. Release the pin first - "+
				"vacuum tuning cannot fix this",
			len(doomed), check.FormatNumber(s.freezeMaxAge),
		),
		Table: &check.Table{
			Headers: []string{"Source", "Object", "Pin Age", "Remediation"},
			Rows:    tableRows,
		},
	})
}

// doomLoopRemediation emits single-PID / single-object commands only. A
// set-valued kill pasted by a stressed human at 3am is its own incident.
func doomLoopRemediation(b blocker) string {
	switch {
	case b.isSlot():
		return fmt.Sprintf(
			"escalate first (dropping a Debezium slot forces a re-snapshot), never drop an active slot: "+
				"SELECT pg_drop_replication_slot('%s');",
			b.object,
		)
	case b.source == sourcePreparedXact:
		return fmt.Sprintf("ROLLBACK PREPARED '%s'; -- verify with the owning application first", b.object)
	case b.source == sourceStandbyFeedback:
		return "fix it on the replica: set hot_standby_feedback = off there, or make its queries shorter. " +
			"Do not kill the walsender on the primary"
	case b.isConcurrentIndexBuild():
		return fmt.Sprintf(
			"CREATE/REINDEX CONCURRENTLY on pid %s - do not cancel blindly, it leaves an INVALID index to drop",
			b.object,
		)
	default:
		return fmt.Sprintf(
			"SELECT pg_cancel_backend(%s); then, if it survives, SELECT pg_terminate_backend(%s);",
			b.object, b.object,
		)
	}
}
