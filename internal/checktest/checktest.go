// Package checktest provides shared assertions for check unit tests.
package checktest

import (
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
)

// AssertSeverityInvariant verifies the severity contract established by
// check.NewReport and check.Report.AddFinding: the report severity is the
// maximum finding severity (never below SeverityPass), and no table row is
// more severe than the finding that contains it.
//
// It covers reports built through AddFinding; runner-injected SKIP reports
// set Report.Severity directly and are outside this contract.
func AssertSeverityInvariant(t *testing.T, report *check.Report) {
	t.Helper()

	for _, violation := range severityViolations(report) {
		t.Error(violation)
	}
}

func severityViolations(report *check.Report) []string {
	var violations []string

	want := check.SeverityPass
	for _, finding := range report.Results {
		if finding.Severity > want {
			want = finding.Severity
		}
	}
	if report.Severity != want {
		violations = append(violations, fmt.Sprintf(
			"report %q severity is %q, want %q (max finding severity, floored at %q)",
			report.CheckID, report.Severity, want, check.SeverityPass))
	}

	for _, finding := range report.Results {
		if finding.Table == nil {
			continue
		}
		for i, row := range finding.Table.Rows {
			if row.Severity > finding.Severity {
				violations = append(violations, fmt.Sprintf(
					"finding %q row %d severity %q exceeds finding severity %q",
					finding.ID, i, row.Severity, finding.Severity))
			}
		}
	}

	return violations
}
