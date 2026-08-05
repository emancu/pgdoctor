package sessionsettings_test

import (
	"context"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/sessionsettings"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func newStaticSessionSettingsQueryer(rows []db.SessionSettingsRow) sessionSettingsQueryer {
	return sessionSettingsQueryer(func() []db.SessionSettingsRow {
		return rows
	})
}

type sessionSettingsQueryer func() []db.SessionSettingsRow

func (f sessionSettingsQueryer) SessionSettings(context.Context) ([]db.SessionSettingsRow, error) {
	return f(), nil
}

func mapToSessionSettingsRows(settings map[string]map[string]string) []db.SessionSettingsRow {
	var rows []db.SessionSettingsRow

	for role, roleSettings := range settings {
		for name, value := range roleSettings {
			rows = append(rows, db.SessionSettingsRow{
				RoleName:     pgtype.Text{String: role, Valid: true},
				SettingName:  pgtype.Text{String: name, Valid: true},
				SettingValue: pgtype.Text{String: value, Valid: true},
			})
		}
	}

	return rows
}

func optimalSessionSettings() map[string]map[string]string {
	return map[string]map[string]string{
		"app_ro": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
		"app_rw": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
	}
}

type ExpectedResultCheck struct {
	ID  string
	Sev check.Severity
}

func overrideOptimalSessionSettings(role, name, value string) []db.SessionSettingsRow {
	settings := optimalSessionSettings()
	settings[role][name] = value

	return mapToSessionSettingsRows(settings)
}

func overrideBothRoles(name, value string) []db.SessionSettingsRow {
	settings := optimalSessionSettings()
	settings["app_ro"][name] = value
	settings["app_rw"][name] = value

	return mapToSessionSettingsRows(settings)
}

func removeFromSessionSettings(role, name string) []db.SessionSettingsRow {
	settings := optimalSessionSettings()
	delete(settings[role], name)

	return mapToSessionSettingsRows(settings)
}

// removeFromAllRoles drops a setting from every role, simulating a server that
// lacks the setting (e.g. transaction_timeout on PG<17 emits no row).
func removeFromAllRoles(name string) []db.SessionSettingsRow {
	settings := optimalSessionSettings()
	for role := range settings {
		delete(settings[role], name)
	}

	return mapToSessionSettingsRows(settings)
}

// setUnit stamps the row matching role/name with the given base Unit, mirroring
// what the real query returns from pg_settings.unit.
func setUnit(rows []db.SessionSettingsRow, role, name, unit string) []db.SessionSettingsRow {
	for i := range rows {
		if rows[i].RoleName.String == role && rows[i].SettingName.String == name {
			rows[i].Unit = pgtype.Text{String: unit, Valid: true}
		}
	}

	return rows
}

func Test_SessionSettings(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name   string
		Rows   []db.SessionSettingsRow
		Expect []ExpectedResultCheck
	}

	testCases := []testCase{
		{
			Name: "with optimal values, check is OK",
			Rows: mapToSessionSettingsRows(optimalSessionSettings()),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityPass},
			},
		},
		// Statement timeout tests
		{
			Name: "statement_timeout disabled for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "statement_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "statement_timeout too high for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "statement_timeout", "15000"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "statement_timeout high (warning) for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "statement_timeout", "7000"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			// ALTER ROLE stores unit-suffixed values literally. "2000ms" must
			// parse to 2000 (≤ 5000 warn threshold) instead of crashing the check.
			Name: "statement_timeout unit-suffixed 2000ms for app_ro is OK",
			Rows: setUnit(
				overrideOptimalSessionSettings("app_ro", "statement_timeout", "2000ms"),
				"app_ro", "statement_timeout", "ms",
			),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityPass},
			},
		},
		{
			Name: "statement_timeout disabled for both roles",
			Rows: overrideBothRoles("statement_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		// Idle timeout tests
		{
			Name: "idle_in_transaction_session_timeout disabled for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "idle_in_transaction_session_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "idle_in_transaction_session_timeout disabled for both roles",
			Rows: overrideBothRoles("idle_in_transaction_session_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		// Transaction timeout tests
		{
			// Row absent ⇒ server predates PG17 ⇒ transaction_timeout is skipped,
			// not failed. The remaining optimal settings keep the check OK.
			Name: "transaction_timeout missing for app_ro (PG < 17) is skipped",
			Rows: removeFromSessionSettings("app_ro", "transaction_timeout"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityPass},
			},
		},
		{
			// All roles lack the row ⇒ PG<17 ⇒ no transaction_timeout finding at all.
			Name: "transaction_timeout absent for all roles (PG < 17) yields no finding",
			Rows: removeFromAllRoles("transaction_timeout"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityPass},
			},
		},
		{
			// PG17+ present with value 0 must WARN (regression guard).
			Name: "transaction_timeout disabled for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "transaction_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "transaction_timeout too high for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "transaction_timeout", "15000"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "transaction_timeout high (warning) for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "transaction_timeout", "7000"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		// Log min duration tests
		{
			Name: "log_min_duration_statement disabled for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "log_min_duration_statement", "-1"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "log_min_duration_statement too low for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "log_min_duration_statement", "100"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
		{
			Name: "log_min_duration_statement too low for both roles",
			Rows: overrideBothRoles("log_min_duration_statement", "100"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityWarn},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newStaticSessionSettingsQueryer(tc.Rows)

			checker := sessionsettings.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			results := report.Results
			require.Equal(t, 1, len(results), "Should have exactly 1 result")

			result := results[0]
			require.Equal(t, tc.Expect[0].ID, result.ID, "Result ID should match")
			require.Equal(t, tc.Expect[0].Sev, result.Severity, "Result severity should match")

			// If not OK, should have a table
			if result.Severity != check.SeverityPass {
				require.NotNil(t, result.Table, "Non-OK result should have a table")
				require.Greater(t, len(result.Table.Rows), 0, "Table should have rows")
			}
		})
	}
}

func Test_SessionSettings_MultipleIssues(t *testing.T) {
	t.Parallel()

	settings := map[string]map[string]string{
		"app_ro": {
			"statement_timeout":                   "0",     // disabled - WARN
			"idle_in_transaction_session_timeout": "0",     // disabled - WARN
			"transaction_timeout":                 "15000", // too high - WARN
			"log_min_duration_statement":          "-1",    // disabled - WARN
		},
		"app_rw": {
			"statement_timeout":                   "7000",  // high - WARN
			"idle_in_transaction_session_timeout": "60000", // OK
			"transaction_timeout":                 "0",     // disabled - WARN
			"log_min_duration_statement":          "100",   // too low - WARN
		},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))

	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.NotNil(t, result.Table, "Result should have a table")

	// Should have multiple issues detected in the table
	require.Greater(t, len(result.Table.Rows), 5, "Should detect multiple configuration issues")

	// The check caps at WARN: every issue is a WARN, none escalate to FAIL.
	require.Equal(t, check.SeverityWarn, result.Severity, "Report caps at WARN")
	for _, row := range result.Table.Rows {
		require.Equal(t, check.SeverityWarn, row.Severity, "Every issue row should be WARN")
	}
}

func Test_SessionSettings_BothRolesCheckedEqually(t *testing.T) {
	t.Parallel()

	// Both roles have the same bad configuration
	settings := overrideBothRoles("statement_timeout", "0")

	queryer := newStaticSessionSettingsQueryer(settings)

	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.NotNil(t, result.Table, "Result should have a table")

	// Count failures for each role in table rows
	appRoCount := 0
	appRwCount := 0
	for _, row := range result.Table.Rows {
		// Row.Cells = [Role, Parameter, Current, Expected, Status]
		if len(row.Cells) >= 2 && row.Cells[0] == "app_ro" && row.Cells[1] == "statement_timeout" {
			appRoCount++
		}
		if len(row.Cells) >= 2 && row.Cells[0] == "app_rw" && row.Cells[1] == "statement_timeout" {
			appRwCount++
		}
	}

	// Both roles should have the same issue
	require.Equal(t, 1, appRoCount, "app_ro should have statement_timeout issue")
	require.Equal(t, 1, appRwCount, "app_rw should have statement_timeout issue")
}

func Test_SessionSettings_SpecificDetailChecks(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name              string
		Rows              []db.SessionSettingsRow
		ExpectedParameter string
		ExpectedCurrent   string
		ExpectedStatus    string
	}

	testCases := []testCase{
		{
			Name:              "statement_timeout disabled message",
			Rows:              overrideOptimalSessionSettings("app_ro", "statement_timeout", "0"),
			ExpectedParameter: "statement_timeout",
			ExpectedCurrent:   "0ms (disabled)",
			ExpectedStatus:    "MUST be set",
		},
		{
			Name:              "statement_timeout too high message",
			Rows:              overrideOptimalSessionSettings("app_ro", "statement_timeout", "15000"),
			ExpectedParameter: "statement_timeout",
			ExpectedCurrent:   "15000ms",
			ExpectedStatus:    "Too high",
		},
		{
			Name:              "log_min_duration_statement disabled message",
			Rows:              overrideOptimalSessionSettings("app_ro", "log_min_duration_statement", "-1"),
			ExpectedParameter: "log_min_duration",
			ExpectedCurrent:   "-1 (disabled)",
			ExpectedStatus:    "Disabled",
		},
		{
			Name:              "transaction_timeout disabled message",
			Rows:              overrideOptimalSessionSettings("app_ro", "transaction_timeout", "0"),
			ExpectedParameter: "transaction_timeout",
			ExpectedCurrent:   "0ms (disabled)",
			ExpectedStatus:    "MUST be set (PG17+)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newStaticSessionSettingsQueryer(tc.Rows)

			checker := sessionsettings.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			results := report.Results
			require.Equal(t, 1, len(results), "Should have exactly 1 result")

			result := results[0]
			require.NotNil(t, result.Table, "Result should have a table")
			require.Greater(t, len(result.Table.Rows), 0, "Table should have rows")

			// Find the row matching the expected parameter
			var foundRow *check.TableRow
			for _, row := range result.Table.Rows {
				// Row.Cells = [Role, Parameter, Current, Expected, Status]
				if len(row.Cells) >= 5 && row.Cells[1] == tc.ExpectedParameter {
					foundRow = &row
					break
				}
			}

			require.NotNil(t, foundRow, "Should find row for parameter: %s", tc.ExpectedParameter)
			require.Equal(t, tc.ExpectedCurrent, foundRow.Cells[2], "Current value should match")
			require.Equal(t, tc.ExpectedStatus, foundRow.Cells[4], "Status should match")
		})
	}
}

func Test_SessionSettings_EmptyRoles(t *testing.T) {
	t.Parallel()

	queryer := newStaticSessionSettingsQueryer([]db.SessionSettingsRow{})

	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityPass, result.Severity, "Empty roles should be OK")
	require.Equal(t, "No application roles found", result.Details)
}

func Test_SessionSettings_ArbitraryRoleNames(t *testing.T) {
	t.Parallel()

	settings := map[string]map[string]string{
		"api_user": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
		"worker_user": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))

	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")
	require.Equal(t, check.SeverityPass, results[0].Severity, "Arbitrary role names with optimal settings should be OK")
}

func Test_SessionSettings_ConfiguredRoleMissing(t *testing.T) {
	t.Parallel()

	settings := map[string]map[string]string{
		"api_user": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
	}

	cfg := check.Config{
		"session-settings": {"roles": "api_user,nonexistent"},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))

	checker := sessionsettings.New(queryer, cfg)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity, "Missing configured role should warn")
	require.NotNil(t, result.Table, "Result should have a table")

	// Find the "Role not found" row
	var foundRow *check.TableRow
	for _, row := range result.Table.Rows {
		if len(row.Cells) >= 5 && row.Cells[0] == "nonexistent" && row.Cells[4] == "Role not found" {
			foundRow = &row
			break
		}
	}
	require.NotNil(t, foundRow, "Should find 'Role not found' row for nonexistent role")
}

func Test_SessionSettings_CustomThreshold(t *testing.T) {
	t.Parallel()

	// With a tighter threshold of 2000, statement_timeout=3000 → "Too high" WARN
	settings := map[string]map[string]string{
		"app_ro": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
	}

	cfg := check.Config{
		"session-settings": {
			"roles":   "app_ro",
			"timeout": "2000",
		},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))
	checker := sessionsettings.New(queryer, cfg)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	result := report.Results[0]
	require.Equal(t, check.SeverityWarn, result.Severity, "3000ms should WARN when threshold is 2000")
	require.NotNil(t, result.Table)

	warnCount := 0
	for _, row := range result.Table.Rows {
		if row.Severity == check.SeverityWarn {
			warnCount++
			require.Equal(t, "≤ 2000ms", row.Cells[3], "Expected should reflect custom threshold")
			require.Equal(t, "Too high", row.Cells[4], "Status should be 'Too high'")
		}
	}
	require.Equal(t, 2, warnCount, "Both statement_timeout and transaction_timeout should WARN")
}

func Test_SessionSettings_DefaultThreshold(t *testing.T) {
	t.Parallel()

	// No config → default threshold 5000; statement_timeout=7000 → WARN
	settings := map[string]map[string]string{
		"app_ro": {
			"statement_timeout":                   "7000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "7000",
			"log_min_duration_statement":          "2000",
		},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))
	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	result := report.Results[0]
	require.Equal(t, check.SeverityWarn, result.Severity, "7000ms should WARN with the default 5000 threshold")
	require.NotNil(t, result.Table)

	for _, row := range result.Table.Rows {
		require.Equal(t, "≤ 5000ms", row.Cells[3], "Expected should reflect default warn threshold")
	}
}

func Test_SessionSettings_ConfigOverridesDiscovery(t *testing.T) {
	t.Parallel()

	// DB has both api_user and worker_user
	settings := map[string]map[string]string{
		"api_user": {
			"statement_timeout":                   "3000",
			"idle_in_transaction_session_timeout": "60000",
			"transaction_timeout":                 "3000",
			"log_min_duration_statement":          "2000",
		},
		"worker_user": {
			"statement_timeout":                   "0",  // bad
			"idle_in_transaction_session_timeout": "0",  // bad
			"transaction_timeout":                 "0",  // bad
			"log_min_duration_statement":          "-1", // bad
		},
	}

	// Config only specifies api_user — worker_user should be ignored
	cfg := check.Config{
		"session-settings": {"roles": "api_user"},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))

	checker := sessionsettings.New(queryer, cfg)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	// Only api_user is checked (which has good settings), worker_user is ignored
	require.Equal(t, check.SeverityPass, results[0].Severity, "Should only check configured roles")
}
