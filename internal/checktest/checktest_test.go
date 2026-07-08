// White-box on purpose: violating reports are asserted through
// severityViolations, since feeding them to AssertSeverityInvariant would
// fail the test run itself.
package checktest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/emancu/pgdoctor/check"
)

func metadata() check.Metadata {
	return check.Metadata{
		CheckID:  "sample-check",
		Name:     "Sample Check",
		Category: check.CategoryConfigs,
	}
}

func reportWith(findings ...check.Finding) *check.Report {
	report := check.NewReport(metadata())
	for _, finding := range findings {
		report.AddFinding(finding)
	}
	return report
}

func TestAssertSeverityInvariant_Passing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report *check.Report
	}{
		{
			name:   "single OK finding",
			report: reportWith(check.Finding{ID: "sample-check", Severity: check.SeverityOK}),
		},
		{
			name: "info finding with info rows keeps report at OK",
			report: reportWith(check.Finding{
				ID:       "informational",
				Severity: check.SeverityInfo,
				Table: &check.Table{
					Headers: []string{"Item"},
					Rows: []check.TableRow{
						{Cells: []string{"a"}, Severity: check.SeverityInfo},
						{Cells: []string{"b"}, Severity: check.SeverityInfo},
					},
				},
			}),
		},
		{
			name: "warn finding with mixed rows at or below warn",
			report: reportWith(
				check.Finding{ID: "healthy", Severity: check.SeverityOK},
				check.Finding{
					ID:       "chronic",
					Severity: check.SeverityWarn,
					Table: &check.Table{
						Headers: []string{"Item"},
						Rows: []check.TableRow{
							{Cells: []string{"a"}, Severity: check.SeverityOK},
							{Cells: []string{"b"}, Severity: check.SeverityWarn},
						},
					},
				},
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			AssertSeverityInvariant(t, tt.report)
			require.Empty(t, severityViolations(tt.report))
		})
	}
}

func TestSeverityViolations(t *testing.T) {
	t.Parallel()

	understatedReport := reportWith(check.Finding{ID: "issue", Severity: check.SeverityWarn})
	understatedReport.Severity = check.SeverityOK

	tests := []struct {
		name   string
		report *check.Report
		expect []string
	}{
		{
			name:   "report severity below max finding severity",
			report: understatedReport,
			expect: []string{`report "sample-check" severity is "pass", want "warn"`},
		},
		{
			name: "row severity exceeds its finding",
			report: reportWith(check.Finding{
				ID:       "overstated-rows",
				Severity: check.SeverityWarn,
				Table: &check.Table{
					Headers: []string{"Item"},
					Rows: []check.TableRow{
						{Cells: []string{"a"}, Severity: check.SeverityWarn},
						{Cells: []string{"b"}, Severity: check.SeverityFail},
					},
				},
			}),
			expect: []string{`finding "overstated-rows" row 1 severity "fail" exceeds finding severity "warn"`},
		},
		{
			name: "info finding with warn row",
			report: reportWith(check.Finding{
				ID:       "informational",
				Severity: check.SeverityInfo,
				Table: &check.Table{
					Headers: []string{"Item"},
					Rows:    []check.TableRow{{Cells: []string{"a"}, Severity: check.SeverityWarn}},
				},
			}),
			expect: []string{`finding "informational" row 0 severity "warn" exceeds finding severity "info"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			violations := severityViolations(tt.report)
			require.Len(t, violations, len(tt.expect))
			for i, expect := range tt.expect {
				require.Contains(t, violations[i], expect)
			}
		})
	}
}
