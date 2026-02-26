package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/fatih/color"

	"github.com/emancu/pgdoctor/check"
)

func formatReport(w io.Writer, reports []*check.Report, opts *runOptions, dbLabel string) check.Severity {
	fmt.Fprintf(w, "Database Health Check: %s\n\n", dbLabel)

	grouped := groupByCategory(reports)
	maxSeverity := check.SeverityOK

	for _, category := range sortedCategories(grouped) {
		categoryReports := grouped[category]

		sort.Slice(categoryReports, func(i, j int) bool {
			if categoryReports[i].Severity != categoryReports[j].Severity {
				return categoryReports[i].Severity > categoryReports[j].Severity
			}
			return categoryReports[i].CheckID < categoryReports[j].CheckID
		})

		hasVisibleReports := false
		for _, report := range categoryReports {
			if report.Severity > maxSeverity {
				maxSeverity = report.Severity
			}
			if report.Severity != check.SeverityOK || !opts.hidePassing {
				hasVisibleReports = true
			}
		}

		if !hasVisibleReports {
			continue
		}

		categoryTitle := strings.ToUpper(category)
		fmt.Fprintln(w, categoryTitle)
		fmt.Fprintln(w, strings.Repeat("─", len(categoryTitle)))

		for _, report := range categoryReports {
			if report.Severity == check.SeverityOK && opts.hidePassing {
				continue
			}

			if opts.detail == string(detailSummary) {
				printCheckSummary(w, report, opts)
			} else {
				printCheckReport(w, report, opts)
			}
		}

		fmt.Fprintln(w)
	}

	printSummary(w, reports)

	if opts.detail == string(detailSummary) {
		dimFunc := dimColor()
		fmt.Fprintf(w, "%s\n", dimFunc("To see details: pgdoctor run ... --detail brief"))
		fmt.Fprintf(w, "%s\n", dimFunc("To see how to fix: pgdoctor explain <check-id>"))
		fmt.Fprintln(w)
	}

	return maxSeverity
}

func groupByCategory(reports []*check.Report) map[string][]*check.Report {
	grouped := map[string][]*check.Report{}
	for _, report := range reports {
		category := string(report.Category)
		grouped[category] = append(grouped[category], report)
	}
	return grouped
}

func sortedCategories(grouped map[string][]*check.Report) []string {
	categories := make([]string, 0, len(grouped))
	for category := range grouped {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	return categories
}

func printCheckSummary(w io.Writer, report *check.Report, opts *runOptions) {
	label, colorFunc := severityDisplay(report.Severity)
	dimFunc := dimColor()

	okCount := 0
	for _, result := range report.Results {
		if result.Severity == check.SeverityOK {
			okCount++
		}
	}
	total := len(report.Results)

	fmt.Fprintf(w, "%s %s %s %s\n",
		colorFunc(fmt.Sprintf("[%s]", label)),
		report.Name,
		dimFunc(fmt.Sprintf("(%s)", report.CheckID)),
		dimFunc(fmt.Sprintf("(%d/%d)", okCount, total)))
}

func printCheckReport(w io.Writer, report *check.Report, opts *runOptions) {
	label, colorFunc := severityDisplay(report.Severity)
	dimFunc := dimColor()

	singleFindingDuplicate := len(report.Results) == 1 && report.Results[0].ID == report.CheckID

	if !singleFindingDuplicate {
		fmt.Fprintf(w, "%s %s %s\n",
			colorFunc(fmt.Sprintf("[%s]", label)),
			report.Name,
			dimFunc(fmt.Sprintf("(%s)", report.CheckID)))
	}

	sortedResults := make([]check.Finding, len(report.Results))
	copy(sortedResults, report.Results)
	sort.Slice(sortedResults, func(i, j int) bool {
		if sortedResults[i].Severity != sortedResults[j].Severity {
			return sortedResults[i].Severity < sortedResults[j].Severity
		}
		return sortedResults[i].Name < sortedResults[j].Name
	})

	for _, result := range sortedResults {
		printSubcheck(w, report, result, opts)
	}

	if opts.detail == string(detailDebug) && report.SQL != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Query:")
		fmt.Fprintf(w, "%s\n", indent(report.SQL, 4))
	}
}

func printSubcheck(w io.Writer, report *check.Report, result check.Finding, opts *runOptions) {
	label, colorFunc := severityDisplay(result.Severity)
	dimFunc := dimColor()

	fullID := report.CheckID
	if result.ID != report.CheckID {
		fullID = report.CheckID + "/" + result.ID
	}

	fmt.Fprintf(w, "%s %s %s\n",
		colorFunc(fmt.Sprintf("[%s]", label)),
		result.Name,
		dimFunc(fmt.Sprintf("(%s)", fullID)))

	if result.Severity != check.SeverityOK && result.Details != "" {
		fmt.Fprintf(w, "%s\n", indent(result.Details, 2))
	}

	if result.Table != nil {
		fmt.Fprintln(w)
		printTable(w, result.Table, 2, opts)
	}

	if opts.detail == string(detailDebug) && result.Debug != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Debug:")
		fmt.Fprintf(w, "%s\n", indent(result.Debug, 4))
	}
}

func printTable(w io.Writer, table *check.Table, indentSpaces int, opts *runOptions) {
	if len(table.Rows) == 0 {
		return
	}

	indentStr := strings.Repeat(" ", indentSpaces)

	const maxRowsBrief = 10
	totalRows := len(table.Rows)
	rowsToShow := table.Rows
	truncated := false

	if opts.detail == string(detailBrief) && totalRows > maxRowsBrief {
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

	fmt.Fprint(w, indentStr)
	for i, header := range table.Headers {
		fmt.Fprintf(w, "%-*s  ", widths[i], header)
	}
	fmt.Fprintln(w)

	fmt.Fprint(w, indentStr)
	for _, width := range widths {
		fmt.Fprint(w, strings.Repeat("─", width), "  ")
	}
	fmt.Fprintln(w)

	for _, row := range rowsToShow {
		colorFunc := colorForSeverity(row.Severity)

		fmt.Fprint(w, indentStr)
		for i, cell := range row.Cells {
			fmt.Fprintf(w, "%s  ", colorFunc(fmt.Sprintf("%-*s", widths[i], cell)))
		}
		fmt.Fprintln(w)
	}

	if truncated {
		dimFunc := dimColor()
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s%s\n", indentStr,
			dimFunc(fmt.Sprintf("(showing %d of %d rows, use --detail verbose to see all)", maxRowsBrief, totalRows)))
	}
}

func printSummary(w io.Writer, reports []*check.Report) {
	okCount, warnCount, failCount := 0, 0, 0
	for _, report := range reports {
		switch report.Severity {
		case check.SeverityOK:
			okCount++
		case check.SeverityWarn:
			warnCount++
		case check.SeverityFail:
			failCount++
		}
	}

	fmt.Fprintln(w, strings.Repeat("━", 70))

	var summaryParts []string
	if failCount > 0 {
		summaryParts = append(summaryParts, colorForSeverity(check.SeverityFail)(fmt.Sprintf("%d failures", failCount)))
	}
	if warnCount > 0 {
		summaryParts = append(summaryParts, colorForSeverity(check.SeverityWarn)(fmt.Sprintf("%d warnings", warnCount)))
	}
	if okCount > 0 {
		summaryParts = append(summaryParts, colorForSeverity(check.SeverityOK)(fmt.Sprintf("%d passed", okCount)))
	}

	fmt.Fprintf(w, "Summary: %s\n", strings.Join(summaryParts, ", "))
	fmt.Fprintln(w)
}

func severityDisplay(severity check.Severity) (string, func(string) string) {
	switch severity {
	case check.SeverityOK:
		return "PASS", colorForSeverity(severity)
	case check.SeverityWarn:
		return "WARN", colorForSeverity(severity)
	case check.SeverityFail:
		return "FAIL", colorForSeverity(severity)
	default:
		return strings.ToUpper(severity.String()), colorForSeverity(severity)
	}
}

func colorForSeverity(severity check.Severity) func(string) string {
	if color.NoColor {
		return func(s string) string { return s }
	}

	switch severity {
	case check.SeverityOK:
		fn := color.New(color.FgGreen).SprintFunc()
		return func(s string) string { return fn(s) }
	case check.SeverityWarn:
		fn := color.New(color.FgYellow).SprintFunc()
		return func(s string) string { return fn(s) }
	case check.SeverityFail:
		fn := color.New(color.FgRed).SprintFunc()
		return func(s string) string { return fn(s) }
	default:
		return func(s string) string { return s }
	}
}

func dimColor() func(string) string {
	if color.NoColor {
		return func(s string) string { return s }
	}
	fn := color.New(color.Faint).SprintFunc()
	return func(s string) string { return fn(s) }
}

func indent(text string, spaces int) string {
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
