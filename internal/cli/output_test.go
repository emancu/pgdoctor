package cli

import (
	"bytes"
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

func TestPrintSummary_InfoTallyComesFromFindings(t *testing.T) {
	t.Parallel()

	// An INFO finding never raises a report above PASS, so a tally that switched on
	// report severity alone could never count one and reported "2 passed".
	info := check.NewReport(check.Metadata{CheckID: "table-activity"})
	info.AddFinding(check.Finding{ID: "high-churn-tables", Name: "High Churn Tables", Severity: check.SeverityInfo})
	assert.Equal(t, check.SeverityPass, info.Severity)

	pass := check.NewReport(check.Metadata{CheckID: "pg-version"})
	pass.AddFinding(check.Finding{ID: "pg-version", Name: "PostgreSQL Version", Severity: check.SeverityPass})

	var buf bytes.Buffer
	printSummary(&buf, []*check.Report{info, pass})

	assert.Contains(t, buf.String(), "1 passed, 1 info")
}
