// Package indexbloat implements checks for PostgreSQL B-tree index bloat estimation.
package indexbloat

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

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

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  "No significant index bloat detected",
		})
		return report, nil
	}

	// SQL already filters to ≥30% bloat AND ≥100 MiB wasted, wasted-desc.
	// Every returned row is rendered as-is.
	tableRows := make([]check.TableRow, 0, len(rows))
	totalWasted := int64(0)
	for _, row := range rows {
		totalWasted += row.BloatBytes.Int64
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.Schemaname.String + "." + row.Tablename.String,
				row.Indexname.String,
				check.FormatBytes(row.ActualBytes.Int64),
				fmt.Sprintf("%.1f", getBloatPercent(row)),
				check.FormatBytes(row.BloatBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "bloated-indexes",
		Name:     "Bloated Indexes",
		Severity: check.SeverityWarn,
		Details: fmt.Sprintf(
			"Found %d bloated index(es), %s reclaimable via REINDEX CONCURRENTLY",
			len(rows), check.FormatBytes(totalWasted),
		),
		Table: &check.Table{
			Headers: []string{"Table", "Index", "Size", "Bloat %", "Wasted"},
			Rows:    tableRows,
		},
	})

	return report, nil
}

func getBloatPercent(row db.IndexBloatRow) float64 {
	if !row.BloatPercent.Valid {
		return 0
	}
	f, _ := row.BloatPercent.Float64Value()
	return f.Float64
}
