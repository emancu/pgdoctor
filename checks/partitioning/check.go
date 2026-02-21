// Package partitioning implements checks for table partitioning compliance.
package partitioning

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

type PartitioningQueries interface {
	LargeTables(context.Context) ([]db.LargeTablesRow, error)
}

type checker struct {
	queries PartitioningQueries
}

const (
	// Regular tables thresholds.
	largeTableFailRows = int64(50_000_000) // MUST be partitioned
	largeTableWarnRows = int64(25_000_000) // approaching threshold

	// Activity-aware tables use lower thresholds because they benefit more from partitioning.
	activityAwareFailRows = int64(25_000_000)
	activityAwareWarnRows = int64(10_000_000)

	// Activity thresholds for determining table write patterns.
	insertHeavyRatio = 0.80 // >80% of DML operations are inserts
	highDeleteRatio  = 0.20 // >20% deletes relative to inserts
)

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategorySchema,
		CheckID:     "partitioning",
		Name:        "Table Partitioning",
		Description: "Validates large and transient tables are properly partitioned",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries PartitioningQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.LargeTables(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategorySchema, report.CheckID, err)
	}

	var largeUnpartitioned []db.LargeTablesRow
	var transientUnpartitioned []db.LargeTablesRow
	var inefficientPartitions []db.LargeTablesRow

	for _, row := range rows {
		// Check if this is a partition with too many rows (inefficient partitioning)
		if row.IsPartition.Valid && row.IsPartition.Bool {
			inefficientPartitions = append(inefficientPartitions, row)
			continue
		}

		// Skip tables that are already partitioned parents
		if row.IsPartitioned.Valid && row.IsPartitioned.Bool {
			continue
		}

		if row.IsTransient.Valid && row.IsTransient.Bool {
			transientUnpartitioned = append(transientUnpartitioned, row)
		} else {
			largeUnpartitioned = append(largeUnpartitioned, row)
		}
	}

	// Run subchecks.
	checkLargeUnpartitioned(largeUnpartitioned, report)
	checkTransientUnpartitioned(transientUnpartitioned, report)
	checkInefficientPartitions(inefficientPartitions, report)

	return report, nil
}

// isInsertHeavy returns true if >80% of DML operations are inserts.
func isInsertHeavy(row db.LargeTablesRow) bool {
	ins := row.NTupIns.Int64
	upd := row.NTupUpd.Int64
	del := row.NTupDel.Int64
	total := ins + upd + del
	if total == 0 {
		return false
	}
	return float64(ins)/float64(total) > insertHeavyRatio
}

// isHighDelete returns true if deletes are >20% of inserts.
func isHighDelete(row db.LargeTablesRow) bool {
	ins := row.NTupIns.Int64
	del := row.NTupDel.Int64
	if ins == 0 {
		return false
	}
	return float64(del)/float64(ins) > highDeleteRatio
}

// isActivityAware returns true if the table qualifies for lower thresholds.
func isActivityAware(row db.LargeTablesRow) bool {
	return isInsertHeavy(row) || isHighDelete(row)
}

// activityReason returns a human-readable reason for activity-aware flagging.
func activityReason(row db.LargeTablesRow) string {
	if isInsertHeavy(row) {
		return "Insert-heavy"
	}
	if isHighDelete(row) {
		return "High-delete"
	}
	return "Large table"
}

func checkLargeUnpartitioned(rows []db.LargeTablesRow, report *check.Report) {
	var critical []db.LargeTablesRow
	var warning []db.LargeTablesRow

	for _, row := range rows {
		estRows := row.EstimatedRows.Int64

		// Use lower thresholds for insert-heavy/high-delete tables.
		warnThreshold := largeTableWarnRows
		failThreshold := largeTableFailRows
		if isActivityAware(row) {
			warnThreshold = activityAwareWarnRows
			failThreshold = activityAwareFailRows
		}

		if estRows >= failThreshold {
			critical = append(critical, row)
		} else if estRows >= warnThreshold {
			warning = append(warning, row)
		}
	}

	// If no tables meet either threshold, report OK.
	if len(critical) == 0 && len(warning) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-unpartitioned",
			Name:     "Large Unpartitioned Tables",
			Severity: check.SeverityOK,
			Details:  "All large tables are properly partitioned (if any)",
		})
		return
	}

	var tableRows []check.TableRow

	for _, row := range critical {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				check.FormatBytes(row.TableSizeBytes.Int64),
				check.FormatNumber(row.EstimatedRows.Int64),
				activityReason(row),
				"MUST partition",
			},
			Severity: check.SeverityFail,
		})
	}

	for _, row := range warning {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				check.FormatBytes(row.TableSizeBytes.Int64),
				check.FormatNumber(row.EstimatedRows.Int64),
				activityReason(row),
				"Approaching threshold",
			},
			Severity: check.SeverityWarn,
		})
	}

	severity := check.SeverityWarn
	if len(critical) > 0 {
		severity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "large-unpartitioned",
		Name:     "Large Unpartitioned Tables",
		Severity: severity,
		Details:  fmt.Sprintf("Found %d large table(s) that should be partitioned", len(rows)),
		Table: &check.Table{
			Headers: []string{"Table", "Size", "Est. Rows", "Reason", "Status"},
			Rows:    tableRows,
		},
	})
}

// Identifies transient tables (outbox, inbox, jobs) without partitioning.
func checkTransientUnpartitioned(rows []db.LargeTablesRow, report *check.Report) {
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "transient-unpartitioned",
			Name:     "Transient Tables Partitioning",
			Severity: check.SeverityOK,
			Details:  "All large transient tables are properly partitioned (if any)",
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range rows {
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				check.FormatBytes(row.TableSizeBytes.Int64),
				check.FormatNumber(row.EstimatedRows.Int64),
			},
			Severity: check.SeverityFail,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "transient-unpartitioned",
		Name:     "Transient Tables Partitioning",
		Severity: check.SeverityFail,
		Details:  fmt.Sprintf("Found %d large transient table(s) without partitioning", len(rows)),
		Table: &check.Table{
			Headers: []string{"Table", "Size", "Est. Rows"},
			Rows:    tableRows,
		},
	})
}

// checkInefficientPartitions identifies partitions that are too large, indicating poor partition strategy.
func checkInefficientPartitions(rows []db.LargeTablesRow, report *check.Report) {
	if len(rows) == 0 {
		return // No finding needed when there are no inefficient partitions
	}

	var tableRows []check.TableRow
	for _, row := range rows {
		parentTable := "unknown"
		if row.ParentTable.Valid {
			parentTable = row.ParentTable.String
		}
		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.TableName.String,
				parentTable,
				check.FormatBytes(row.TableSizeBytes.Int64),
				check.FormatNumber(row.EstimatedRows.Int64),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "inefficient-partitions",
		Name:     "Inefficient Partition Strategy",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d partition(s) with >= 25M rows - partition strategy may be inefficient", len(rows)),
		Table: &check.Table{
			Headers: []string{"Partition", "Parent Table", "Size", "Est. Rows"},
			Rows:    tableRows,
		},
	})
}
