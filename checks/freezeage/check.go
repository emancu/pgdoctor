// Package freezeage implements checks for PostgreSQL transaction ID wraparound risk.
package freezeage

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

type FreezeAgeQueries interface {
	DatabaseFreezeAge(context.Context) ([]db.DatabaseFreezeAgeRow, error)
	TableFreezeAge(context.Context) ([]db.TableFreezeAgeRow, error)
}

type checker struct {
	queries FreezeAgeQueries
}

const (
	// Transaction ID age thresholds.
	// PostgreSQL will force shutdown at ~2 billion to prevent wraparound.
	// autovacuum_freeze_max_age default is 200 million.
	ageWarnThreshold = int64(500_000_000)   // 500 million - getting concerning
	ageFailThreshold = int64(1_000_000_000) // 1 billion - emergency action needed

	// Table-level thresholds (lower since tables can be vacuumed individually).
	tableAgeWarnThreshold = int64(400_000_000)
	tableAgeFailThreshold = int64(800_000_000)
)

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryVacuum,
		CheckID:     "freeze-age",
		Name:        "Transaction ID Freeze Age",
		Description: "Monitors transaction ID age to prevent wraparound issues",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries FreezeAgeQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	dbRows, err := c.queries.DatabaseFreezeAge(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (database): %w", check.CategoryVacuum, report.CheckID, err)
	}

	tableRows, err := c.queries.TableFreezeAge(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (tables): %w", check.CategoryVacuum, report.CheckID, err)
	}

	// Run subchecks.
	checkDatabaseFreezeAge(dbRows, report)
	checkTableFreezeAge(tableRows, report)

	return report, nil
}

func checkDatabaseFreezeAge(rows []db.DatabaseFreezeAgeRow, report *check.Report) {
	var critical []db.DatabaseFreezeAgeRow
	var warning []db.DatabaseFreezeAgeRow

	for _, row := range rows {
		age := int64(row.FreezeAge.Int32)
		if age >= ageFailThreshold {
			critical = append(critical, row)
		} else if age >= ageWarnThreshold {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		// Find the oldest database for informational reporting.
		var oldestAge int64
		var oldestDB string
		for _, row := range rows {
			if int64(row.FreezeAge.Int32) > oldestAge {
				oldestAge = int64(row.FreezeAge.Int32)
				oldestDB = row.DatabaseName.String
			}
		}

		report.AddFinding(check.Finding{
			ID:       "database-freeze-age",
			Name:     "Database Freeze Age",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All databases within safe range. Oldest: %s at %s transactions", oldestDB, formatAge(oldestAge)),
		})
		return
	}

	var tableRows []check.TableRow

	for _, row := range critical {
		age := int64(row.FreezeAge.Int32)
		percentToLimit := float64(age) / float64(2_000_000_000) * 100
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.DatabaseName.String,
				formatAge(age),
				fmt.Sprintf("%.1f%%", percentToLimit),
				formatAge(row.FreezeMaxAge.Int64),
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		age := int64(row.FreezeAge.Int32)
		percentToLimit := float64(age) / float64(2_000_000_000) * 100
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.DatabaseName.String,
				formatAge(age),
				fmt.Sprintf("%.1f%%", percentToLimit),
				formatAge(row.FreezeMaxAge.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	severity := check.SeverityWarn
	if len(critical) > 0 {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "database-freeze-age",
		Name:     "Database Freeze Age",
		Severity: severity,
		Details:  fmt.Sprintf("Found %d database(s) with high transaction ID age", len(critical)+len(warning)),
		Table: &check.Table{
			Headers: []string{"Database", "Age", "% to Limit", "Freeze Max Age"},
			Rows:    tableRows,
		},
	})
}

func checkTableFreezeAge(rows []db.TableFreezeAgeRow, report *check.Report) {
	var critical []db.TableFreezeAgeRow
	var warning []db.TableFreezeAgeRow

	for _, row := range rows {
		age := int64(row.FreezeAge.Int32)
		if age >= tableAgeFailThreshold {
			critical = append(critical, row)
		} else if age >= tableAgeWarnThreshold {
			warning = append(warning, row)
		}
	}

	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "table-freeze-age",
			Name:     "Table Freeze Age",
			Severity: check.SeverityOK,
			Details:  "All tables within safe transaction ID age range",
		})
		return
	}

	var tableRows []check.TableRow

	for _, row := range critical {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				formatAge(int64(row.FreezeAge.Int32)),
				check.FormatBytes(row.TableSizeBytes.Int64),
				formatVacuumTime(row),
				fmt.Sprintf("%d", row.AutovacuumCount.Int64+row.VacuumCount.Int64),
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				formatAge(int64(row.FreezeAge.Int32)),
				check.FormatBytes(row.TableSizeBytes.Int64),
				formatVacuumTime(row),
				fmt.Sprintf("%d", row.AutovacuumCount.Int64+row.VacuumCount.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	severity := check.SeverityWarn
	if len(critical) > 0 {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "table-freeze-age",
		Name:     "Table Freeze Age",
		Severity: severity,
		Details:  fmt.Sprintf("Found %d table(s) with high transaction ID age", len(critical)+len(warning)),
		Table: &check.Table{
			Headers: []string{"Table", "Age", "Size", "Last Vacuum", "Vacuum Count"},
			Rows:    tableRows,
		},
	})
}

// Helper functions.

func formatAge(age int64) string {
	if age >= 1_000_000_000 {
		return fmt.Sprintf("%.2fB", float64(age)/1_000_000_000)
	}
	if age >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(age)/1_000_000)
	}
	if age >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(age)/1_000)
	}
	return fmt.Sprintf("%d", age)
}

func formatVacuumTime(row db.TableFreezeAgeRow) string {
	if row.LastAutovacuum.Valid {
		return row.LastAutovacuum.Time.Format("2006-01-02 15:04")
	}
	if row.LastVacuum.Valid {
		return row.LastVacuum.Time.Format("2006-01-02 15:04") + " (manual)"
	}
	return "never"
}
