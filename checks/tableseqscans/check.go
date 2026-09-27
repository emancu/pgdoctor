// Package tableseqscans implements checks for identifying tables with excessive sequential scan activity.
package tableseqscans

import (
	"context"
	_ "embed"
	"fmt"
	"math"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	warnRowThreshold   = 10000
	warnRatioThreshold = 10.0
)

type TableSeqScansQueries interface {
	HighSeqScanTables(ctx context.Context, minRows int64) ([]db.HighSeqScanTablesRow, error)
}

type Config struct {
	HighSeqScansMinRows  int64   `yaml:"high_seq_scans_min_rows"`
	HighSeqScansMinRatio float64 `yaml:"high_seq_scans_min_ratio"`
}

func DefaultConfig() Config {
	return Config{HighSeqScansMinRows: 50000, HighSeqScansMinRatio: 50}
}

func (c Config) Validate() error {
	if c.HighSeqScansMinRows <= 0 {
		return fmt.Errorf("high_seq_scans_min_rows: %d is not a positive integer", c.HighSeqScansMinRows)
	}
	if !(c.HighSeqScansMinRatio > 0) || math.IsInf(c.HighSeqScansMinRatio, 1) {
		return fmt.Errorf("high_seq_scans_min_ratio: %v is not a positive number", c.HighSeqScansMinRatio)
	}
	return nil
}

type checker struct {
	queries      TableSeqScansQueries
	highMinRows  int64
	highMinRatio float64
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "table-seq-scans",
		Name:        "Table Sequential Scans",
		Description: "Identifies tables with excessive sequential scans that may benefit from indexes",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries TableSeqScansQueries, cfg Config) check.Checker {
	return &checker{queries: queries, highMinRows: cfg.HighSeqScansMinRows, highMinRatio: cfg.HighSeqScansMinRatio}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.HighSeqScanTables(ctx, min(warnRowThreshold, c.highMinRows))
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
		})
		return report, nil
	}

	checkHighSeqScans(rows, c.highMinRows, c.highMinRatio, report)

	return report, nil
}

func checkHighSeqScans(rows []db.HighSeqScanTablesRow, highMinRows int64, highMinRatio float64, report *check.Report) {
	var failRows []check.TableRow
	var warnRows []check.TableRow

	for _, row := range rows {
		if row.IndexCount.Int64 == 0 {
			continue
		}

		noIndexScans := !row.SeqToIdxRatio.Valid
		var ratio float64
		ratioCell := "no index scans"
		if !noIndexScans {
			r, _ := row.SeqToIdxRatio.Float64Value()
			ratio = r.Float64
			ratioCell = fmt.Sprintf("%.1f", ratio)
		}

		cells := []string{
			row.TableName.String,
			check.FormatNumber(row.SeqScan.Int64),
			check.FormatNumber(row.IdxScan.Int64),
			ratioCell,
			check.FormatNumber(row.EstimatedRows.Int64),
			check.FormatBytes(row.TableSizeBytes.Int64),
		}

		if row.EstimatedRows.Int64 >= highMinRows && (noIndexScans || ratio >= highMinRatio) {
			failRows = append(failRows, check.TableRow{Cells: cells, Severity: check.SeverityFail})
		} else if row.EstimatedRows.Int64 >= warnRowThreshold && (noIndexScans || ratio >= warnRatioThreshold) {
			warnRows = append(warnRows, check.TableRow{Cells: cells, Severity: check.SeverityWarn})
		}
	}

	headers := []string{"Table", "Seq Scans", "Idx Scans", "Ratio", "Rows", "Size"}

	if len(failRows) > 0 {
		report.AddFinding(check.Finding{
			ID:       "high-seq-scans",
			Name:     "High Sequential Scans",
			Severity: check.SeverityFail,
			Details:  fmt.Sprintf("Found %d tables with very high sequential scan ratios", len(failRows)),
			Table:    &check.Table{Headers: headers, Rows: failRows},
		})
	} else {
		report.AddFinding(check.Finding{
			ID:       "high-seq-scans",
			Name:     "High Sequential Scans",
			Severity: check.SeverityPass,
		})
	}

	if len(warnRows) > 0 {
		report.AddFinding(check.Finding{
			ID:       "moderate-seq-scans",
			Name:     "Moderate Sequential Scans",
			Severity: check.SeverityWarn,
			Details:  fmt.Sprintf("Found %d tables with elevated sequential scan ratios", len(warnRows)),
			Table:    &check.Table{Headers: headers, Rows: warnRows},
		})
	}
}
