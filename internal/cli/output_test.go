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
