package pgdoctor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/emancu/pgdoctor/check"
)

// The golden files in internal/cli/testdata were captured from the original
// internal/cli renderer before it was refactored away. These tests prove the
// root-package renderers reproduce that output byte-for-byte. Some goldens carry
// trailing whitespace (table cell padding) and a trailing blank line — that is the
// original output verbatim, so it stays; .gitattributes exempts these files from
// git's whitespace checks.
//
// We cover the distinct rendering paths — each detail level, table truncation,
// the skip line, hide-passing, colored output, and zero reports — rather than the
// full option matrix (scenario × detail × hide × color), which is mostly redundant
// (color only wraps cells in ANSI; hide-passing only filters).
const goldenDir = "internal/cli/testdata"

const goldenTitle = "Database Health Check: db.example.com/production"

func goldenMultiSubcheck() *check.Report {
	r := check.NewReport(check.Metadata{CheckID: "connection-efficiency", Name: "Connection Efficiency", Category: check.CategoryConfigs, SQL: "SELECT * FROM pg_stat_activity;"})
	r.Duration = 1500 * time.Millisecond
	r.AddFinding(check.Finding{ID: "sessions-idle", Name: "Idle Sessions", Severity: check.SeverityWarn, Details: "too many idle sessions\nconsider a pooler", Debug: "SELECT count(*) FROM pg_stat_activity WHERE state = 'idle';"})
	r.AddFinding(check.Finding{ID: "sessions-fatal", Name: "Fatal Sessions", Severity: check.SeverityFail, Details: "connection saturation imminent"})
	r.AddFinding(check.Finding{ID: "sessions-ok", Name: "Active Sessions", Severity: check.SeverityOK})
	return r
}

func goldenMultiCategory() []*check.Report {
	c := check.NewReport(check.Metadata{CheckID: "pg-version", Name: "PostgreSQL Version", Category: check.CategoryConfigs})
	c.Duration = 5 * time.Millisecond
	c.AddFinding(check.Finding{ID: "pg-version", Name: "PostgreSQL Version", Severity: check.SeverityOK})

	idx := check.NewReport(check.Metadata{CheckID: "invalid-indexes", Name: "Invalid Indexes", Category: check.CategoryIndexes})
	idx.Duration = 42 * time.Millisecond
	idx.AddFinding(check.Finding{ID: "invalid-indexes", Name: "Invalid Indexes", Severity: check.SeverityWarn, Details: "1 invalid index found"})

	vac := check.NewReport(check.Metadata{CheckID: "table-bloat", Name: "Table Bloat", Category: check.CategoryVacuum})
	vac.Duration = 99 * time.Millisecond
	vac.AddFinding(check.Finding{ID: "table-bloat", Name: "Table Bloat", Severity: check.SeverityOK})

	return []*check.Report{c, idx, vac}
}

func goldenSkip() *check.Report {
	r := check.NewReport(check.Metadata{CheckID: "replication-lag", Name: "Replication Lag", Category: check.CategoryConfigs})
	r.Duration = 2000 * time.Millisecond
	r.Severity = check.SeveritySkip
	r.AddFinding(check.Finding{ID: "error", Name: "Check Error", Severity: check.SeveritySkip, Details: "query cancelled by statement_timeout"})
	return r
}

func goldenWithTable() *check.Report {
	r := check.NewReport(check.Metadata{CheckID: "table-bloat", Name: "Table Bloat", Category: check.CategoryVacuum, SQL: "SELECT relname FROM pg_stat_user_tables;"})
	r.Duration = 333 * time.Millisecond
	table := &check.Table{
		Headers: []string{"Table", "Bloat", "Wasted"},
		Rows: []check.TableRow{
			{Cells: []string{"public.orders", "45%", "1.2GiB"}, Severity: check.SeverityFail},
			{Cells: []string{"public.users", "12%", "64MiB"}, Severity: check.SeverityWarn},
			{Cells: []string{"public.line_items", "3%", "8MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.a", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.b", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.c", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.d", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.e", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.f", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.g", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.h", "1%", "1MiB"}, Severity: check.SeverityOK},
			{Cells: []string{"public.i", "1%", "1MiB"}, Severity: check.SeverityOK},
		},
	}
	r.AddFinding(check.Finding{ID: "table-bloat", Name: "Table Bloat", Severity: check.SeverityFail, Details: "high bloat detected", Table: table, Debug: "bloat estimate query"})
	return r
}

func goldenFailWarnOKMix() []*check.Report {
	f := check.NewReport(check.Metadata{CheckID: "no-backups", Name: "Backups", Category: check.CategoryConfigs})
	f.Duration = 7 * time.Millisecond
	f.AddFinding(check.Finding{ID: "no-backups", Name: "Backups", Severity: check.SeverityFail, Details: "no backups configured"})

	w := check.NewReport(check.Metadata{CheckID: "minor-version", Name: "Minor Version", Category: check.CategoryConfigs})
	w.Duration = 3 * time.Millisecond
	w.AddFinding(check.Finding{ID: "minor-version", Name: "Minor Version", Severity: check.SeverityWarn, Details: "outdated minor version"})

	o := check.NewReport(check.Metadata{CheckID: "pg-version", Name: "PostgreSQL Version", Category: check.CategoryConfigs})
	o.Duration = 1 * time.Millisecond
	o.AddFinding(check.Finding{ID: "pg-version", Name: "PostgreSQL Version", Severity: check.SeverityOK})

	s := check.NewReport(check.Metadata{CheckID: "replication-slots", Name: "Replication Slots", Category: check.CategoryConfigs})
	s.Duration = 2000 * time.Millisecond
	s.Severity = check.SeveritySkip
	s.AddFinding(check.Finding{ID: "error", Name: "Check Error", Severity: check.SeveritySkip, Details: "permission denied"})

	return []*check.Report{f, w, o, s}
}

func footerHints() []string {
	return []string{
		"To see more: pgdoctor run ... --detail verbose",
		"To see how to fix: pgdoctor explain <check-id>",
	}
}

func TestTextRenderer_GoldenByteForByte(t *testing.T) {
	t.Parallel()

	// Each case targets a distinct rendering path. The golden filenames index into
	// the captured-from-original set in testdata/.
	cases := []struct {
		scenario string
		reports  []*check.Report
		detail   Detail
		hide     bool
		noColor  bool
	}{
		{"multi-category", goldenMultiCategory(), DetailSummary, false, true},  // summary line + category headers
		{"multi-category", goldenMultiCategory(), DetailSummary, false, false}, // color: ANSI wrapping
		{"multi-subcheck", []*check.Report{goldenMultiSubcheck()}, DetailBrief, false, true},
		{"multi-subcheck", []*check.Report{goldenMultiSubcheck()}, DetailVerbose, false, true},
		{"multi-subcheck", []*check.Report{goldenMultiSubcheck()}, DetailDebug, false, true}, // SQL + debug blocks
		{"with-table", []*check.Report{goldenWithTable()}, DetailBrief, false, true},         // row truncation
		{"with-table", []*check.Report{goldenWithTable()}, DetailVerbose, false, true},       // full table
		{"skip", []*check.Report{goldenSkip()}, DetailBrief, false, true},                    // skip line
		{"fail-warn-ok-mix", goldenFailWarnOKMix(), DetailBrief, true, true},                 // hide-passing filter
		{"empty", nil, DetailBrief, false, true},                                             // zero reports
	}

	for _, tc := range cases {
		name := goldenName(tc.scenario, string(tc.detail), tc.hide, tc.noColor)

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := TextOptions{Title: goldenTitle, Detail: tc.detail, HidePassing: tc.hide, NoColor: tc.noColor}
			// Footer hints are only emitted at summary/brief, matching the CLI.
			if tc.detail == DetailSummary || tc.detail == DetailBrief {
				opts.FooterHints = footerHints()
			}

			var buf bytes.Buffer
			r := NewTextRenderer(&buf, opts)
			for _, report := range tc.reports {
				r.Report(report)
			}
			require.NoError(t, r.Flush())

			require.Equal(t, string(readGolden(t, name)), buf.String(), "renderer output must match captured golden %s", name)
		})
	}
}

func TestJSONRenderer_GoldenByteForByte(t *testing.T) {
	t.Parallel()

	cases := []struct {
		scenario string
		reports  []*check.Report
	}{
		{"multi-subcheck", []*check.Report{goldenMultiSubcheck()}},
		{"with-table", []*check.Report{goldenWithTable()}},
		{"empty", nil},
	}

	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			r := NewJSONRenderer(&buf)
			for _, report := range tc.reports {
				r.Report(report)
			}
			require.NoError(t, r.Flush())

			name := fmt.Sprintf("text_%s.json.golden", tc.scenario)
			require.Equal(t, string(readGolden(t, name)), buf.String(), "JSON renderer output must match captured golden %s", name)
		})
	}
}

func TestRenderers_ImplementRendererInterface(t *testing.T) {
	t.Parallel()

	var _ Renderer = NewTextRenderer(&bytes.Buffer{}, TextOptions{})
	var _ Renderer = NewJSONRenderer(&bytes.Buffer{})
}

func TestTextRenderer_ZeroDetailMapsToBrief(t *testing.T) {
	t.Parallel()

	var zeroBuf, briefBuf bytes.Buffer

	zero := NewTextRenderer(&zeroBuf, TextOptions{Title: goldenTitle, FooterHints: footerHints()})
	brief := NewTextRenderer(&briefBuf, TextOptions{Title: goldenTitle, Detail: DetailBrief, FooterHints: footerHints()})

	for _, report := range goldenMultiCategory() {
		zero.Report(report)
		brief.Report(report)
	}
	require.NoError(t, zero.Flush())
	require.NoError(t, brief.Flush())

	require.Equal(t, briefBuf.String(), zeroBuf.String())
}

func goldenName(scenario, detail string, hide, noColor bool) string {
	parts := []string{"text", scenario, detail}
	if hide {
		parts = append(parts, "hidepassing")
	}
	if noColor {
		parts = append(parts, "nocolor")
	} else {
		parts = append(parts, "color")
	}
	return strings.Join(parts, "_") + ".golden"
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goldenDir, name))
	require.NoError(t, err)
	return data
}
