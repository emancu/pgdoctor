// Package partitionusage implements checks for partition key usage in queries.
package partitionusage

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type PartitionUsageQueries interface {
	HasPgStatStatements(context.Context) (bool, error)
	PartitionedTablesWithKeys(context.Context) ([]db.PartitionedTablesWithKeysRow, error)
	QueryStatsFromStatStatements(context.Context) ([]db.QueryStatsFromStatStatementsRow, error)
}

type checker struct {
	queries PartitionUsageQueries
}

func Metadata() check.CheckMetadata {
	return check.CheckMetadata{
		Category:    check.CategoryPerformance,
		CheckID:     "partition-usage",
		Name:        "Partition Key Usage",
		Description: "Detects queries on partitioned tables that don't use partition keys",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries PartitionUsageQueries) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.CheckMetadata {
	return Metadata()
}

// Thresholds for severity levels.
const (
	minCallsWarn        = int64(100)
	minCallsFail        = int64(1000)
	totalExecTimeWarnMs = float64(300_000)  // 5 minutes
	totalExecTimeFailMs = float64(3600_000) // 1 hour

	// Sequential scan thresholds.
	minSeqScansWarn   = int64(1000)
	seqToIdxRatioWarn = int64(10)
	seqToIdxRatioFail = int64(100)
)

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	partitionedTables, err := c.queries.PartitionedTablesWithKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s (partitioned tables): %w", report.Category, report.CheckID, err)
	}

	if len(partitionedTables) == 0 {
		report.AddFinding(check.Finding{
			ID:       "partition-key-unused",
			Name:     "Partition Key Usage Analysis",
			Severity: check.SeverityOK,
			Details:  "No partitioned tables found",
		})
		return report, nil
	}

	checkSequentialScans(partitionedTables, report)

	hasExtension, err := c.queries.HasPgStatStatements(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking pg_stat_statements extension: %w", err)
	}

	if !hasExtension {
		report.AddFinding(check.Finding{
			ID:       "extension-unavailable",
			Name:     "pg_stat_statements Extension Not Available",
			Severity: check.SeverityWarn,
			Details:  fmt.Sprintf("Found %d partitioned table(s) but cannot analyze query patterns without pg_stat_statements extension", len(partitionedTables)),
		})

		return report, nil
	}

	// Full query pattern analysis with pg_stat_statements
	queryStats, err := c.queries.QueryStatsFromStatStatements(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying pg_stat_statements: %w", err)
	}

	if len(queryStats) == 0 {
		report.AddFinding(check.Finding{
			ID:       "partition-key-unused",
			Name:     "Partition Key Usage Analysis",
			Severity: check.SeverityOK,
			Details:  "No query statistics available (pg_stat_statements may be empty)",
		})
	} else {
		checkPartitionKeyUsage(partitionedTables, queryStats, report)
		checkJoinsMissingPartitionKey(partitionedTables, queryStats, report)
	}

	return report, nil
}

// checkPartitionKeyUsage analyzes queries to find those not using partition keys.
func checkPartitionKeyUsage(
	tables []db.PartitionedTablesWithKeysRow,
	queries []db.QueryStatsFromStatStatementsRow,
	report *check.Report,
) {
	var tableRows []check.TableRow
	var prescriptionExamples []string
	hasCritical := false

	for _, table := range tables {
		// Skip tables with expression-based partition keys (too complex to analyze).
		if table.HasExpressionKey.Valid && table.HasExpressionKey.Bool {
			continue
		}

		// Skip tables without partition key info.
		if !table.PartitionKeyColumns.Valid || table.PartitionKeyColumns.String == "" {
			continue
		}

		partitionKeys := strings.Split(table.PartitionKeyColumns.String, ",")
		tableName := table.TableName.String
		schemaName := table.SchemaName.String

		var problemQueryCount int
		var totalCalls int64
		var totalExecTime float64
		var exampleQuery string

		for _, q := range queries {
			queryText := strings.ToLower(q.Query.String)

			if !queryReferencesTable(queryText, schemaName, tableName) {
				continue
			}

			if !queryUsesPartitionKey(queryText, partitionKeys) {
				calls := q.Calls.Int64
				execTime := q.TotalExecTime.Float64
				if calls >= minCallsWarn || execTime >= totalExecTimeWarnMs {
					problemQueryCount++
					totalCalls += calls
					totalExecTime += execTime
					if exampleQuery == "" {
						exampleQuery = fmt.Sprintf("Table: %s.%s (partition key: %s, %d partitions)\n  Example query (%d calls, %s total):\n    %s",
							schemaName, tableName, table.PartitionKeyColumns.String, table.PartitionCount.Int64,
							calls, check.FormatDurationMs(execTime), q.Query.String)
					}
				}
			}
		}

		if problemQueryCount > 0 {
			severity := check.SeverityWarn
			if totalCalls >= minCallsFail || totalExecTime >= totalExecTimeFailMs {
				severity = check.SeverityFail
				hasCritical = true
			}

			tableRows = append(tableRows, check.TableRow{
				Cells: []string{
					fmt.Sprintf("%s.%s", schemaName, tableName),
					table.PartitionKeyColumns.String,
					fmt.Sprintf("%d", table.PartitionCount.Int64),
					fmt.Sprintf("%d", problemQueryCount),
					fmt.Sprintf("%d", totalCalls),
					check.FormatDurationMs(totalExecTime),
				},
				Severity: severity,
			})

			if len(prescriptionExamples) < 3 {
				prescriptionExamples = append(prescriptionExamples, exampleQuery)
			}
		}
	}

	if len(tableRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "partition-key-unused",
			Name:     "Partition Key Usage Analysis",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All queries on %d partitioned table(s) properly use partition keys", len(tables)),
		})
		return
	}

	overallSeverity := check.SeverityWarn
	if hasCritical {
		overallSeverity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "partition-key-unused",
		Name:     "Partition Key Usage Analysis",
		Severity: overallSeverity,
		Details:  fmt.Sprintf("Found %d partitioned table(s) with queries not using partition key", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Partition Key", "Partitions", "Problem Queries", "Total Calls", "Total Time"},
			Rows:    tableRows,
		},
	})
}

// queryReferencesTable checks if a query text references a specific table.
func queryReferencesTable(queryText, schemaName, tableName string) bool {
	patterns := []string{
		strings.ToLower(schemaName + "." + tableName),
		strings.ToLower(tableName),
		`"` + strings.ToLower(tableName) + `"`,
	}

	for _, p := range patterns {
		if strings.Contains(queryText, p) {
			return true
		}
	}
	return false
}

// queryUsesPartitionKey checks if the query's WHERE clause uses any partition key column.
func queryUsesPartitionKey(queryText string, partitionKeys []string) bool {
	whereClause := extractWhereClause(queryText)
	if whereClause == "" {
		return false
	}

	for _, col := range partitionKeys {
		col = strings.ToLower(strings.TrimSpace(col))
		if col == "" {
			continue
		}

		patterns := []string{
			col + " =", col + "=",
			col + " >", col + ">",
			col + " <", col + "<",
			col + " in", col + " between", col + " is", col + " any",
			"." + col + " ", "." + col + "=", "." + col + ">", "." + col + "<",
		}

		for _, p := range patterns {
			if strings.Contains(whereClause, p) {
				return true
			}
		}
	}

	return false
}

// extractWhereClause extracts the WHERE clause from a query.
func extractWhereClause(queryText string) string {
	_, after, ok := strings.Cut(queryText, " where ")
	if !ok {
		return ""
	}

	clause := after

	endMarkers := []string{" order by", " group by", " having", " limit", " offset", " for update", " for share", ";"}
	for _, marker := range endMarkers {
		if idx := strings.Index(clause, marker); idx != -1 {
			clause = clause[:idx]
		}
	}

	return strings.TrimSpace(clause)
}

// checkJoinsMissingPartitionKey detects JOINs on partitioned tables that don't include the partition key.
func checkJoinsMissingPartitionKey(
	tables []db.PartitionedTablesWithKeysRow,
	queries []db.QueryStatsFromStatStatementsRow,
	report *check.Report,
) {
	var tableRows []check.TableRow
	hasCritical := false

	for _, table := range tables {
		// Skip tables with expression-based partition keys.
		if table.HasExpressionKey.Valid && table.HasExpressionKey.Bool {
			continue
		}

		if !table.PartitionKeyColumns.Valid || table.PartitionKeyColumns.String == "" {
			continue
		}

		partitionKeys := strings.Split(table.PartitionKeyColumns.String, ",")
		tableName := table.TableName.String
		schemaName := table.SchemaName.String

		var problemJoinCount int
		var totalCalls int64
		var totalExecTime float64

		for _, q := range queries {
			queryText := strings.ToLower(q.Query.String)

			// Only check queries with JOINs that reference this table.
			if !queryHasJoin(queryText) {
				continue
			}

			if !queryReferencesTable(queryText, schemaName, tableName) {
				continue
			}

			// Check if partition key appears after FROM (covers JOIN ON, WHERE, implicit joins).
			if !queryUsesPartitionKeyAfterFrom(queryText, partitionKeys) {
				calls := q.Calls.Int64
				execTime := q.TotalExecTime.Float64
				if calls >= minCallsWarn || execTime >= totalExecTimeWarnMs {
					problemJoinCount++
					totalCalls += calls
					totalExecTime += execTime
				}
			}
		}

		if problemJoinCount > 0 {
			severity := check.SeverityWarn
			if totalCalls >= minCallsFail || totalExecTime >= totalExecTimeFailMs {
				severity = check.SeverityFail
				hasCritical = true
			}

			tableRows = append(tableRows, check.TableRow{
				Cells: []string{
					fmt.Sprintf("%s.%s", schemaName, tableName),
					table.PartitionKeyColumns.String,
					fmt.Sprintf("%d", problemJoinCount),
					fmt.Sprintf("%d", totalCalls),
					check.FormatDurationMs(totalExecTime),
				},
				Severity: severity,
			})
		}
	}

	if len(tableRows) == 0 {
		return // No finding needed when there are no issues
	}

	overallSeverity := check.SeverityWarn
	if hasCritical {
		overallSeverity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "join-missing-partition-key",
		Name:     "JOINs Missing Partition Key",
		Severity: overallSeverity,
		Details:  fmt.Sprintf("Found %d partitioned table(s) with JOINs not using partition key", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Partition Key", "Problem JOINs", "Total Calls", "Total Time"},
			Rows:    tableRows,
		},
	})
}

// checkSequentialScans detects partitioned tables with high sequential scan ratios.
func checkSequentialScans(tables []db.PartitionedTablesWithKeysRow, report *check.Report) {
	var tableRows []check.TableRow
	hasCritical := false

	for _, table := range tables {
		seqScans := table.TotalSeqScans.Int64
		idxScans := table.TotalIdxScans.Int64

		// Skip if not enough seq scans to be significant.
		if seqScans < minSeqScansWarn {
			continue
		}

		// Check ratio: seq_scans > N * idx_scans.
		// Handle idx_scans = 0 case (infinite ratio).
		var ratio int64
		if idxScans == 0 {
			ratio = seqScans // Treat as very high ratio
		} else {
			ratio = seqScans / idxScans
		}

		if ratio < seqToIdxRatioWarn {
			continue
		}

		severity := check.SeverityWarn
		if ratio >= seqToIdxRatioFail {
			severity = check.SeverityFail
			hasCritical = true
		}

		ratioStr := fmt.Sprintf("%d:1", ratio)
		if idxScans == 0 {
			ratioStr = "∞ (no idx scans)"
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				fmt.Sprintf("%s.%s", table.SchemaName.String, table.TableName.String),
				check.FormatNumber(seqScans),
				check.FormatNumber(idxScans),
				ratioStr,
			},
			Severity: severity,
		})
	}

	if len(tableRows) == 0 {
		return // No finding needed when there are no issues
	}

	overallSeverity := check.SeverityWarn
	if hasCritical {
		overallSeverity = check.SeverityFail
	}

	report.AddFinding(check.Finding{
		ID:       "high-seq-scan-ratio",
		Name:     "High Sequential Scan Ratio",
		Severity: overallSeverity,
		Details:  fmt.Sprintf("Found %d partitioned table(s) with high sequential scan ratio", len(tableRows)),
		Table: &check.Table{
			Headers: []string{"Table", "Seq Scans", "Idx Scans", "Ratio"},
			Rows:    tableRows,
		},
	})
}

// Query analysis helpers.

// queryHasJoin checks if a query contains a JOIN clause.
func queryHasJoin(queryText string) bool {
	return strings.Contains(queryText, " join ")
}

// queryUsesPartitionKeyAfterFrom checks if partition key appears after FROM clause.
func queryUsesPartitionKeyAfterFrom(queryText string, partitionKeys []string) bool {
	fromIdx := strings.Index(queryText, " from ")
	if fromIdx == -1 {
		return false
	}

	afterFrom := queryText[fromIdx:]

	for _, col := range partitionKeys {
		col = strings.ToLower(strings.TrimSpace(col))
		if col == "" {
			continue
		}

		if strings.Contains(afterFrom, col) {
			return true
		}
	}
	return false
}
