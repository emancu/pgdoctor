// Package partitionusage implements checks for partition key usage in queries.
package partitionusage

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type PartitionUsageQueries interface {
	HasPgStatStatements(context.Context) (bool, error)
	HiddenQueryTextCount(context.Context) (pgtype.Int8, error)
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

	// problemQueriesBrief is how many statements are listed at the default
	// detail level; --detail verbose lists every one.
	problemQueriesBrief = 3

	// maxQueryTextWidth keeps the table inside a terminal. ORM-generated
	// statements run to thousands of characters, and the queryid identifies
	// them anyway.
	maxQueryTextWidth = 120
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
			Name:     "Queries Missing Partition Key",
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

	hiddenQueries, err := c.queries.HiddenQueryTextCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting hidden query texts: %w", err)
	}

	if hiddenQueries.Int64 > 0 {
		report.AddFinding(check.Finding{
			ID:       "query-text-restricted",
			Name:     "Query Text Not Fully Visible",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf(
				"%d pg_stat_statements entries are hidden from the current role, so partition key analysis covers only part of the workload. Grant pg_read_all_stats to see every query.",
				hiddenQueries.Int64),
		})
	}

	// Full query pattern analysis with pg_stat_statements
	queryStats, err := c.queries.QueryStatsFromStatStatements(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying pg_stat_statements: %w", err)
	}

	if len(queryStats) == 0 {
		report.AddFinding(check.Finding{
			ID:       "partition-key-unused",
			Name:     "Queries Missing Partition Key",
			Severity: check.SeverityPass,
			Details:  "No query statistics available (pg_stat_statements may be empty)",
		})
	} else {
		analyzed := prepareQueries(queryStats)

		checkPartitionKeyUsage(partitionedTables, analyzed, report)
		checkJoinsMissingPartitionKey(partitionedTables, analyzed, report)
	}

	return report, nil
}

// analyzedQuery is one pg_stat_statements entry prepared for matching.
type analyzedQuery struct {
	// matchText is the statement lowercased, since matching is case-insensitive.
	matchText string
	// display is the statement as written, for reporting.
	display  string
	queryID  int64
	calls    int64
	execTime float64
}

// prepareQueries lowercases each statement once. Both subchecks compare every
// statement against every partitioned table, so doing it here keeps the work
// linear in the number of statements — and fetching one copy of the text from
// the server rather than two keeps the transfer half the size.
func prepareQueries(rows []db.QueryStatsFromStatStatementsRow) []analyzedQuery {
	analyzed := make([]analyzedQuery, 0, len(rows))
	for _, row := range rows {
		analyzed = append(analyzed, analyzedQuery{
			matchText: strings.ToLower(row.Query.String),
			display:   row.Query.String,
			queryID:   row.QueryID.Int64,
			calls:     row.Calls.Int64,
			execTime:  row.TotalExecTime.Float64,
		})
	}

	return analyzed
}

// checkPartitionKeyUsage analyzes queries to find those not using partition keys.
func checkPartitionKeyUsage(
	tables []db.PartitionedTablesWithKeysRow,
	queries []analyzedQuery,
	report *check.Report,
) {
	var affected []affectedTable
	var problems []problemQuery
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
		qualifiedName := fmt.Sprintf("%s.%s", schemaName, tableName)

		var found []problemQuery
		var totalCalls int64
		var totalExecTime float64

		for _, q := range queries {
			if !queryReferencesTable(q.matchText, schemaName, tableName) {
				continue
			}

			if !queryConstrainsPartitionKey(q.matchText, partitionKeys, table.PartitionStrategy.String, tableName) {
				if q.calls >= minCallsWarn || q.execTime >= totalExecTimeWarnMs {
					totalCalls += q.calls
					totalExecTime += q.execTime
					found = append(found, problemQuery{
						table:    qualifiedName,
						calls:    q.calls,
						execTime: q.execTime,
						queryID:  q.queryID,
						text:     q.display,
					})
				}
			}
		}

		if len(found) == 0 {
			continue
		}

		// Severity is a property of the table's whole workload, not of any single
		// statement, so every one of its rows carries the table's severity.
		severity := check.SeverityWarn
		if totalCalls >= minCallsFail || totalExecTime >= totalExecTimeFailMs {
			severity = check.SeverityFail
			hasCritical = true
		}

		for i := range found {
			found[i].severity = severity
		}

		problems = append(problems, found...)
		affected = append(affected, affectedTable{
			qualifiedName: qualifiedName,
			partitionKeys: table.PartitionKeyColumns.String,
			partitions:    table.PartitionCount.Int64,
			statements:    len(found),
			calls:         totalCalls,
			execTime:      totalExecTime,
		})
	}

	if len(affected) == 0 {
		report.AddFinding(check.Finding{
			ID:       "partition-key-unused",
			Name:     "Queries Missing Partition Key",
			Severity: check.SeverityPass,
			Details:  fmt.Sprintf("All queries on %d partitioned table(s) properly use partition keys", len(tables)),
		})
		return
	}

	overallSeverity := check.SeverityWarn
	if hasCritical {
		overallSeverity = check.SeverityFail
	}

	// Worst first, so the default detail level shows the statements that matter
	// most across every table rather than whichever table came first.
	slices.SortStableFunc(problems, func(a, b problemQuery) int {
		switch {
		case a.execTime > b.execTime:
			return -1
		case a.execTime < b.execTime:
			return 1
		default:
			return 0
		}
	})

	rows := make([]check.TableRow, 0, len(problems))
	for _, problem := range problems {
		rows = append(rows, check.TableRow{
			Cells: []string{
				problem.table,
				check.FormatNumber(problem.calls),
				check.FormatDurationMs(problem.execTime),
				fmt.Sprintf("%d", problem.queryID),
				clipQueryText(problem.text),
			},
			Severity: problem.severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "partition-key-unused",
		Name:     "Queries Missing Partition Key",
		Severity: overallSeverity,
		Details:  problemDetails(affected, len(problems)),
		Table: &check.Table{
			Headers:      []string{"Table", "Calls", "Total Time", "Query ID", "Query"},
			Rows:         rows,
			MaxRowsBrief: problemQueriesBrief,
		},
	})
}

// affectedTable summarizes one partitioned table's unprunable workload.
type affectedTable struct {
	qualifiedName string
	partitionKeys string
	partitions    int64
	statements    int
	calls         int64
	execTime      float64
}

// problemQuery is one statement that does not constrain its table's partition key.
type problemQuery struct {
	table    string
	calls    int64
	execTime float64
	queryID  int64
	text     string
	severity check.Severity
}

// problemDetails describes the affected tables, so the per-table totals stay
// visible even when the statement list is capped at the default detail level.
func problemDetails(affected []affectedTable, statements int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Found %d statement(s) not using the partition key on %d partitioned table(s)",
		statements, len(affected))

	for _, table := range affected {
		fmt.Fprintf(&b, "\n  %s (key: %s, %d partitions) — %d statement(s), %s calls, %s",
			table.qualifiedName, table.partitionKeys, table.partitions, table.statements,
			check.FormatNumber(table.calls), check.FormatDurationMs(table.execTime))
	}

	b.WriteString("\nRead a statement in full: SELECT query FROM pg_stat_statements WHERE queryid = <Query ID>;")

	return b.String()
}

// clipQueryText shortens a normalized statement to one terminal line, counting
// runes so a multi-byte identifier or comment is never split mid-character.
func clipQueryText(text string) string {
	runes := []rune(text)
	if len(runes) <= maxQueryTextWidth {
		return text
	}

	return string(runes[:maxQueryTextWidth-1]) + "…"
}

// queryReferencesTable checks if a query text references a specific table.
// A reference qualified by a different schema does not count, so identically
// named tables in sibling schemas are not credited with each other's queries.
func queryReferencesTable(queryText, schemaName, tableName string) bool {
	schemaName = strings.ToLower(schemaName)
	tableName = strings.ToLower(tableName)

	// Either identifier may be quoted independently, so cover all four forms.
	for _, schema := range []string{schemaName, `"` + schemaName + `"`} {
		for _, table := range []string{tableName, `"` + tableName + `"`} {
			if containsSQLIdentifier(queryText, schema+"."+table) {
				return true
			}
		}
	}

	return containsUnqualifiedIdentifier(queryText, tableName) ||
		containsUnqualifiedIdentifier(queryText, `"`+tableName+`"`)
}

// containsUnqualifiedIdentifier reports whether identifier occurs with SQL
// identifier boundaries and without a schema qualifier in front of it.
func containsUnqualifiedIdentifier(queryText, identifier string) bool {
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
		if hasStartBoundary && hasEndBoundary && !isSchemaQualified(queryText[:match]) {
			return true
		}

		searchFrom = match + 1
	}

	return false
}

// isSchemaQualified reports whether text ends with a qualifier such as
// "tenant_a." or `"tenant_a".` that would bind the following identifier to
// another schema.
func isSchemaQualified(text string) bool {
	// Skip the identifier's own opening quote, so tenant_a."orders" is seen as
	// qualified even when the bare table name was the pattern that matched.
	text = strings.TrimSuffix(text, `"`)

	if !strings.HasSuffix(text, ".") {
		return false
	}

	qualifier := strings.TrimSuffix(text, ".")
	if qualifier == "" {
		return false
	}

	last := qualifier[len(qualifier)-1]

	return isSQLIdentifierByte(last) || last == '"'
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

// Partition strategies as stored in pg_partitioned_table.partstrat.
const (
	strategyHash  = "h"
	strategyList  = "l"
	strategyRange = "r"
)

// queryConstrainsPartitionKey reports whether the statement constrains the
// partition key in a way the strategy can prune with, looking at everything
// after FROM so JOIN conditions count alongside WHERE. A key constrained only
// through a join prunes when the planner parameterizes the partitioned side,
// and not when it hash joins, so this cannot be decided from query text —
// treating it as constrained keeps the check quiet rather than reporting a
// table whose access path may well be pruning.
func queryConstrainsPartitionKey(queryText string, partitionKeys []string, strategy string, tableName string) bool {
	clause := searchableClause(queryText)
	if clause == "" {
		return false
	}

	return clauseEnablesPruning(maskForeignSubqueries(clause, tableName), partitionKeys, strategy)
}

// searchableClause returns the part of the statement that can constrain the
// partition key. Everything after FROM covers JOIN conditions alongside WHERE,
// but a plain UPDATE has no FROM, so fall back to its WHERE clause.
func searchableClause(queryText string) string {
	if fromIdx := strings.Index(queryText, " from "); fromIdx != -1 {
		return queryText[fromIdx:]
	}

	if whereIdx := strings.Index(queryText, " where "); whereIdx != -1 {
		return queryText[whereIdx:]
	}

	return ""
}

// maskForeignSubqueries blanks out parenthesized subqueries that do not scan the
// target table, so a predicate on another table's identically named column —
// common for a key called "id" — is not read as constraining this one. A
// subquery that does reference the target table is kept, since its predicate may
// be the one enabling pruning. Only groups that open a SELECT are removed:
// ActiveRecord and Ecto wrap ordinary predicates in parentheses, and those must
// still be searched.
func maskForeignSubqueries(text, tableName string) string {
	tableName = strings.ToLower(tableName)
	masked := []byte(text)

	for i := 0; i < len(masked); i++ {
		if masked[i] != '(' {
			continue
		}

		if !strings.HasPrefix(strings.TrimLeft(text[i+1:], " "), "select") {
			continue
		}

		if end := matchingParen(text, i); end != -1 &&
			containsUnqualifiedIdentifier(text[i:end], tableName) {
			continue
		}

		end := matchingParen(text, i)
		if end == -1 {
			end = len(masked)
		}

		for j := i; j < end; j++ {
			masked[j] = ' '
		}

		i = end
	}

	return string(masked)
}

// matchingParen returns the index just past the ')' closing the '(' at open,
// or -1 when the parentheses are unbalanced.
func matchingParen(text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}

	return -1
}

// clauseEnablesPruning reports whether the clause constrains the partition key
// in a way the strategy can prune with:
//
//   - hash: every key column needs equality, since the hash is only determined
//     once every column is known. A range comparison never prunes.
//   - list and range: the leading key column must be constrained, with any
//     comparison. PostgreSQL prunes LIST partitions for inequalities too — it
//     excludes partitions whose listed values cannot satisfy the predicate.
//
// An unrecognized strategy falls back to "any key column, any pruning operator".
func clauseEnablesPruning(clause string, partitionKeys []string, strategy string) bool {
	columns := normalizeColumns(partitionKeys)
	if len(columns) == 0 {
		return false
	}

	switch strategy {
	case strategyHash:
		for _, col := range columns {
			if !clauseFiltersColumn(clause, col, equalityOnly) {
				return false
			}
		}
		return true

	case strategyList, strategyRange:
		return clauseFiltersColumn(clause, columns[0], anyPruningOperator)

	default:
		return clauseFiltersAnyColumn(clause, columns)
	}
}

func normalizeColumns(columns []string) []string {
	normalized := make([]string, 0, len(columns))
	for _, col := range columns {
		if col = strings.ToLower(strings.TrimSpace(col)); col != "" {
			normalized = append(normalized, col)
		}
	}

	return normalized
}

// clauseFiltersAnyColumn reports whether the clause filters on any of the columns.
func clauseFiltersAnyColumn(clause string, columns []string) bool {
	for _, col := range normalizeColumns(columns) {
		if clauseFiltersColumn(clause, col, anyPruningOperator) {
			return true
		}
	}

	return false
}

// operatorMode selects which comparisons count as constraining a column.
type operatorMode int

const (
	// anyPruningOperator accepts every btree comparison, as range pruning uses.
	anyPruningOperator operatorMode = iota
	// equalityOnly accepts only equality forms, the sole way hash and list
	// partitioning can prune.
	equalityOnly
)

// clauseFiltersColumn reports whether col appears in clause as a comparison
// that can drive partition pruning, with SQL identifier boundaries so a key
// such as "id" does not match inside "customer_id". Bare, quoted, and
// table-qualified references all satisfy the boundary rule.
func clauseFiltersColumn(clause, col string, mode operatorMode) bool {
	for searchFrom := 0; searchFrom < len(clause); {
		match := strings.Index(clause[searchFrom:], col)
		if match == -1 {
			return false
		}
		match += searchFrom
		matchEnd := match + len(col)

		hasStartBoundary := match == 0 || !isSQLIdentifierByte(clause[match-1])
		hasEndBoundary := matchEnd == len(clause) || !isSQLIdentifierByte(clause[matchEnd])
		if hasStartBoundary && hasEndBoundary &&
			(hasComparisonAfter(clause[matchEnd:], mode) || hasComparisonBefore(clause[:match], mode)) {
			return true
		}

		searchFrom = match + 1
	}

	return false
}

// pruningOperators are the btree comparison operators partition pruning can
// use, longest first so "<=" is not read as "<".
var pruningOperators = []string{"<=", ">=", "=", "<", ">"}

// operatorsFor returns the comparison operators the mode accepts.
func operatorsFor(mode operatorMode) []string {
	if mode == equalityOnly {
		return []string{"="}
	}

	return pruningOperators
}

// keywordsFor returns the comparison keywords the mode accepts. BETWEEN is a
// range constraint, so hash and list partitioning cannot prune with it.
func keywordsFor(mode operatorMode) []string {
	if mode == equalityOnly {
		return []string{"in ", "in(", "is null"}
	}

	return []string{"in ", "in(", "between ", "is null"}
}

// operatorBytes are bytes that can form part of a SQL operator name. They mark
// a match such as "<" in "<@" or "<>" as a different, non-pruning operator.
const operatorBytes = "=<>!~@#%^&|?"

func isOperatorByte(b byte) bool {
	return strings.IndexByte(operatorBytes, b) != -1
}

// trimTrailingIdentifier removes one trailing SQL identifier, quoted or bare.
func trimTrailingIdentifier(text string) string {
	text = strings.TrimSuffix(text, `"`)

	end := len(text)
	for end > 0 && isSQLIdentifierByte(text[end-1]) {
		end--
	}

	return strings.TrimSuffix(text[:end], `"`)
}

// hasComparisonAfter reports whether rest starts (after an optional closing
// quote and spaces) with a comparison that enables partition pruning, i.e. the
// column is on the left-hand side. A bare column mention (in ORDER BY or a
// SELECT list) does not count.
func hasComparisonAfter(rest string, mode operatorMode) bool {
	rest = strings.TrimPrefix(rest, `"`)
	rest = strings.TrimLeft(rest, " ")

	// Match against every pruning operator, not just the accepted ones, so a
	// rejected operator does not fall through to the keyword check.
	for _, op := range pruningOperators {
		if strings.HasPrefix(rest, op) {
			trailing := rest[len(op):]
			// Reject compound operators such as <>, <@ and >>.
			if trailing != "" && isOperatorByte(trailing[0]) {
				return false
			}
			return slices.Contains(operatorsFor(mode), op)
		}
	}

	for _, keyword := range keywordsFor(mode) {
		if strings.HasPrefix(rest, keyword) {
			return true
		}
	}

	return false
}

// hasComparisonBefore reports whether text ends with a comparison that enables
// partition pruning, i.e. the column is on the right-hand side ($1 <= created_at).
// PostgreSQL commutes these operators, so they prune just like the left-hand form.
func hasComparisonBefore(text string, mode operatorMode) bool {
	text = strings.TrimSuffix(text, `"`)

	// Skip a table qualifier between the operator and the column, as in
	// "$1 <= o.created_at" or `$1 = "orders"."created_at"`.
	if strings.HasSuffix(text, ".") {
		text = trimTrailingIdentifier(strings.TrimSuffix(text, "."))
	}

	text = strings.TrimRight(text, " ")

	for _, op := range pruningOperators {
		if strings.HasSuffix(text, op) {
			leading := text[:len(text)-len(op)]
			// Reject compound operators such as <>, != and @>.
			if leading != "" && isOperatorByte(leading[len(leading)-1]) {
				return false
			}
			return slices.Contains(operatorsFor(mode), op)
		}
	}

	return false
}

// checkJoinsMissingPartitionKey detects JOINs on partitioned tables that don't include the partition key.
func checkJoinsMissingPartitionKey(
	tables []db.PartitionedTablesWithKeysRow,
	queries []analyzedQuery,
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
			// Only check queries with JOINs that reference this table.
			if !queryHasJoin(q.matchText) {
				continue
			}

			if !queryReferencesTable(q.matchText, schemaName, tableName) {
				continue
			}

			// Check if partition key appears after FROM (covers JOIN ON, WHERE, implicit joins).
			if !queryConstrainsPartitionKey(q.matchText, partitionKeys, table.PartitionStrategy.String, tableName) {
				if q.calls >= minCallsWarn || q.execTime >= totalExecTimeWarnMs {
					problemJoinCount++
					totalCalls += q.calls
					totalExecTime += q.execTime
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
