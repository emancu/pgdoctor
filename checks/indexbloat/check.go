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

	// Run subchecks
	checkHighBloatIndexes(rows, report)
	checkLargeBloatedIndexes(rows, report)

	return report, nil
}

// checkHighBloatIndexes flags indexes whose bloat exceeds 50% and whose size
// exceeds 1GB. Qualifying indexes are listed first (worst-first), followed by
// the remaining bloated-but-below-threshold indexes so brief output (capped at
// 10 rows) surfaces the actionable ones while verbose reveals the tail.
func checkHighBloatIndexes(rows []db.IndexBloatRow, report *check.Report) {
	const oneGB = int64(1024 * 1024 * 1024)

	qualifies := func(row db.IndexBloatRow) bool {
		return getBloatPercent(row) > 50 && row.ActualBytes.Int64 > oneGB
	}

	qualifying := 0
	for _, row := range rows {
		if qualifies(row) {
			qualifying++
		}
	}

	if qualifying == 0 {
		report.AddFinding(check.Finding{
			ID:       "high-bloat",
			Name:     "Index Bloat Percentage",
			Severity: check.SeverityOK,
			Details:  "No indexes with excessive bloat (>50% and >1GB) detected",
		})
		return
	}

	ordered := make([]db.IndexBloatRow, len(rows))
	copy(ordered, rows)
	sort.SliceStable(ordered, func(i, j int) bool {
		qi, qj := qualifies(ordered[i]), qualifies(ordered[j])
		if qi != qj {
			return qi
		}
		return getBloatPercent(ordered[i]) > getBloatPercent(ordered[j])
	})

	headers := []string{"Table", "Index", "Bloat %", "Bloat Size", "Actual Size"}
	tableRows := make([]check.TableRow, 0, len(ordered))
	for _, row := range ordered {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.Tablename.String,
				row.Indexname.String,
				fmt.Sprintf("%.1f%%", getBloatPercent(row)),
				check.FormatBytes(row.BloatBytes.Int64),
				check.FormatBytes(row.ActualBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "high-bloat",
		Name:     "Index Bloat Percentage",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d index(es) with high bloat (>50%% and >1GB)", qualifying),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

// checkLargeBloatedIndexes identifies large indexes with notable bloat.
func checkLargeBloatedIndexes(rows []db.IndexBloatRow, report *check.Report) {
	const oneGB = int64(1024 * 1024 * 1024)
	const oneHundredMB = int64(100 * 1024 * 1024)

	var critical []db.IndexBloatRow // >1GB bloat
	var warning []db.IndexBloatRow  // >100MB bloat

	for _, row := range rows {
		bloatBytes := row.BloatBytes.Int64
		pct := getBloatPercent(row)

		// Only consider if bloat is at least 30% to avoid false positives
		if pct < 30 {
			continue
		}

		if bloatBytes >= oneGB {
			critical = append(critical, row)
		} else if bloatBytes >= oneHundredMB {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-bloat",
			Name:     "Large Bloated Indexes",
			Severity: check.SeverityOK,
			Details:  "No large bloated indexes (>100MB wasted) detected",
		})
		return
	}

	headers := []string{"Table", "Index", "Bloat Size", "Bloat %", "Actual Size"}
	var tableRows []check.TableRow

	for _, row := range critical {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.Tablename.String,
				row.Indexname.String,
				check.FormatBytes(row.BloatBytes.Int64),
				fmt.Sprintf("%.1f%%", getBloatPercent(row)),
				check.FormatBytes(row.ActualBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	for _, row := range warning {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.Tablename.String,
				row.Indexname.String,
				check.FormatBytes(row.BloatBytes.Int64),
				fmt.Sprintf("%.1f%%", getBloatPercent(row)),
				check.FormatBytes(row.ActualBytes.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	totalWasted := int64(0)
	for _, row := range append(critical, warning...) {
		totalWasted += row.BloatBytes.Int64
	}

	report.AddFinding(check.Finding{
		ID:       "large-bloat",
		Name:     "Large Bloated Indexes",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d index(es) wasting significant disk space (total: %s)", len(critical)+len(warning), check.FormatBytes(totalWasted)),
		Table: &check.Table{
			Headers: headers,
			Rows:    tableRows,
		},
	})
}

func getBloatPercent(row db.IndexBloatRow) float64 {
	if !row.BloatPercent.Valid {
		return 0
	}
	f, _ := row.BloatPercent.Float64Value()
	return f.Float64
}
