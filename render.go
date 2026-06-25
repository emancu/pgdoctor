package pgdoctor

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/fatih/color"

	"github.com/emancu/pgdoctor/check"
)

// Renderer streams check reports to an output as they complete and finalises
// the output once every report has been delivered.
//
// Report wires directly into Options.OnReport: it never errors, buffering any
// write/encode failure internally. Flush emits any trailing output (summary,
// closing array) and returns the first recorded error. Flush is safe to call
// when zero reports were delivered.
type Renderer interface {
	Report(*check.Report)
	Flush() error
}

// Detail controls how much information the text renderer emits per check.
type Detail string

const (
	DetailSummary Detail = "summary"
	DetailBrief   Detail = "brief"
	DetailVerbose Detail = "verbose"
	DetailDebug   Detail = "debug"
)

// TextOptions configures a TextRenderer.
type TextOptions struct {
	// Title is the full header line, formatted by the caller (e.g.
	// "Database Health Check: host/db"). Empty skips the header entirely.
	Title string
	// Instance is an optional instance specification block printed under the
	// title. Nil skips it. Only generic check.InstanceMetadata fields are
	// rendered; the renderer stays cloud-agnostic.
	Instance *check.InstanceMetadata
	// Detail selects the verbosity. The zero value ("") maps to DetailBrief,
	// matching the CLI's default --detail.
	Detail Detail
	// HidePassing suppresses the body of checks whose severity is OK. Category
	// headers and the final summary still account for them.
	HidePassing bool
	// NoColor disables ANSI color in the output.
	NoColor bool
	// FooterHints are dim lines printed after the summary. Nil prints none.
	FooterHints []string
}

// TextRenderer streams reports as the human-readable text report. It is
// stateful: it holds the writer, prints its head lazily on the first Report,
// and accumulates reports for the trailing summary emitted by Flush.
type TextRenderer struct {
	w    io.Writer
	opts TextOptions

	palette palette

	headerDone      bool
	currentCategory string
	reports         []*check.Report
	err             error
}

// NewTextRenderer returns a TextRenderer writing to w.
func NewTextRenderer(w io.Writer, opts TextOptions) *TextRenderer {
	if opts.Detail == "" {
		opts.Detail = DetailBrief
	}
	return &TextRenderer{
		w:       w,
		opts:    opts,
		palette: newPalette(opts.NoColor),
	}
}

func (r *TextRenderer) Report(report *check.Report) {
	r.ensureHeader()

	r.reports = append(r.reports, report)

	cat := string(report.Category)
	if cat != r.currentCategory {
		if r.currentCategory != "" {
			r.println()
		}
		title := strings.ToUpper(cat)
		r.println(title)
		r.println(strings.Repeat("─", len(title)))
		r.currentCategory = cat
	}

	if report.Severity == check.SeverityOK && r.opts.HidePassing {
		return
	}

	if r.opts.Detail == DetailSummary {
		r.printCheckSummary(report)
	} else {
		r.printCheckReport(report)
	}
}

func (r *TextRenderer) Flush() error {
	r.ensureHeader()

	r.println()
	r.printSummary()

	if r.opts.Detail == DetailSummary || r.opts.Detail == DetailBrief {
		for _, hint := range r.opts.FooterHints {
			r.printf("%s\n", r.palette.dim(hint))
		}
		if len(r.opts.FooterHints) > 0 {
			r.println()
		}
	}

	return r.err
}

// ensureHeader prints the title and optional instance block once, lazily.
func (r *TextRenderer) ensureHeader() {
	if r.headerDone {
		return
	}
	r.headerDone = true

	if r.opts.Title != "" {
		r.printf("%s\n\n", r.opts.Title)
	}
	if r.opts.Instance != nil {
		r.printInstance(r.opts.Instance)
	}
}

func (r *TextRenderer) printInstance(meta *check.InstanceMetadata) {
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, fmt.Sprintf("%s: %s", label, value))
		}
	}

	add("Instance", meta.InstanceID)
	add("Class", meta.InstanceClass)
	if meta.EngineVersion != "" {
		add("Engine", "PostgreSQL "+meta.EngineVersion)
	}
	if meta.VCPUCores > 0 {
		add("vCPUs", fmt.Sprintf("%d", meta.VCPUCores))
	}
	if meta.MemoryGB > 0 {
		add("Memory", fmt.Sprintf("%.0f GB", meta.MemoryGB))
	}
	if meta.StorageGB > 0 {
		storage := fmt.Sprintf("%d GB", meta.StorageGB)
		if meta.StorageType != "" {
			storage = fmt.Sprintf("%s %s", storage, meta.StorageType)
		}
		add("Storage", storage)
	}

	if len(lines) == 0 {
		return
	}
	for _, line := range lines {
		r.println(line)
	}
	r.println()
}

func (r *TextRenderer) showTiming() bool {
	return r.opts.Detail == DetailVerbose || r.opts.Detail == DetailDebug
}

func (r *TextRenderer) printCheckSummary(report *check.Report) {
	label, colorFunc := r.palette.severity(report.Severity)
	dimFunc := r.palette.dim

	var timingStr string
	if r.showTiming() {
		timingStr = " " + dimFunc(fmt.Sprintf("[%s]", check.FormatDurationMs(float64(report.Duration.Milliseconds()))))
	}

	if report.Severity == check.SeveritySkip && len(report.Results) > 0 {
		r.printf("%s %s %s%s — %s\n",
			colorFunc(fmt.Sprintf("[%s]", label)),
			report.Name,
			dimFunc(fmt.Sprintf("(%s)", report.CheckID)),
			timingStr,
			dimFunc(report.Results[0].Details))
		return
	}

	okCount := 0
	for _, result := range report.Results {
		if result.Severity == check.SeverityOK {
			okCount++
		}
	}
	total := len(report.Results)

	r.printf("%s %s %s %s%s\n",
		colorFunc(fmt.Sprintf("[%s]", label)),
		report.Name,
		dimFunc(fmt.Sprintf("(%s)", report.CheckID)),
		dimFunc(fmt.Sprintf("(%d/%d)", okCount, total)),
		timingStr)
}

func (r *TextRenderer) printCheckReport(report *check.Report) {
	label, colorFunc := r.palette.severity(report.Severity)
	dimFunc := r.palette.dim

	var timingStr string
	if r.showTiming() {
		timingStr = " " + dimFunc(fmt.Sprintf("[%s]", check.FormatDurationMs(float64(report.Duration.Milliseconds()))))
	}

	if report.Severity == check.SeveritySkip && len(report.Results) > 0 {
		r.printf("%s %s %s%s — %s\n",
			colorFunc(fmt.Sprintf("[%s]", label)),
			report.Name,
			dimFunc(fmt.Sprintf("(%s)", report.CheckID)),
			timingStr,
			dimFunc(report.Results[0].Details))
		return
	}

	singleFinding := len(report.Results) == 1 && report.Results[0].ID == report.CheckID

	if singleFinding {
		result := report.Results[0]
		r.printf("%s %s %s%s\n",
			colorFunc(fmt.Sprintf("[%s]", label)),
			report.Name,
			dimFunc(fmt.Sprintf("(%s)", report.CheckID)),
			timingStr)
		if result.Severity != check.SeverityOK && result.Details != "" {
			r.printf("%s\n", indentText(result.Details, 2))
		}
		if result.Table != nil {
			r.println()
			r.printTable(result.Table, 2)
		}
		if r.opts.Detail == DetailDebug && result.Debug != "" {
			r.println()
			r.println("  Debug:")
			r.printf("%s\n", indentText(result.Debug, 4))
		}
	} else {
		r.printf("%s %s %s%s\n",
			colorFunc(fmt.Sprintf("[%s]", label)),
			report.Name,
			dimFunc(fmt.Sprintf("(%s)", report.CheckID)),
			timingStr)

		sortedResults := make([]check.Finding, len(report.Results))
		copy(sortedResults, report.Results)
		sort.Slice(sortedResults, func(i, j int) bool {
			if sortedResults[i].Severity != sortedResults[j].Severity {
				return sortedResults[i].Severity < sortedResults[j].Severity
			}
			return sortedResults[i].Name < sortedResults[j].Name
		})

		for _, result := range sortedResults {
			r.printSubcheck(report, result)
		}
	}

	if r.opts.Detail == DetailDebug && report.SQL != "" {
		r.println()
		r.println("  Query:")
		r.printf("%s\n", indentText(report.SQL, 4))
	}
}

func (r *TextRenderer) printSubcheck(report *check.Report, result check.Finding) {
	label, colorFunc := r.palette.severity(result.Severity)
	dimFunc := r.palette.dim

	fullID := report.CheckID
	if result.ID != report.CheckID {
		fullID = report.CheckID + "/" + result.ID
	}

	r.printf("%s %s %s\n",
		colorFunc(fmt.Sprintf("[%s]", label)),
		result.Name,
		dimFunc(fmt.Sprintf("(%s)", fullID)))

	if result.Severity != check.SeverityOK && result.Details != "" {
		r.printf("%s\n", indentText(result.Details, 2))
	}

	if result.Table != nil {
		r.println()
		r.printTable(result.Table, 2)
	}

	if r.opts.Detail == DetailDebug && result.Debug != "" {
		r.println()
		r.println("  Debug:")
		r.printf("%s\n", indentText(result.Debug, 4))
	}
}

func (r *TextRenderer) printTable(table *check.Table, indentSpaces int) {
	if len(table.Rows) == 0 {
		return
	}

	indentStr := strings.Repeat(" ", indentSpaces)

	const maxRowsBrief = 10
	totalRows := len(table.Rows)
	rowsToShow := table.Rows
	truncated := false

	if r.opts.Detail == DetailBrief && totalRows > maxRowsBrief {
		rowsToShow = table.Rows[:maxRowsBrief]
		truncated = true
	}

	widths := make([]int, len(table.Headers))
	for i, header := range table.Headers {
		widths[i] = len(header)
	}
	for _, row := range table.Rows {
		for i, cell := range row.Cells {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	r.print(indentStr)
	for i, header := range table.Headers {
		r.printf("%-*s  ", widths[i], header)
	}
	r.println()

	r.print(indentStr)
	for _, width := range widths {
		r.print(strings.Repeat("─", width), "  ")
	}
	r.println()

	for _, row := range rowsToShow {
		colorFunc := r.palette.severityColor(row.Severity)

		r.print(indentStr)
		for i, cell := range row.Cells {
			r.printf("%s  ", colorFunc(fmt.Sprintf("%-*s", widths[i], cell)))
		}
		r.println()
	}

	if truncated {
		dimFunc := r.palette.dim
		r.println()
		r.printf("%s%s\n", indentStr,
			dimFunc(fmt.Sprintf("(showing %d of %d rows, use --detail verbose to see all)", maxRowsBrief, totalRows)))
	}
}

func (r *TextRenderer) printSummary() {
	okCount, warnCount, failCount, skipCount := 0, 0, 0, 0
	var totalMs int64
	for _, report := range r.reports {
		totalMs += report.Duration.Milliseconds()
		switch report.Severity {
		case check.SeverityOK:
			okCount++
		case check.SeverityWarn:
			warnCount++
		case check.SeverityFail:
			failCount++
		case check.SeveritySkip:
			skipCount++
		}
	}

	r.println(strings.Repeat("━", 70))

	var summaryParts []string
	if failCount > 0 {
		summaryParts = append(summaryParts, r.palette.severityColor(check.SeverityFail)(fmt.Sprintf("%d failures", failCount)))
	}
	if warnCount > 0 {
		summaryParts = append(summaryParts, r.palette.severityColor(check.SeverityWarn)(fmt.Sprintf("%d warnings", warnCount)))
	}
	if okCount > 0 {
		summaryParts = append(summaryParts, r.palette.severityColor(check.SeverityOK)(fmt.Sprintf("%d passed", okCount)))
	}
	if skipCount > 0 {
		summaryParts = append(summaryParts, r.palette.severityColor(check.SeveritySkip)(fmt.Sprintf("%d skipped", skipCount)))
	}

	dimFunc := r.palette.dim
	r.printf("Summary: %s %s\n", strings.Join(summaryParts, ", "),
		dimFunc(fmt.Sprintf("(%d checks in %s)", len(r.reports), check.FormatDurationMs(float64(totalMs)))))
	r.println()
}

// buffered write helpers record the first error and stop emitting further output.

func (r *TextRenderer) printf(format string, a ...any) {
	if r.err != nil {
		return
	}
	if _, err := fmt.Fprintf(r.w, format, a...); err != nil {
		r.err = err
	}
}

func (r *TextRenderer) print(a ...any) {
	if r.err != nil {
		return
	}
	if _, err := fmt.Fprint(r.w, a...); err != nil {
		r.err = err
	}
}

func (r *TextRenderer) println(a ...any) {
	if r.err != nil {
		return
	}
	if _, err := fmt.Fprintln(r.w, a...); err != nil {
		r.err = err
	}
}

// JSONRenderer streams reports as a single JSON array matching the CLI's
// historical bare-array shape ([{check_id,name,category,severity,results}]).
// It buffers reports and encodes them on Flush so the output is a well-formed
// array; Flush on zero reports emits "[]\n".
type JSONRenderer struct {
	w       io.Writer
	reports []*check.Report
	err     error
}

// NewJSONRenderer returns a JSONRenderer writing to w.
func NewJSONRenderer(w io.Writer) *JSONRenderer {
	return &JSONRenderer{w: w}
}

func (r *JSONRenderer) Report(report *check.Report) {
	r.reports = append(r.reports, report)
}

func (r *JSONRenderer) Flush() error {
	if r.err != nil {
		return r.err
	}

	output := make([]jsonReport, 0, len(r.reports))
	for _, report := range r.reports {
		jr := jsonReport{
			CheckID:  report.CheckID,
			Name:     report.Name,
			Category: string(report.Category),
			Severity: report.Severity.String(),
			Results:  make([]jsonFinding, 0, len(report.Results)),
		}

		for _, result := range report.Results {
			jf := jsonFinding{
				ID:       result.ID,
				Name:     result.Name,
				Severity: result.Severity.String(),
				Details:  result.Details,
			}

			if result.Table != nil {
				jt := &jsonTable{
					Headers: result.Table.Headers,
					Rows:    make([]jsonRow, 0, len(result.Table.Rows)),
				}
				for _, row := range result.Table.Rows {
					jt.Rows = append(jt.Rows, jsonRow{
						Cells:    row.Cells,
						Severity: row.Severity.String(),
					})
				}
				jf.Table = jt
			}

			jr.Results = append(jr.Results, jf)
		}

		output = append(output, jr)
	}

	enc := json.NewEncoder(r.w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		r.err = fmt.Errorf("encoding JSON: %w", err)
		return r.err
	}

	return nil
}

type jsonReport struct {
	CheckID  string        `json:"check_id"`
	Name     string        `json:"name"`
	Category string        `json:"category"`
	Severity string        `json:"severity"`
	Results  []jsonFinding `json:"results"`
}

type jsonFinding struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Severity string     `json:"severity"`
	Details  string     `json:"details,omitempty"`
	Table    *jsonTable `json:"table,omitempty"`
}

type jsonTable struct {
	Headers []string  `json:"headers"`
	Rows    []jsonRow `json:"rows"`
}

type jsonRow struct {
	Cells    []string `json:"cells"`
	Severity string   `json:"severity"`
}

// palette holds pre-resolved color functions so the renderer is self-contained:
// color choice depends only on TextOptions.NoColor, never the process-global
// fatih/color.NoColor, which keeps output deterministic under test capture.
type palette struct {
	noColor  bool
	dim      func(string) string
	ok       func(string) string
	warn     func(string) string
	fail     func(string) string
	skip     func(string) string
	identity func(string) string
}

func newPalette(noColor bool) palette {
	identity := func(s string) string { return s }
	if noColor {
		return palette{
			noColor:  true,
			dim:      identity,
			ok:       identity,
			warn:     identity,
			fail:     identity,
			skip:     identity,
			identity: identity,
		}
	}

	return palette{
		noColor:  false,
		dim:      sprintFunc(color.New(color.Faint)),
		ok:       sprintFunc(color.New(color.FgGreen)),
		warn:     sprintFunc(color.New(color.FgYellow)),
		fail:     sprintFunc(color.New(color.FgRed)),
		skip:     sprintFunc(color.New(color.FgMagenta)),
		identity: identity,
	}
}

// sprintFunc forces the color on for this instance (overriding the global
// fatih/color.NoColor) and returns its string wrapper.
func sprintFunc(c *color.Color) func(string) string {
	c.EnableColor()
	fn := c.SprintFunc()
	return func(s string) string { return fn(s) }
}

func (p palette) severityColor(severity check.Severity) func(string) string {
	switch severity {
	case check.SeverityOK:
		return p.ok
	case check.SeverityWarn:
		return p.warn
	case check.SeverityFail:
		return p.fail
	case check.SeveritySkip:
		return p.skip
	default:
		return p.identity
	}
}

func (p palette) severity(severity check.Severity) (string, func(string) string) {
	switch severity {
	case check.SeverityOK:
		return "PASS", p.ok
	case check.SeverityWarn:
		return "WARN", p.warn
	case check.SeverityFail:
		return "FAIL", p.fail
	default:
		return strings.ToUpper(severity.String()), p.severityColor(severity)
	}
}

func indentText(text string, spaces int) string {
	lines := strings.Split(text, "\n")
	indented := make([]string, len(lines))
	indentStr := strings.Repeat(" ", spaces)

	for i, line := range lines {
		if line != "" {
			indented[i] = indentStr + line
		}
	}

	return strings.Join(indented, "\n")
}
