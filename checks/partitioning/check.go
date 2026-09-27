// Package partitioning implements checks for table partitioning compliance.
package partitioning

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type PartitioningQueries interface {
	LargeTables(ctx context.Context, arg db.LargeTablesParams) ([]db.LargeTablesRow, error)
}

type checker struct {
	queries          PartitioningQueries
	minPartitionRows int64
	largeMinRows     int64
	transientMinRows int64
}

const (
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

func New(queries PartitioningQueries, cfg ...check.Config) check.Checker {
	c := &checker{
		queries:          queries,
		minPartitionRows: 10_000_000,
		largeMinRows:     50_000_000,
		transientMinRows: 10_000_000,
	}
	if len(cfg) > 0 && cfg[0] != nil {
		if myCfg, ok := cfg[0][Metadata().CheckID]; ok {
			for key, dst := range map[string]*int64{
				"inefficient_partitions_min_rows":  &c.minPartitionRows,
				"large_unpartitioned_min_rows":     &c.largeMinRows,
				"transient_unpartitioned_min_rows": &c.transientMinRows,
			} {
				if n, err := strconv.ParseInt(myCfg[key], 10, 64); err == nil && n > 0 {
					*dst = n
				}
			}
		}
	}
	return c
}

func ValidateSetting(key, value string) error {
	switch key {
	case "inefficient_partitions_min_rows", "large_unpartitioned_min_rows", "transient_unpartitioned_min_rows":
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	if n, err := strconv.ParseInt(value, 10, 64); err != nil || n <= 0 {
		return fmt.Errorf("%s: %q is not a positive integer", key, value)
	}
	return nil
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.LargeTables(ctx, db.LargeTablesParams{
		MinTableRows:     min(c.largeMinRows, c.transientMinRows),
		MinPartitionRows: c.minPartitionRows,
	})
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
			if row.EstimatedRows.Int64 >= c.transientMinRows {
				transientUnpartitioned = append(transientUnpartitioned, row)
			}
		} else if row.EstimatedRows.Int64 >= c.largeMinRows {
			largeUnpartitioned = append(largeUnpartitioned, row)
		}
	}

	// Run subchecks.
	checkLargeUnpartitioned(largeUnpartitioned, report)
	checkTransientUnpartitioned(transientUnpartitioned, report)
	checkInefficientPartitions(inefficientPartitions, c.minPartitionRows, report)

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

// activityReason returns a human-readable write pattern for the table.
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
	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "large-unpartitioned",
			Name:     "Large Unpartitioned Tables",
			Severity: check.SeverityPass,
			Details:  "All large tables are properly partitioned (if any)",
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
				activityReason(row),
			},
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "large-unpartitioned",
		Name:     "Large Unpartitioned Tables",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d large table(s) that should be partitioned", len(rows)),
		Table: &check.Table{
			Headers: []string{"Table", "Size", "Est. Rows", "Reason"},
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
			Severity: check.SeverityPass,
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
			Severity: check.SeverityWarn,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "transient-unpartitioned",
		Name:     "Transient Tables Partitioning",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d large transient table(s) without partitioning", len(rows)),
		Table: &check.Table{
			Headers: []string{"Table", "Size", "Est. Rows"},
			Rows:    tableRows,
		},
	})
}

// checkInefficientPartitions identifies partitions that are too large, indicating poor partition strategy.
func checkInefficientPartitions(rows []db.LargeTablesRow, minRows int64, report *check.Report) {
	if len(rows) == 0 {
		return // No finding needed when there are no inefficient partitions
	}

	var tableRows []check.TableRow
	for _, row := range rows {
		parentTable := "-"
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
		Details:  fmt.Sprintf("Found %d partition(s) with >= %s rows - partition strategy may be inefficient", len(rows), check.FormatNumber(minRows)),
		Table: &check.Table{
			Headers: []string{"Partition", "Parent Table", "Size", "Est. Rows"},
			Rows:    tableRows,
		},
	})
}
