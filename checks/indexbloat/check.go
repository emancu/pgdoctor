// Package indexbloat implements checks for PostgreSQL B-tree index bloat estimation.
package indexbloat

import (
	"context"
	_ "embed"
	"fmt"
	"sort"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	listPctThreshold = 50.0
	warnPctThreshold = 70.0
	warnWastedBytes  = 2 * check.GiB
)

type IndexBloatQueries interface {
	IndexBloat(context.Context) ([]db.IndexBloatRow, error)
}

type checker struct {
	queries IndexBloatQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryIndexes,
		CheckID:     "index-bloat",
		Name:        "Index Bloat",
		Description: "Estimates B-tree index bloat to identify indexes needing maintenance",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries IndexBloatQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.IndexBloat(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryIndexes, report.CheckID, err)
	}

	var bloated []db.IndexBloatRow
	for _, row := range rows {
		if check.NumericToFloat64(row.BloatPercent) >= listPctThreshold || row.BloatBytes.Int64 >= warnWastedBytes {
			bloated = append(bloated, row)
		}
	}

	if len(bloated) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityPass,
			Details:  "No bloated indexes detected",
		})
		return report, nil
	}

	sort.Slice(bloated, func(i, j int) bool {
		si, sj := rowSeverity(bloated[i]), rowSeverity(bloated[j])
		if si != sj {
			return si > sj
		}
		return bloated[i].BloatBytes.Int64 > bloated[j].BloatBytes.Int64
	})

	warnCount := 0
	tableRows := make([]check.TableRow, 0, len(bloated))
	for _, row := range bloated {
		severity := rowSeverity(row)
		if severity == check.SeverityWarn {
			warnCount++
		}
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				row.Indexname.String,
				check.FormatBytes(row.ActualBytes.Int64),
				check.FormatBytes(row.BloatBytes.Int64),
				fmt.Sprintf("%.1f%%", check.NumericToFloat64(row.BloatPercent)),
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       report.CheckID,
		Name:     report.Name,
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("%d bloated index(es) (%d with >=2GiB wasted or >=70%% bloat)", len(bloated), warnCount),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Size", "Wasted", "Bloat %"},
			Rows:    tableRows,
		},
	})

	return report, nil
}

func rowSeverity(row db.IndexBloatRow) check.Severity {
	if row.BloatBytes.Int64 >= warnWastedBytes || check.NumericToFloat64(row.BloatPercent) >= warnPctThreshold {
		return check.SeverityWarn
	}
	return check.SeverityInfo
}
