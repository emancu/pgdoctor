// Package checktest provides shared assertions for pgdoctor check tests.
package checktest

import (
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/stretchr/testify/assert"
)

// AssertSeverityInvariant enforces the presentation invariant that no table row
// may carry a severity greater than its enclosing finding. A red (FAIL) row under
// a yellow (WARN) finding misleads operators — if the finding is WARN, nothing
// beneath it should render RED. Every check's report must satisfy this.
func AssertSeverityInvariant(t testing.TB, report *check.Report) {
	t.Helper()

	for _, finding := range report.Results {
		if finding.Table == nil {
			continue
		}
		for i, row := range finding.Table.Rows {
			assert.LessOrEqualf(t, row.Severity, finding.Severity,
				"finding %q row %d severity %v exceeds finding severity %v",
				finding.ID, i, row.Severity, finding.Severity)
		}
	}
}
