// Package checktest provides shared assertions for check unit tests.
package checktest

import (
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
)

// AssertSeverityInvariant verifies the severity contract established by
// check.NewReport and check.Report.AddFinding: the report severity is the
// maximum finding severity (never below SeverityPass). Table row severities
// do not count.
//
// A check whose findings are all SKIP is the one exception: it could not run at
// all, so it reports SKIP rather than being floored up to PASS. AddFinding only
// raises severity and SKIP sorts below PASS, so such a check assigns
// Report.Severity directly. Runner-injected SKIP reports take the same shape.
func AssertSeverityInvariant(t *testing.T, report *check.Report) {
	t.Helper()

	for _, violation := range severityViolations(report) {
		t.Error(violation)
	}
}

func severityViolations(report *check.Report) []string {
	var violations []string

	want := check.SeverityPass
	allSkipped := len(report.Results) > 0
	for _, finding := range report.Results {
		if finding.Severity > want {
			want = finding.Severity
		}
		if finding.Severity != check.SeveritySkip {
			allSkipped = false
		}
	}
	if allSkipped {
		want = check.SeveritySkip
	}
	if report.Severity != want {
		violations = append(violations, fmt.Sprintf(
			"report %q severity is %q, want %q (max finding severity, floored at %q)",
			report.CheckID, report.Severity, want, check.SeverityPass))
	}

	return violations
}
