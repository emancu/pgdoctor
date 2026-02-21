// Package sessionsettings implements a check for validating role-level timeout and logging configurations.
package sessionsettings

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type dbSessionSettings []db.SessionSettingsRow

type SessionSettingsQueries interface {
	SessionSettings(context.Context) ([]db.SessionSettingsRow, error)
}

type settingCheck struct {
	Role      string
	Parameter string
	Current   string
	Expected  string
	Status    string
	Severity  check.Severity
}

type checker struct {
	queryer SessionSettingsQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "session-settings",
		Name:        "PostgreSQL Session Configs",
		Description: "Validates role-level timeout and logging configurations",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queryer SessionSettingsQueries) check.Checker {
	return &checker{
		queryer: queryer,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	settings, err := c.queryer.SessionSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryConfigs, report.CheckID, err)
	}

	dbSettings := dbSessionSettings(settings)

	// Collect all setting checks into a table
	var checks []settingCheck

	roTimeouts, err := checkUserTimeouts(dbSettings, "app_ro")
	if err != nil {
		return nil, fmt.Errorf("checking timeouts for app_ro: %w", err)
	}
	checks = append(checks, roTimeouts...)

	rwTimeouts, err := checkUserTimeouts(dbSettings, "app_rw")
	if err != nil {
		return nil, fmt.Errorf("checking timeouts for app_rw: %w", err)
	}
	checks = append(checks, rwTimeouts...)

	roLogSettings, err := checkLogStatements(dbSettings, "app_ro")
	if err != nil {
		return nil, fmt.Errorf("checking log statements for app_ro: %w", err)
	}
	checks = append(checks, roLogSettings...)

	rwLogSettings, err := checkLogStatements(dbSettings, "app_rw")
	if err != nil {
		return nil, fmt.Errorf("checking log statements for app_rw: %w", err)
	}
	checks = append(checks, rwLogSettings...)

	// Determine overall severity
	overallSeverity := check.SeverityOK
	for _, sc := range checks {
		if sc.Severity > overallSeverity {
			overallSeverity = sc.Severity
		}
	}

	// Build result with table
	result := check.Finding{
		Name:     report.Name,
		ID:       report.CheckID,
		Severity: overallSeverity,
	}

	if overallSeverity != check.SeverityOK {
		// Create table with only non-OK entries
		tableRows := []check.TableRow{}
		for _, sc := range checks {
			if sc.Severity != check.SeverityOK {
				tableRows = append(tableRows, check.TableRow{
					Cells: []string{
						sc.Role,
						sc.Parameter,
						sc.Current,
						sc.Expected,
						sc.Status,
					},
					Severity: sc.Severity,
				})
			}
		}

		result.Details = fmt.Sprintf("Found %d configuration issue(s)", len(tableRows))
		result.Table = &check.Table{
			Headers: []string{"Role", "Parameter", "Current", "Expected", "Status"},
			Rows:    tableRows,
		}
	}

	report.AddFinding(result)
	return report, nil
}

func checkUserTimeouts(s dbSessionSettings, user string) ([]settingCheck, error) {
	var checks []settingCheck

	stmtTimeout, err := s.fetch(user, "statement_timeout")
	if err != nil {
		return nil, fmt.Errorf("fetching statement_timeout: %w", err)
	}

	idleTimeout, err := s.fetch(user, "idle_in_transaction_session_timeout")
	if err != nil {
		return nil, fmt.Errorf("fetching idle_in_transaction_session_timeout: %w", err)
	}

	txTimeout := s.get(user, "transaction_timeout", 0)

	// Check statement_timeout
	if stmtTimeout == 0 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   "0ms (disabled)",
			Expected:  "500-5000ms",
			Status:    "MUST be set",
			Severity:  check.SeverityFail,
		})
	} else if stmtTimeout > 10000 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   fmt.Sprintf("%dms", stmtTimeout),
			Expected:  "500-5000ms",
			Status:    "Too high",
			Severity:  check.SeverityFail,
		})
	} else if stmtTimeout > 5000 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   fmt.Sprintf("%dms", stmtTimeout),
			Expected:  "500-5000ms",
			Status:    "High",
			Severity:  check.SeverityWarn,
		})
	} else {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   fmt.Sprintf("%dms", stmtTimeout),
			Expected:  "500-5000ms",
			Status:    "OK",
			Severity:  check.SeverityOK,
		})
	}

	// Check idle_in_transaction_session_timeout
	if idleTimeout == 0 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "idle_in_txn_timeout",
			Current:   "0ms (disabled)",
			Expected:  "60000ms",
			Status:    "Disabled",
			Severity:  check.SeverityWarn,
		})
	} else {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "idle_in_txn_timeout",
			Current:   fmt.Sprintf("%dms", idleTimeout),
			Expected:  "60000ms",
			Status:    "OK",
			Severity:  check.SeverityOK,
		})
	}

	// Check transaction_timeout
	if txTimeout == 0 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "transaction_timeout",
			Current:   "0ms (disabled)",
			Expected:  "500-5000ms",
			Status:    "MUST be set (PG17+)",
			Severity:  check.SeverityFail,
		})
	} else if txTimeout > 10000 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "transaction_timeout",
			Current:   fmt.Sprintf("%dms", txTimeout),
			Expected:  "500-5000ms",
			Status:    "Too high",
			Severity:  check.SeverityFail,
		})
	} else if txTimeout > 5000 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "transaction_timeout",
			Current:   fmt.Sprintf("%dms", txTimeout),
			Expected:  "500-5000ms",
			Status:    "High",
			Severity:  check.SeverityWarn,
		})
	} else {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "transaction_timeout",
			Current:   fmt.Sprintf("%dms", txTimeout),
			Expected:  "500-5000ms",
			Status:    "OK",
			Severity:  check.SeverityOK,
		})
	}

	return checks, nil
}

func checkLogStatements(s dbSessionSettings, user string) ([]settingCheck, error) {
	var checks []settingCheck

	minDuration, err := s.fetch(user, "log_min_duration_statement")
	if err != nil {
		return nil, fmt.Errorf("fetching log_min_duration_statement: %w", err)
	}

	if minDuration == -1 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "log_min_duration",
			Current:   "-1 (disabled)",
			Expected:  "2000ms",
			Status:    "Disabled",
			Severity:  check.SeverityFail,
		})
	} else if minDuration < 500 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "log_min_duration",
			Current:   fmt.Sprintf("%dms", minDuration),
			Expected:  "2000ms",
			Status:    "Too low",
			Severity:  check.SeverityFail,
		})
	} else {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "log_min_duration",
			Current:   fmt.Sprintf("%dms", minDuration),
			Expected:  "2000ms",
			Status:    "OK",
			Severity:  check.SeverityOK,
		})
	}

	return checks, nil
}

// Type functions

func (s dbSessionSettings) fetch(user string, name string) (int64, error) {
	for _, n := range s {
		if n.RoleName.Valid && n.RoleName.String == user {
			if n.SettingName.Valid && n.SettingName.String == name && n.SettingValue.Valid {
				intVal, err := strconv.ParseInt(n.SettingValue.String, 10, 64)
				if err != nil {
					return 0, fmt.Errorf("setting %s for user %s has invalid integer value: %w", name, user, err)
				}
				return intVal, nil
			}
		}
	}

	return 0, fmt.Errorf("setting %s not found for user %s", name, user)
}

func (s dbSessionSettings) get(user string, name string, def int64) int64 {
	for _, n := range s {
		if n.RoleName.Valid && n.RoleName.String == user {
			if n.SettingName.Valid && n.SettingName.String == name && n.SettingValue.Valid {
				intVal, err := strconv.ParseInt(n.SettingValue.String, 10, 64)

				if err != nil {
					return def
				}

				return intVal
			}
		}
	}

	return def
}
