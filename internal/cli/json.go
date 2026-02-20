package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/emancu/pgdoctor/check"
)

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
	Headers []string     `json:"headers"`
	Rows    []jsonRow    `json:"rows"`
}

type jsonRow struct {
	Cells    []string `json:"cells"`
	Severity string   `json:"severity"`
}

func formatJSON(w io.Writer, reports []*check.Report) error {
	output := make([]jsonReport, 0, len(reports))

	for _, report := range reports {
		jr := jsonReport{
			CheckID:  report.CheckID,
			Name:     report.Name,
			Category: string(report.Category),
			Severity: severityString(report.Severity),
			Results:  make([]jsonFinding, 0, len(report.Results)),
		}

		for _, result := range report.Results {
			jf := jsonFinding{
				ID:       result.ID,
				Name:     result.Name,
				Severity: severityString(result.Severity),
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
						Severity: severityString(row.Severity),
					})
				}
				jf.Table = jt
			}

			jr.Results = append(jr.Results, jf)
		}

		output = append(output, jr)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	return nil
}

func severityString(s check.Severity) string {
	switch s {
	case check.SeverityOK:
		return "pass"
	case check.SeverityWarn:
		return "warn"
	case check.SeverityFail:
		return "fail"
	default:
		return "unknown"
	}
}
