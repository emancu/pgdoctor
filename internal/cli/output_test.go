package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/stretchr/testify/assert"
)

// singleFindingReport builds a report whose only finding has ID == CheckID,
// which routes printCheckReport through the header-folded single-finding branch.
func singleFindingReport() *check.Report {
	report := check.NewReport(check.Metadata{CheckID: "demo", Name: "Demo Check"})
	report.AddFinding(check.Finding{
		ID:       "demo",
		Name:     "Demo Check",
		Severity: check.SeverityWarn,
		Details:  "something looks off",
		Debug:    "SELECT 1 -- debug payload",
	})
	return report
}

func TestPrintCheckReport_SingleFinding_ShowsDebugUnderDebugDetail(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printCheckReport(&buf, singleFindingReport(), &runOptions{detail: string(detailDebug)})

	out := buf.String()
	assert.Contains(t, out, "Debug:", "single-finding debug block must render under --detail debug")
	assert.Contains(t, out, "SELECT 1 -- debug payload")
}

func TestPrintCheckReport_SingleFinding_HidesDebugWithoutDebugDetail(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printCheckReport(&buf, singleFindingReport(), &runOptions{detail: string(detailBrief)})

	assert.NotContains(t, buf.String(), "Debug:", "debug must stay hidden unless --detail debug")
}

func TestPrintCheckReport_PassDetailsFollowDetailLevel(t *testing.T) {
	t.Parallel()

	single := check.NewReport(check.Metadata{CheckID: "demo", Name: "Demo Check"})
	single.AddFinding(check.Finding{ID: "demo", Name: "Demo Check", Severity: check.SeverityPass, Details: "pass figure"})

	multi := check.NewReport(check.Metadata{CheckID: "demo", Name: "Demo Check"})
	multi.AddFinding(check.Finding{ID: "one", Name: "One", Severity: check.SeverityPass, Details: "pass figure"})
	multi.AddFinding(check.Finding{ID: "two", Name: "Two", Severity: check.SeverityPass})

	tests := []struct {
		name   string
		report *check.Report
		detail detailLevel
		want   bool
	}{
		{"single finding at brief", single, detailBrief, false},
		{"single finding at verbose", single, detailVerbose, true},
		{"single finding at debug", single, detailDebug, true},
		{"subcheck at brief", multi, detailBrief, false},
		{"subcheck at verbose", multi, detailVerbose, true},
		{"subcheck at debug", multi, detailDebug, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			printCheckReport(&buf, tt.report, &runOptions{detail: string(tt.detail)})

			assert.Equal(t, tt.want, strings.Contains(buf.String(), "pass figure"))
		})
	}
}

func TestPrintCheckSummary_InfoFindingsLeaveTheTally(t *testing.T) {
	t.Parallel()

	report := check.NewReport(check.Metadata{CheckID: "cache-efficiency", Name: "Cache Efficiency"})
	report.AddFinding(check.Finding{ID: "db-cache-ratio", Name: "Database Cache Ratio", Severity: check.SeverityPass})
	report.AddFinding(check.Finding{ID: "cache-hit-ratio", Name: "Cache Hit Ratio", Severity: check.SeverityInfo})
	report.AddFinding(check.Finding{ID: "index-cache-ratio", Name: "Index Cache Ratio", Severity: check.SeverityInfo})

	var buf bytes.Buffer
	printCheckSummary(&buf, report, &runOptions{detail: string(detailSummary)})

	assert.Contains(t, buf.String(), "(1/1)", "two INFO findings must not make a healthy check read (1/3)")
}

func TestPrintCheckSummary_AllInfoFindingsHaveNoTally(t *testing.T) {
	t.Parallel()

	report := check.NewReport(check.Metadata{CheckID: "table-activity", Name: "Table Activity"})
	report.AddFinding(check.Finding{ID: "high-churn-tables", Name: "High Churn Tables", Severity: check.SeverityInfo})

	var buf bytes.Buffer
	printCheckSummary(&buf, report, &runOptions{detail: string(detailSummary)})

	assert.Equal(t, "[PASS] Table Activity (table-activity)\n", buf.String())
}

func reportWith(severities ...check.Severity) *check.Report {
	report := check.NewReport(check.Metadata{CheckID: "demo", Name: "Demo Check"})
	for i, severity := range severities {
		report.AddFinding(check.Finding{ID: fmt.Sprintf("finding-%d", i), Name: fmt.Sprintf("Finding %d", i), Severity: severity})
	}
	return report
}

func TestPrintSummary_CountsEachCheckByHeaderSeverity(t *testing.T) {
	t.Parallel()

	reports := []*check.Report{
		reportWith(check.SeverityInfo),
		reportWith(check.SeverityPass, check.SeveritySkip),
		reportWith(check.SeverityPass),
		reportWith(check.SeverityPass, check.SeverityWarn),
	}

	var buf bytes.Buffer
	printSummary(&buf, reports)

	assert.Contains(t, buf.String(), "Summary: 1 warning, 3 passed (4 checks")
}

func TestPrintSummary_Plurals(t *testing.T) {
	t.Parallel()

	var one bytes.Buffer
	printSummary(&one, []*check.Report{reportWith(check.SeverityFail)})
	assert.Contains(t, one.String(), "Summary: 1 failure (1 check in")

	var many bytes.Buffer
	printSummary(&many, []*check.Report{
		reportWith(check.SeverityFail),
		reportWith(check.SeverityFail),
		reportWith(check.SeverityWarn),
		reportWith(check.SeverityWarn),
	})
	assert.Contains(t, many.String(), "Summary: 2 failures, 2 warnings (4 checks in")
}

func TestHidden(t *testing.T) {
	t.Parallel()

	skipped := reportWith(check.SeveritySkip)
	skipped.Severity = check.SeveritySkip

	tests := []struct {
		name        string
		report      *check.Report
		hidePassing bool
		want        bool
	}{
		{"all findings passed", reportWith(check.SeverityPass, check.SeverityPass), true, true},
		{"no findings", reportWith(), true, true},
		{"flag off", reportWith(check.SeverityPass), false, false},
		{"info finding under pass header", reportWith(check.SeverityPass, check.SeverityInfo), true, false},
		{"skip finding under pass header", reportWith(check.SeverityPass, check.SeveritySkip), true, false},
		{"warn check", reportWith(check.SeverityPass, check.SeverityWarn), true, false},
		{"skipped check", skipped, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, hidden(tt.report, &runOptions{hidePassing: tt.hidePassing}))
		})
	}
}

func TestPrintCheckReport_HidePassingDropsPassFindings(t *testing.T) {
	t.Parallel()

	report := reportWith(check.SeverityPass, check.SeverityWarn, check.SeverityInfo, check.SeveritySkip)

	var buf bytes.Buffer
	printCheckReport(&buf, report, &runOptions{detail: string(detailBrief), hidePassing: true})

	out := buf.String()
	assert.Contains(t, out, "[WARN] Demo Check (demo)")
	assert.NotContains(t, out, "Finding 0")
	assert.Contains(t, out, "[WARN] Finding 1")
	assert.Contains(t, out, "[INFO] Finding 2")
	assert.Contains(t, out, "[SKIP] Finding 3")
}
