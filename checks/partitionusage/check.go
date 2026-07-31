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

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "partition-usage",
		Name:        "Partition Key Usage",
		Description: "Detects queries on partitioned tables that don't use partition keys",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries PartitionUsageQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
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
			Severity: check.SeverityPass,
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
			Severity: check.SeverityPass,
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
			// Query text is already normalized and lowercased by the SQL.
			queryText := q.Query.String

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
			Severity: check.SeverityPass,
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
		if containsSQLIdentifier(queryText, p) {
			return true
		}
	}
	return false
}

// containsSQLIdentifier reports whether identifier occurs with SQL identifier
// boundaries. In particular, a partition parent such as "orders" must not
// match a partition leaf such as "orders_2025_01".
func containsSQLIdentifier(queryText, identifier string) bool {
	if identifier == "" {
		return false
	}

	for searchFrom := 0; searchFrom < len(queryText); {
		match := strings.Index(queryText[searchFrom:], identifier)
		if match == -1 {
			return false
		}
		match += searchFrom
		matchEnd := match + len(identifier)

		hasStartBoundary := match == 0 || !isSQLIdentifierByte(queryText[match-1])
		hasEndBoundary := matchEnd == len(queryText) || !isSQLIdentifierByte(queryText[matchEnd])
		if hasStartBoundary && hasEndBoundary {
			return true
		}

		searchFrom = match + 1
	}

	return false
}

func isSQLIdentifierByte(b byte) bool {
	return b >= 'a' && b <= 'z' ||
		b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' ||
		b == '_' ||
		b == '$' ||
		b >= 0x80
}

// queryUsesPartitionKey checks if the query's WHERE clause uses any partition key column.
func queryUsesPartitionKey(queryText string, partitionKeys []string) bool {
	whereClause := extractWhereClause(queryText)
	if whereClause == "" {
		return false
	}

	return clauseFiltersAnyColumn(whereClause, partitionKeys)
}

// clauseFiltersAnyColumn reports whether the clause filters on any of the columns.
func clauseFiltersAnyColumn(clause string, columns []string) bool {
	for _, col := range columns {
		col = strings.ToLower(strings.TrimSpace(col))
		if col == "" {
			continue
		}

		if clauseFiltersColumn(clause, col) {
			return true
		}
	}

	return false
}

// clauseFiltersColumn reports whether col appears in clause as a comparison
// that can drive partition pruning, with SQL identifier boundaries so a key
// such as "id" does not match inside "customer_id". Bare, quoted, and
// table-qualified references all satisfy the boundary rule.
func clauseFiltersColumn(clause, col string) bool {
	for searchFrom := 0; searchFrom < len(clause); {
		match := strings.Index(clause[searchFrom:], col)
		if match == -1 {
			return false
		}
		match += searchFrom
		matchEnd := match + len(col)

		hasStartBoundary := match == 0 || !isSQLIdentifierByte(clause[match-1])
		hasEndBoundary := matchEnd == len(clause) || !isSQLIdentifierByte(clause[matchEnd])
		if hasStartBoundary && hasEndBoundary && hasComparisonAfter(clause[matchEnd:]) {
			return true
		}

		searchFrom = match + 1
	}

	return false
}

// hasComparisonAfter reports whether rest starts (after an optional closing
// quote and spaces) with a comparison operator that enables partition pruning.
// A bare column mention (e.g. in ORDER BY or a SELECT list) does not count.
func hasComparisonAfter(rest string) bool {
	rest = strings.TrimPrefix(rest, `"`)
	rest = strings.TrimLeft(rest, " ")

	if strings.HasPrefix(rest, "<>") {
		return false // <> never prunes partitions
	}

	for _, op := range []string{"=", ">", "<", "in ", "in(", "between ", "is null"} {
		if strings.HasPrefix(rest, op) {
			return true
		}
	}

	return false
}

// extractWhereClause extracts the WHERE clause from a query. Clause end
// markers are only honored outside parentheses, so a LIMIT or ORDER BY inside
// a subquery does not truncate the outer clause.
func extractWhereClause(queryText string) string {
	_, after, ok := strings.Cut(queryText, " where ")
	if !ok {
		return ""
	}

	endMarkers := []string{" order by", " group by", " having", " limit", " offset", " for update", " for share", ";"}

	depth := 0
	for i := 0; i < len(after); i++ {
		switch after[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ' ', ';':
			if depth > 0 {
				continue
			}
			for _, marker := range endMarkers {
				if strings.HasPrefix(after[i:], marker) {
					return strings.TrimSpace(after[:i])
				}
			}
		}
	}

	return strings.TrimSpace(after)
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
			queryText := q.Query.String

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

// queryUsesPartitionKeyAfterFrom checks if the partition key is compared
// anywhere after the FROM clause (covers JOIN ON conditions and WHERE).
func queryUsesPartitionKeyAfterFrom(queryText string, partitionKeys []string) bool {
	fromIdx := strings.Index(queryText, " from ")
	if fromIdx == -1 {
		return false
	}

	return clauseFiltersAnyColumn(queryText[fromIdx:], partitionKeys)
}
