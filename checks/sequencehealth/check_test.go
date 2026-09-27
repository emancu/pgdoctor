package sequencehealth_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/sequencehealth"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	findingIDNearExhaustion = "near-exhaustion"
	findingIDIntegerColumns = "integer-columns"
	findingIDTypeMismatch   = "type-mismatch"
)

type mockQueryer struct {
	rows []db.SequenceHealthRow
	err  error
}

func (m *mockQueryer) SequenceHealth(context.Context) ([]db.SequenceHealthRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func makeSequenceRow(
	schemaName, seqName, seqDataType, tableName, columnName, columnType string,
	currentValue, maxValue, incrementBy, remainingValues, columnMaxValue int64,
	usagePercent float64,
	isCyclic, sequenceExceedsColumn, isPrimaryKey bool,
	fkReferenceCount int64,
) db.SequenceHealthRow {
	usageNumeric := &pgtype.Numeric{}
	_ = usageNumeric.Scan(fmt.Sprintf("%.2f", usagePercent))

	var columnUsage pgtype.Numeric
	if columnType == "integer" || columnType == "smallint" {
		up := float64(currentValue) / float64(columnMaxValue) * 100
		down := -float64(currentValue) / (float64(columnMaxValue) + 1) * 100
		usage := max(0, up)
		if incrementBy < 0 {
			usage = max(0, down)
		}
		if up > 100 || down > 100 {
			usage = max(up, down)
		}
		_ = columnUsage.Scan(fmt.Sprintf("%.2f", usage))
	}

	return db.SequenceHealthRow{
		SchemaName:            pgtype.Text{String: schemaName, Valid: true},
		SequenceName:          pgtype.Text{String: seqName, Valid: true},
		SeqDataType:           pgtype.Text{String: seqDataType, Valid: true},
		CurrentValue:          pgtype.Int8{Int64: currentValue, Valid: true},
		MaxValue:              pgtype.Int8{Int64: maxValue, Valid: true},
		IncrementBy:           pgtype.Int8{Int64: incrementBy, Valid: true},
		IsCyclic:              pgtype.Bool{Bool: isCyclic, Valid: true},
		RemainingValues:       pgtype.Int8{Int64: remainingValues, Valid: true},
		UsagePercent:          *usageNumeric,
		TableName:             pgtype.Text{String: tableName, Valid: tableName != ""},
		ColumnName:            pgtype.Text{String: columnName, Valid: columnName != ""},
		ColumnType:            pgtype.Text{String: columnType, Valid: columnType != ""},
		ColumnMaxValue:        pgtype.Int8{Int64: columnMaxValue, Valid: columnMaxValue > 0},
		SequenceExceedsColumn: pgtype.Bool{Bool: sequenceExceedsColumn, Valid: true},
		ColumnUsagePercent:    columnUsage,
		IsPrimaryKey:          pgtype.Bool{Bool: isPrimaryKey, Valid: true},
		FkReferenceCount:      pgtype.Int8{Int64: fkReferenceCount, Valid: true},
	}
}

func TestSequenceHealth_NoSequences(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.SequenceHealthRow{}}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "No sequences found; expected for a schema using UUID primary keys")
}

func TestSequenceHealth_AllHealthy(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "bookings_id_seq", "bigint", "bookings", "id", "bigint",
			1000000, 9223372036854775807, 1, 9223372036853775807, 9223372036854775807,
			0.00001, false, false, true, 5,
		),
		makeSequenceRow(
			"public", "users_id_seq", "bigint", "users", "id", "bigint",
			500000, 9223372036854775807, 1, 9223372036854275807, 9223372036854775807,
			0.000005, false, false, true, 3,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)
	require.Equal(t, 3, len(report.Results))

	// All three subchecks should be OK
	for _, finding := range report.Results {
		require.Equal(t, check.SeverityPass, finding.Severity)
	}
}

func unreadableRow(seqName, tableName string) db.SequenceHealthRow {
	return db.SequenceHealthRow{
		SchemaName:   pgtype.Text{String: "public", Valid: true},
		SequenceName: pgtype.Text{String: seqName, Valid: true},
		SeqDataType:  pgtype.Text{String: "integer", Valid: true},
		MaxValue:     pgtype.Int8{Int64: 2147483647, Valid: true},
		IncrementBy:  pgtype.Int8{Int64: 1, Valid: true},
		IsCyclic:     pgtype.Bool{Bool: false, Valid: true},
		IsUnreadable: pgtype.Bool{Bool: true, Valid: true},
		TableName:    pgtype.Text{String: tableName, Valid: true},
		ColumnName:   pgtype.Text{String: "id", Valid: true},
		ColumnType:   pgtype.Text{String: "integer", Valid: true},
	}
}

func TestSequenceHealth_UnreadableSequences(t *testing.T) {
	t.Parallel()

	mismatched := unreadableRow("c_id_seq", "c")
	mismatched.SeqDataType = pgtype.Text{String: "bigint", Valid: true}
	mismatched.MaxValue = pgtype.Int8{Int64: 9223372036854775807, Valid: true}
	mismatched.ColumnMaxValue = pgtype.Int8{Int64: 2147483647, Valid: true}
	mismatched.SequenceExceedsColumn = pgtype.Bool{Bool: true, Valid: true}

	critical := makeSequenceRow(
		"public", "orders_id_seq", "integer", "orders", "id", "integer",
		1932735283, 2147483647, 1, 214748364, 2147483647,
		90.0, false, false, true, 2,
	)

	tests := []struct {
		name         string
		rows         []db.SequenceHealthRow
		severity     check.Severity
		findingIDs   []string
		wantInfoText string
	}{
		{
			name:       "all unreadable - SKIP",
			rows:       []db.SequenceHealthRow{unreadableRow("a_id_seq", "a"), unreadableRow("b_id_seq", "b")},
			severity:   check.SeveritySkip,
			findingIDs: []string{"sequence-health"},
		},
		{
			name:       "all unreadable with a type mismatch - SKIP",
			rows:       []db.SequenceHealthRow{mismatched, unreadableRow("a_id_seq", "a")},
			severity:   check.SeveritySkip,
			findingIDs: []string{"sequence-health"},
		},
		{
			name:         "some unreadable - readable ones evaluated plus INFO",
			rows:         []db.SequenceHealthRow{critical, unreadableRow("a_id_seq", "a"), unreadableRow("b_id_seq", "b")},
			severity:     check.SeverityFail,
			findingIDs:   []string{findingIDNearExhaustion, findingIDIntegerColumns, findingIDTypeMismatch, "unreadable-sequences"},
			wantInfoText: "2 sequence(s) not evaluated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report, err := sequencehealth.New(&mockQueryer{rows: tt.rows}).Check(context.Background())

			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)
			assert.Equal(t, tt.severity, report.Severity)

			var ids []string
			for _, finding := range report.Results {
				ids = append(ids, finding.ID)
			}
			assert.Equal(t, tt.findingIDs, ids)

			last := report.Results[len(report.Results)-1]
			assert.Contains(t, last.Details, "role cannot read sequence values (needs SELECT on the sequences)")
			if tt.wantInfoText != "" {
				assert.Equal(t, check.SeverityInfo, last.Severity)
				assert.Contains(t, last.Details, tt.wantInfoText)
			}
		})
	}
}

func TestSequenceHealth_NearExhaustion_Critical(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "orders_id_seq", "integer", "orders", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 2,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	// Find the near-exhaustion finding
	var exhaustionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDNearExhaustion {
			exhaustionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, exhaustionFinding)
	require.Equal(t, check.SeverityFail, exhaustionFinding.Severity)
	require.Contains(t, exhaustionFinding.Details, "CRITICAL")
	require.NotNil(t, exhaustionFinding.Table)
	require.Equal(t, 1, len(exhaustionFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, exhaustionFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_NearExhaustion_Direction(t *testing.T) {
	t.Parallel()

	const bigintMax = 9223372036854775807

	tests := []struct {
		name      string
		row       db.SequenceHealthRow
		severity  check.Severity
		usage     string
		remaining string
	}{
		{
			name: "ascending",
			row: makeSequenceRow("public", "public.asc_seq", "integer", "", "", "",
				2000000000, 2147483647, 1, 147483647, 0, 93.13, false, false, false, 0),
			severity: check.SeverityFail, usage: "93.1%", remaining: "147.5M",
		},
		{
			name: "descending",
			row: makeSequenceRow("public", "public.desc_seq", "integer", "", "", "",
				-2100000000, -1, -1, 47483648, 0, 97.79, false, false, false, 0),
			severity: check.SeverityFail, usage: "97.8%", remaining: "47.5M",
		},
		{
			name: "descending never called",
			row: makeSequenceRow("public", "public.desc_new_seq", "integer", "", "", "",
				-1, -1, -1, 2147483647, 0, 0, false, false, false, 0),
			severity: check.SeverityPass,
		},
		{
			name: "ascending with increment 10",
			row: makeSequenceRow("public", "public.step_seq", "integer", "", "", "",
				2147000000, 2147483647, 10, 48364, 0, 99.98, false, false, false, 0),
			severity: check.SeverityFail, usage: "100.0%", remaining: "48.4K",
		},
		{
			name: "descending with increment -10",
			row: makeSequenceRow("public", "public.desc_step_seq", "integer", "", "", "",
				-2147000000, -1, -10, 48364, 0, 99.98, false, false, false, 0),
			severity: check.SeverityFail, usage: "100.0%", remaining: "48.4K",
		},
		{
			name: "full bigint range",
			row: makeSequenceRow("public", "public.full_seq", "bigint", "", "", "",
				-bigintMax-1, bigintMax, 1, bigintMax, 0, 0, false, false, false, 0),
			severity: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report, err := sequencehealth.New(&mockQueryer{rows: []db.SequenceHealthRow{tt.row}}).Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			var finding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingIDNearExhaustion {
					finding = &report.Results[i]
				}
			}
			require.NotNil(t, finding)
			assert.Equal(t, tt.severity, finding.Severity)
			if tt.severity == check.SeverityPass {
				assert.Nil(t, finding.Table)
				return
			}
			require.Len(t, finding.Table.Rows, 1)
			assert.Equal(t, tt.usage, finding.Table.Rows[0].Cells[2])
			assert.Equal(t, tt.remaining, finding.Table.Rows[0].Cells[3])
		})
	}
}

func TestSequenceHealth_NearExhaustion_Warning(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "products_id_seq", "integer", "products", "id", "integer",
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	// Find the near-exhaustion finding
	var exhaustionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDNearExhaustion {
			exhaustionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, exhaustionFinding)
	require.Equal(t, check.SeverityWarn, exhaustionFinding.Severity)
	require.NotContains(t, exhaustionFinding.Details, "CRITICAL")
	require.Contains(t, exhaustionFinding.Details, "nearing exhaustion")
	require.NotNil(t, exhaustionFinding.Table)
	require.Equal(t, 1, len(exhaustionFinding.Table.Rows))
	require.Equal(t, check.SeverityWarn, exhaustionFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_NearExhaustion_MixedSeverity(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "critical_seq", "integer", "critical_table", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 0,
		),
		makeSequenceRow(
			"public", "warning_seq", "integer", "warning_table", "id", "integer",
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, 0,
		),
		makeSequenceRow(
			"public", "healthy_seq", "bigint", "healthy_table", "id", "bigint",
			1000000, 9223372036854775807, 1, 9223372036853775807, 9223372036854775807,
			0.00001, false, false, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	// Find the near-exhaustion finding
	var exhaustionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDNearExhaustion {
			exhaustionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, exhaustionFinding)
	require.Equal(t, check.SeverityFail, exhaustionFinding.Severity)
	require.Contains(t, exhaustionFinding.Details, "CRITICAL")
	require.Contains(t, exhaustionFinding.Details, "1 sequence(s) near exhaustion")
	require.Contains(t, exhaustionFinding.Details, "1 more nearing it")
	require.NotNil(t, exhaustionFinding.Table)
	require.Equal(t, 2, len(exhaustionFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, exhaustionFinding.Table.Rows[0].Severity)
	require.Equal(t, check.SeverityWarn, exhaustionFinding.Table.Rows[1].Severity)
}

func TestSequenceHealth_NearExhaustion_CyclicIgnored(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "cyclic_seq", "integer", "cyclic_table", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, true, false, false, 0, // is_cyclic = true
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity) // overall is FAIL because should_be_bigint is true

	// Near-exhaustion should be OK because cyclic sequences are ignored
	var exhaustionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == "near-exhaustion" {
			exhaustionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, exhaustionFinding)
	require.Equal(t, check.SeverityPass, exhaustionFinding.Severity)
	require.Contains(t, exhaustionFinding.Details, "sufficient headroom")
}

func TestSequenceHealth_IntegerShouldBeBigint_Warning(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.users_id_seq", "integer", "public.users", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, 5,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	// Find the integer-columns finding
	var integerFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDIntegerColumns {
			integerFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, integerFinding)
	require.Equal(t, check.SeverityWarn, integerFinding.Severity)
	require.Contains(t, integerFinding.Details, ">=50%")
	require.Contains(t, integerFinding.Details, "migrated to bigint")
	require.NotNil(t, integerFinding.Table)
	require.Equal(t, 1, len(integerFinding.Table.Rows))
	require.Equal(t, check.SeverityWarn, integerFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_IntegerShouldBeBigint_Critical(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.bookings_id_seq", "integer", "public.bookings", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 3,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	// Find the integer-columns finding
	var integerFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDIntegerColumns {
			integerFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, integerFinding)
	require.Equal(t, check.SeverityFail, integerFinding.Severity)
	require.NotNil(t, integerFinding.Table)
	require.Equal(t, 1, len(integerFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, integerFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_IntegerShouldBeBigint_Multiple(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "critical_seq", "integer", "critical_table", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 0,
		),
		makeSequenceRow(
			"public", "warning_seq", "integer", "warning_table", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, 0,
		),
		makeSequenceRow(
			"public", "healthy_seq", "integer", "healthy_table", "id", "integer",
			100000, 2147483647, 1, 2147383647, 2147483647,
			0.0047, false, false, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	// Find the integer-columns finding
	var integerFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDIntegerColumns {
			integerFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, integerFinding)
	require.Equal(t, check.SeverityFail, integerFinding.Severity)
	require.Contains(t, integerFinding.Details, "2 integer column(s)")
	require.NotNil(t, integerFinding.Table)
	require.Equal(t, 2, len(integerFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, integerFinding.Table.Rows[0].Severity)
	require.Equal(t, check.SeverityWarn, integerFinding.Table.Rows[1].Severity)
}

func TestSequenceHealth_TypeMismatch(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.problem_seq", "bigint", "public.problem_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, true, 0, // sequence_exceeds_column = true
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)

	// Find the type-mismatch finding
	var mismatchFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTypeMismatch {
			mismatchFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, mismatchFinding)
	require.Equal(t, check.SeverityInfo, mismatchFinding.Severity)
	require.Contains(t, mismatchFinding.Details, "exceeding their column's capacity")
	require.NotNil(t, mismatchFinding.Table)
	require.Equal(t, 1, len(mismatchFinding.Table.Rows))
	require.Equal(t, check.SeverityInfo, mismatchFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_TypeMismatch_Multiple(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "problem1_seq", "bigint", "problem1_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, true, 0,
		),
		makeSequenceRow(
			"public", "problem2_seq", "bigint", "problem2_table", "id", "integer",
			500000, 9223372036854775807, 1, 9223372036854275807, 2147483647,
			0.000005, false, true, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)

	// Find the type-mismatch finding
	var mismatchFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTypeMismatch {
			mismatchFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, mismatchFinding)
	require.Equal(t, check.SeverityInfo, mismatchFinding.Severity)
	require.Contains(t, mismatchFinding.Details, "2 sequence(s)")
	require.NotNil(t, mismatchFinding.Table)
	require.Equal(t, 2, len(mismatchFinding.Table.Rows))
}

func TestSequenceHealth_ComplexScenario_AllSubchecks(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		// Near exhaustion - critical
		makeSequenceRow(
			"public", "exhausted_seq", "integer", "exhausted_table", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 2,
		),
		// Should be bigint - warning
		makeSequenceRow(
			"public", "needs_bigint_seq", "integer", "needs_bigint_table", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, 0,
		),
		// Type mismatch
		makeSequenceRow(
			"public", "mismatch_seq", "bigint", "mismatch_table", "id", "integer",
			100000, 9223372036854775807, 1, 9223372036854675807, 2147483647,
			0.000001, false, true, true, 1,
		),
		// Healthy
		makeSequenceRow(
			"public", "healthy_seq", "bigint", "healthy_table", "id", "bigint",
			1000000, 9223372036854775807, 1, 9223372036853775807, 9223372036854775807,
			0.00001, false, false, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)
	require.GreaterOrEqual(t, len(report.Results), 3) // at least 3 findings (could be more if OK findings are reported)

	// Check all three findings are present and have correct severity
	findingIDs := map[string]check.Severity{}
	for _, finding := range report.Results {
		findingIDs[finding.ID] = finding.Severity
	}

	require.Equal(t, check.SeverityFail, findingIDs[findingIDNearExhaustion])
	require.Equal(t, check.SeverityFail, findingIDs[findingIDIntegerColumns])
	require.Equal(t, check.SeverityInfo, findingIDs[findingIDTypeMismatch])
}

func TestSequenceHealth_EdgeCase_ExactThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		seqType          string
		usagePercent     float64
		expectedSeverity check.Severity
	}{
		{name: "integer 49.9%", seqType: "integer", usagePercent: 49.9, expectedSeverity: check.SeverityPass},
		{name: "integer 50%", seqType: "integer", usagePercent: 50, expectedSeverity: check.SeverityWarn},
		{name: "integer 89.9%", seqType: "integer", usagePercent: 89.9, expectedSeverity: check.SeverityWarn},
		{name: "integer 90%", seqType: "integer", usagePercent: 90, expectedSeverity: check.SeverityFail},
		{name: "bigint 74.9%", seqType: "bigint", usagePercent: 74.9, expectedSeverity: check.SeverityPass},
		{name: "bigint 75%", seqType: "bigint", usagePercent: 75, expectedSeverity: check.SeverityWarn},
		{name: "bigint 89.9%", seqType: "bigint", usagePercent: 89.9, expectedSeverity: check.SeverityWarn},
		{name: "bigint 90%", seqType: "bigint", usagePercent: 90, expectedSeverity: check.SeverityFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := []db.SequenceHealthRow{
				makeSequenceRow(
					"public", "test_seq", tt.seqType, "", "", "",
					1000, 2147483647, 1, 1000, 0,
					tt.usagePercent, false, false, false, 0,
				),
			}

			report, err := sequencehealth.New(&mockQueryer{rows: rows}).Check(context.Background())
			require.NoError(t, err)

			finding := findingByID(t, report, findingIDNearExhaustion)
			assert.Equal(t, tt.expectedSeverity, finding.Severity,
				"usage=%.2f%% should be %s", tt.usagePercent, tt.expectedSeverity)
		})
	}
}

func findingByID(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()
	for _, finding := range report.Results {
		if finding.ID == id {
			return finding
		}
	}
	require.FailNow(t, "finding not found", id)
	return check.Finding{}
}

func TestSequenceHealth_ColumnCapacity(t *testing.T) {
	t.Parallel()

	const bigintMax = 9223372036854775807

	tests := []struct {
		name            string
		row             db.SequenceHealthRow
		integerColumns  check.Severity
		typeMismatch    check.Severity
		columnUsageCell string
	}{
		{
			name: "integer column 49.9%",
			row: makeSequenceRow("public", "public.t_id_seq", "integer", "public.t", "id", "integer",
				1071594330, 2147483647, 1, 1075889317, 2147483647, 49.9, false, false, true, 0),
			integerColumns: check.SeverityPass, typeMismatch: check.SeverityPass,
		},
		{
			name: "integer column 50%",
			row: makeSequenceRow("public", "public.t_id_seq", "integer", "public.t", "id", "integer",
				1073741824, 2147483647, 1, 1073741823, 2147483647, 50, false, false, true, 0),
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityPass,
		},
		{
			name: "integer column 89.9%",
			row: makeSequenceRow("public", "public.t_id_seq", "integer", "public.t", "id", "integer",
				1930587800, 2147483647, 1, 216895847, 2147483647, 89.9, false, false, true, 0),
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityPass,
		},
		{
			name: "integer column 90%",
			row: makeSequenceRow("public", "public.t_id_seq", "integer", "public.t", "id", "integer",
				1932735283, 2147483647, 1, 214748364, 2147483647, 90, false, false, true, 0),
			integerColumns: check.SeverityFail, typeMismatch: check.SeverityPass,
		},
		{
			name: "bigint sequence feeds an integer column at 60%",
			row: makeSequenceRow("public", "public.t_id_seq", "bigint", "public.t", "id", "integer",
				1288490189, bigintMax, 1, bigintMax-1288490189, 2147483647, 0.00, false, true, true, 0),
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityWarn, columnUsageCell: "60.0%",
		},
		{
			name: "bigint sequence feeds an integer column at 95%",
			row: makeSequenceRow("public", "public.t_id_seq", "bigint", "public.t", "id", "integer",
				2040109465, bigintMax, 1, bigintMax-2040109465, 2147483647, 0.00, false, true, true, 0),
			integerColumns: check.SeverityFail, typeMismatch: check.SeverityFail, columnUsageCell: "95.0%",
		},
		{
			name: "bigint sequence already beyond the integer column range",
			row: makeSequenceRow("public", "public.t_id_seq", "bigint", "public.t", "id", "integer",
				3000000000, bigintMax, 1, bigintMax-3000000000, 2147483647, 0.00, false, true, true, 0),
			integerColumns: check.SeverityFail, typeMismatch: check.SeverityFail, columnUsageCell: "139.7%",
		},
		{
			name: "descending bigint sequence feeds an integer column at 95%",
			row: makeSequenceRow("public", "public.t_id_seq", "bigint", "public.t", "id", "integer",
				-2040109466, -1, -1, bigintMax-2040109466, 2147483647, 0.00, false, true, true, 0),
			integerColumns: check.SeverityFail, typeMismatch: check.SeverityFail, columnUsageCell: "95.0%",
		},
		{
			name: "ascending bigint sequence already below the integer column range",
			row: makeSequenceRow("public", "public.t_id_seq", "bigint", "public.t", "id", "integer",
				-3000000000, bigintMax, 1, bigintMax, 2147483647, 0.00, false, true, true, 0),
			integerColumns: check.SeverityFail, typeMismatch: check.SeverityFail, columnUsageCell: "139.7%",
		},
		{
			name: "descending integer sequence at 50% of the column",
			row: makeSequenceRow("public", "public.t_id_seq", "integer", "public.t", "id", "integer",
				-1073741824, -1, -1, 1073741824, 2147483647, 50, false, false, true, 0),
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report, err := sequencehealth.New(&mockQueryer{rows: []db.SequenceHealthRow{tt.row}}).Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			assert.Equal(t, tt.integerColumns, findingByID(t, report, findingIDIntegerColumns).Severity)
			mismatch := findingByID(t, report, findingIDTypeMismatch)
			assert.Equal(t, tt.typeMismatch, mismatch.Severity)
			if tt.columnUsageCell != "" {
				assert.Equal(t, tt.columnUsageCell, mismatch.Table.Rows[0].Cells[6])
			}
		})
	}
}

func TestSequenceHealth_TypeMismatch_Unreadable(t *testing.T) {
	t.Parallel()

	mismatched := unreadableRow("c_id_seq", "c")
	mismatched.SeqDataType = pgtype.Text{String: "bigint", Valid: true}
	mismatched.MaxValue = pgtype.Int8{Int64: 9223372036854775807, Valid: true}
	mismatched.ColumnMaxValue = pgtype.Int8{Int64: 2147483647, Valid: true}
	mismatched.SequenceExceedsColumn = pgtype.Bool{Bool: true, Valid: true}

	readable := makeSequenceRow("public", "public.t_id_seq", "bigint", "public.t", "id", "bigint",
		1000, 9223372036854775807, 1, 9223372036854774807, 9223372036854775807, 0.00, false, false, true, 0)

	report, err := sequencehealth.New(&mockQueryer{rows: []db.SequenceHealthRow{readable, mismatched}}).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	mismatch := findingByID(t, report, findingIDTypeMismatch)
	assert.Equal(t, check.SeverityInfo, mismatch.Severity)
	assert.Equal(t, "-", mismatch.Table.Rows[0].Cells[6])
}

func TestSequenceHealth_Config(t *testing.T) {
	t.Parallel()

	const bigintMax = 9223372036854775807

	rows := []db.SequenceHealthRow{
		makeSequenceRow("public", "public.a_id_seq", "integer", "public.a", "id", "integer",
			858993459, 2147483647, 1, 1288490188, 2147483647, 40, false, false, true, 0),
		makeSequenceRow("public", "public.b_id_seq", "bigint", "public.b", "id", "integer",
			1610612736, bigintMax, 1, bigintMax-1610612736, 2147483647, 0.00, false, true, true, 0),
		makeSequenceRow("public", "public.c_seq", "bigint", "", "", "",
			1000, bigintMax, 1, 1000, 0, 80, false, false, false, 0),
	}

	tests := []struct {
		name           string
		settings       map[string]string
		nearExhaustion int
		integerColumns check.Severity
		typeMismatch   check.Severity
	}{
		{
			name:     "defaults ignore pk-types keys",
			settings: nil, nearExhaustion: 1,
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityWarn,
		},
		{
			name:     "override",
			settings: map[string]string{"usage_warn_percent": "30", "usage_fail_percent": "75"}, nearExhaustion: 2,
			integerColumns: check.SeverityFail, typeMismatch: check.SeverityFail,
		},
		{
			name:     "warn not below fail keeps defaults",
			settings: map[string]string{"usage_warn_percent": "80", "usage_fail_percent": "70"}, nearExhaustion: 1,
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityWarn,
		},
		{
			name:     "invalid value keeps default",
			settings: map[string]string{"usage_fail_percent": "0"}, nearExhaustion: 1,
			integerColumns: check.SeverityWarn, typeMismatch: check.SeverityWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := check.Config{
				"sequence-health": tt.settings,
				"pk-types":        {"usage_warn_percent": "10", "usage_fail_percent": "20"},
			}
			report, err := sequencehealth.New(&mockQueryer{rows: rows}, cfg).Check(context.Background())
			require.NoError(t, err)

			near := findingByID(t, report, findingIDNearExhaustion)
			require.NotNil(t, near.Table)
			assert.Len(t, near.Table.Rows, tt.nearExhaustion)
			assert.Equal(t, tt.integerColumns, findingByID(t, report, findingIDIntegerColumns).Severity)
			assert.Equal(t, tt.typeMismatch, findingByID(t, report, findingIDTypeMismatch).Severity)
		})
	}
}

func TestValidateSetting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key, value string
		wantErr    bool
	}{
		{"usage_warn_percent", "50", false},
		{"usage_fail_percent", "100", false},
		{"usage_warn_percent", "49.5", false},
		{"usage_warn_percent", "0", true},
		{"usage_fail_percent", "101", true},
		{"usage_warn_percent", "-5", true},
		{"usage_warn_percent", "NaN", true},
		{"usage_fail_percent", "Inf", true},
		{"usage_fail_percent", "ninety", true},
		{"usage_percent", "50", true},
	}

	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			t.Parallel()

			err := sequencehealth.ValidateSetting(tt.key, tt.value)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSequenceHealth_TableFormatting_NearExhaustion(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.bookings_id_seq", "integer", "public.bookings", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 5,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	// Find the near-exhaustion finding
	var exhaustionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDNearExhaustion {
			exhaustionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, exhaustionFinding)
	require.NotNil(t, exhaustionFinding.Table)

	table := exhaustionFinding.Table
	require.Equal(t, []string{"Sequence", "Table.Column", "Usage", "Remaining", "Type"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))

	require.Equal(t, "public.bookings_id_seq", table.Rows[0].Cells[0])
	require.Equal(t, "public.bookings.id", table.Rows[0].Cells[1])
	require.Contains(t, table.Rows[0].Cells[2], "90.0%")
	require.NotEmpty(t, table.Rows[0].Cells[3]) // Remaining values (formatted)
	require.Equal(t, "integer", table.Rows[0].Cells[4])
}

func TestSequenceHealth_TableFormatting_IntegerColumns(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.users_id_seq", "integer", "public.users", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, 3,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	// Find the integer-columns finding
	var integerFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDIntegerColumns {
			integerFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, integerFinding)
	require.NotNil(t, integerFinding.Table)

	table := integerFinding.Table
	require.Equal(t, []string{"Table", "Column", "Type", "Usage", "Current Value", "FKs"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))

	require.Equal(t, "public.users", table.Rows[0].Cells[0])
	require.Equal(t, "id", table.Rows[0].Cells[1])
	require.Equal(t, "integer", table.Rows[0].Cells[2])
	require.Contains(t, table.Rows[0].Cells[3], "50.0%")
	require.NotEmpty(t, table.Rows[0].Cells[4]) // Current value (formatted)
}

func TestSequenceHealth_TableFormatting_TypeMismatch(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.problem_seq", "bigint", "public.problem_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	// Find the type-mismatch finding
	var mismatchFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTypeMismatch {
			mismatchFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, mismatchFinding)
	require.NotNil(t, mismatchFinding.Table)

	table := mismatchFinding.Table
	require.Equal(t, []string{"Sequence", "Table.Column", "Column Type", "Seq Min", "Seq Max", "Column Max", "Column Usage"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))

	require.Equal(t, "public.problem_seq", table.Rows[0].Cells[0])
	require.Equal(t, "public.problem_table.id", table.Rows[0].Cells[1])
	require.Equal(t, "integer", table.Rows[0].Cells[2])
	require.NotEmpty(t, table.Rows[0].Cells[3]) // Seq Max (formatted)
	require.NotEmpty(t, table.Rows[0].Cells[5]) // Column Max (formatted)
	require.Equal(t, "0.1%", table.Rows[0].Cells[6])
}

func TestSequenceHealth_SequenceWithoutColumn(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "standalone_seq", "bigint", "", "", "",
			1932735283, 9223372036854775807, 1, 9223372035922040524, 0,
			0.00002, false, false, false, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)

	// All subchecks should be OK (no column means no problems)
	for _, finding := range report.Results {
		require.Equal(t, check.SeverityPass, finding.Severity)
	}
}

func TestSequenceHealth_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{err: expectedErr}
	checker := sequencehealth.New(queryer)

	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "sequence health")
}

func TestSequenceHealth_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.SequenceHealthRow{}}
	checker := sequencehealth.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "sequence-health", metadata.CheckID)
	require.Equal(t, "Sequence Health", metadata.Name)
	require.Equal(t, check.CategorySchema, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
	require.Contains(t, metadata.Description, "sequences")
	require.Contains(t, metadata.Description, "bigint")
}

func TestSequenceHealth_PrescriptionContent_NearExhaustion(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "orders_id_seq", "integer", "orders", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, 2,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	// Find the near-exhaustion finding
	var exhaustionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDNearExhaustion {
			exhaustionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, exhaustionFinding)
}

func TestSequenceHealth_PrescriptionContent_IntegerColumns(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.users_id_seq", "integer", "public.users", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, 3,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	// Find the integer-columns finding
	var integerFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDIntegerColumns {
			integerFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, integerFinding)
}

func TestSequenceHealth_PrescriptionContent_TypeMismatch(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "public.problem_seq", "bigint", "public.problem_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	// Find the type-mismatch finding
	var mismatchFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTypeMismatch {
			mismatchFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, mismatchFinding)
}

func TestSequenceHealth_SmallintType(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "lookup_id_seq", "smallint", "lookup", "id", "smallint",
			24575, 32767, 1, 8192, 32767,
			75.0, false, false, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	// Should trigger both near-exhaustion and should-be-bigint
	findingIDs := map[string]check.Severity{}
	for _, finding := range report.Results {
		findingIDs[finding.ID] = finding.Severity
	}

	require.Equal(t, check.SeverityWarn, findingIDs[findingIDNearExhaustion])
	require.Equal(t, check.SeverityWarn, findingIDs[findingIDIntegerColumns])
}

func TestSequenceHealth_IntegerColumns_FKCount(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "tenants_id_seq", "integer", "tenants", "id", "integer",
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, 3,
		),
	}

	report, err := sequencehealth.New(&mockQueryer{rows: rows}).Check(context.Background())
	require.NoError(t, err)

	var integerFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDIntegerColumns {
			integerFinding = &report.Results[i]
		}
	}
	require.NotNil(t, integerFinding)
	require.Equal(t, "3", integerFinding.Table.Rows[0].Cells[5])
}
