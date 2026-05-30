// Package sessionsettings implements a check for validating role-level timeout and logging configurations.
package sessionsettings

import (
	"context"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	queryer     SessionSettingsQueries
	roles       []string
	timeoutWarn int64 // default: 5000
	timeoutFail int64 // default: 10000
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

func New(queryer SessionSettingsQueries, cfg ...check.Config) check.Checker {
	c := &checker{
		queryer:     queryer,
		timeoutWarn: 5000,
		timeoutFail: 10000,
	}
	if len(cfg) > 0 && cfg[0] != nil {
		if myCfg, ok := cfg[0][Metadata().CheckID]; ok {
			if roles, ok := myCfg["roles"]; ok {
				c.roles = strings.Split(roles, ",")
			}
			if v, ok := myCfg["timeout_warn"]; ok {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					c.timeoutWarn = n
				}
			}
			if v, ok := myCfg["timeout_fail"]; ok {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					c.timeoutFail = n
				}
			}
		}
	}
	return c
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

	// Determine which roles to check
	roles := dbSettings.roles() // dynamic discovery
	if c.roles != nil {
		roles = c.roles // override with configured roles
	}

	if len(roles) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No application roles found",
		})
		return report, nil
	}

	// Collect all setting checks into a table
	var checks []settingCheck

	for _, role := range roles {
		if !dbSettings.hasRole(role) {
			checks = append(checks, settingCheck{
				Role:      role,
				Parameter: "(all)",
				Current:   "N/A",
				Expected:  "Role exists",
				Status:    "Role not found",
				Severity:  check.SeverityWarn,
			})
			continue
		}

		timeouts, err := c.checkUserTimeouts(dbSettings, role)
		if err != nil {
			return nil, fmt.Errorf("checking timeouts for %s: %w", role, err)
		}
		checks = append(checks, timeouts...)

		logSettings, err := checkLogStatements(dbSettings, role)
		if err != nil {
			return nil, fmt.Errorf("checking log statements for %s: %w", role, err)
		}
		checks = append(checks, logSettings...)
	}

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

func (c *checker) checkUserTimeouts(s dbSessionSettings, user string) ([]settingCheck, error) {
	var checks []settingCheck

	stmtTimeout, _, err := s.fetch(user, "statement_timeout")
	if err != nil {
		return nil, fmt.Errorf("fetching statement_timeout: %w", err)
	}

	idleTimeout, _, err := s.fetch(user, "idle_in_transaction_session_timeout")
	if err != nil {
		return nil, fmt.Errorf("fetching idle_in_transaction_session_timeout: %w", err)
	}

	txTimeout, txFound, err := s.fetch(user, "transaction_timeout")
	if err != nil {
		return nil, fmt.Errorf("fetching transaction_timeout: %w", err)
	}

	// Check statement_timeout
	expectedTimeout := fmt.Sprintf("≤ %dms", c.timeoutWarn)
	if stmtTimeout == 0 {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   "0ms (disabled)",
			Expected:  expectedTimeout,
			Status:    "MUST be set",
			Severity:  check.SeverityFail,
		})
	} else if stmtTimeout > c.timeoutFail {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   fmt.Sprintf("%dms", stmtTimeout),
			Expected:  expectedTimeout,
			Status:    "Too high",
			Severity:  check.SeverityFail,
		})
	} else if stmtTimeout > c.timeoutWarn {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   fmt.Sprintf("%dms", stmtTimeout),
			Expected:  expectedTimeout,
			Status:    "High",
			Severity:  check.SeverityWarn,
		})
	} else {
		checks = append(checks, settingCheck{
			Role:      user,
			Parameter: "statement_timeout",
			Current:   fmt.Sprintf("%dms", stmtTimeout),
			Expected:  expectedTimeout,
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

	// Check transaction_timeout (PG17+). When the row is absent the server
	// predates PG17 and lacks the setting entirely — skip it rather than
	// false-FAILing every role.
	if txFound {
		if txTimeout == 0 {
			checks = append(checks, settingCheck{
				Role:      user,
				Parameter: "transaction_timeout",
				Current:   "0ms (disabled)",
				Expected:  expectedTimeout,
				Status:    "MUST be set (PG17+)",
				Severity:  check.SeverityFail,
			})
		} else if txTimeout > c.timeoutFail {
			checks = append(checks, settingCheck{
				Role:      user,
				Parameter: "transaction_timeout",
				Current:   fmt.Sprintf("%dms", txTimeout),
				Expected:  expectedTimeout,
				Status:    "Too high",
				Severity:  check.SeverityFail,
			})
		} else if txTimeout > c.timeoutWarn {
			checks = append(checks, settingCheck{
				Role:      user,
				Parameter: "transaction_timeout",
				Current:   fmt.Sprintf("%dms", txTimeout),
				Expected:  expectedTimeout,
				Status:    "High",
				Severity:  check.SeverityWarn,
			})
		} else {
			checks = append(checks, settingCheck{
				Role:      user,
				Parameter: "transaction_timeout",
				Current:   fmt.Sprintf("%dms", txTimeout),
				Expected:  expectedTimeout,
				Status:    "OK",
				Severity:  check.SeverityOK,
			})
		}
	}

	return checks, nil
}

func checkLogStatements(s dbSessionSettings, user string) ([]settingCheck, error) {
	var checks []settingCheck

	minDuration, _, err := s.fetch(user, "log_min_duration_statement")
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

// roles extracts unique role names from query results.
func (s dbSessionSettings) roles() []string {
	seen := map[string]struct{}{}
	var result []string
	for _, row := range s {
		if row.RoleName.Valid {
			if _, ok := seen[row.RoleName.String]; !ok {
				seen[row.RoleName.String] = struct{}{}
				result = append(result, row.RoleName.String)
			}
		}
	}
	sort.Strings(result)
	return result
}

// hasRole checks if a role exists in the query results.
func (s dbSessionSettings) hasRole(role string) bool {
	for _, row := range s {
		if row.RoleName.Valid && row.RoleName.String == role {
			return true
		}
	}
	return false
}

// fetch returns the millisecond value of a setting for a user.
// found is false when no matching row with a valid value exists — for
// version-gated settings (e.g. transaction_timeout on PG<17) the query emits no
// row at all, so absence is the only reliable "unsupported version" signal.
func (s dbSessionSettings) fetch(user, name string) (value int64, found bool, err error) {
	for _, n := range s {
		if !n.RoleName.Valid || n.RoleName.String != user {
			continue
		}
		if !n.SettingName.Valid || n.SettingName.String != name || !n.SettingValue.Valid {
			continue
		}
		ms, err := check.ParseDurationMs(n.SettingValue.String, n.Unit.String)
		if err != nil {
			return 0, false, fmt.Errorf("setting %s for user %s has invalid value %q: %w", name, user, n.SettingValue.String, err)
		}
		return ms, true, nil
	}
	return 0, false, nil
}
