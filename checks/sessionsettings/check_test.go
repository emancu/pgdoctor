package sessionsettings_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/sessionsettings"
	"github.com/emancu/pgdoctor/db"
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
				{ID: "session-settings", Sev: check.SeverityOK},
			},
		},
		// Statement timeout tests
		{
			Name: "statement_timeout disabled for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "statement_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
			},
		},
		{
			Name: "statement_timeout too high for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "statement_timeout", "15000"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
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
			Name: "statement_timeout disabled for both roles",
			Rows: overrideBothRoles("statement_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
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
			Name: "transaction_timeout missing for app_ro",
			Rows: removeFromSessionSettings("app_ro", "transaction_timeout"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
			},
		},
		{
			Name: "transaction_timeout disabled for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "transaction_timeout", "0"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
			},
		},
		{
			Name: "transaction_timeout too high for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "transaction_timeout", "15000"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
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
				{ID: "session-settings", Sev: check.SeverityFail},
			},
		},
		{
			Name: "log_min_duration_statement too low for app_ro",
			Rows: overrideOptimalSessionSettings("app_ro", "log_min_duration_statement", "100"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
			},
		},
		{
			Name: "log_min_duration_statement too low for both roles",
			Rows: overrideBothRoles("log_min_duration_statement", "100"),
			Expect: []ExpectedResultCheck{
				{ID: "session-settings", Sev: check.SeverityFail},
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

			results := report.Results
			require.Equal(t, 1, len(results), "Should have exactly 1 result")

			result := results[0]
			require.Equal(t, tc.Expect[0].ID, result.ID, "Result ID should match")
			require.Equal(t, tc.Expect[0].Sev, result.Severity, "Result severity should match")

			// If not OK, should have a table
			if result.Severity != check.SeverityOK {
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
			"statement_timeout":                   "0",     // disabled - FAIL
			"idle_in_transaction_session_timeout": "0",     // disabled - WARN
			"transaction_timeout":                 "15000", // too high - FAIL
			"log_min_duration_statement":          "-1",    // disabled - FAIL
		},
		"app_rw": {
			"statement_timeout":                   "7000",  // high - WARN
			"idle_in_transaction_session_timeout": "60000", // OK
			"transaction_timeout":                 "0",     // disabled - FAIL
			"log_min_duration_statement":          "100",   // too low - FAIL
		},
	}

	queryer := newStaticSessionSettingsQueryer(mapToSessionSettingsRows(settings))

	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.NotNil(t, result.Table, "Result should have a table")

	// Should have multiple issues detected in the table
	require.Greater(t, len(result.Table.Rows), 5, "Should detect multiple configuration issues")

	// Verify we have both FAIL and WARN severities in table rows
	hasFail := false
	hasWarn := false
	for _, row := range result.Table.Rows {
		if row.Severity == check.SeverityFail {
			hasFail = true
		}
		if row.Severity == check.SeverityWarn {
			hasWarn = true
		}
	}
	require.True(t, hasFail, "Should have at least one FAIL severity in table rows")
	require.True(t, hasWarn, "Should have at least one WARN severity in table rows")
}

func Test_SessionSettings_BothRolesCheckedEqually(t *testing.T) {
	t.Parallel()

	// Both roles have the same bad configuration
	settings := overrideBothRoles("statement_timeout", "0")

	queryer := newStaticSessionSettingsQueryer(settings)

	checker := sessionsettings.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

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
