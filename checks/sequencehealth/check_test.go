package sequencehealth_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/sequencehealth"
	"github.com/emancu/pgdoctor/db"
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
	isCyclic, sequenceExceedsColumn, shouldBeBigint, isPrimaryKey bool,
	fkReferenceCount int64,
) db.SequenceHealthRow {
	usageNumeric := &pgtype.Numeric{}
	_ = usageNumeric.Scan(fmt.Sprintf("%.2f", usagePercent))

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
		ShouldBeBigint:        pgtype.Bool{Bool: shouldBeBigint, Valid: true},
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
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "No sequences found")
}

func TestSequenceHealth_AllHealthy(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "bookings_id_seq", "bigint", "bookings", "id", "bigint",
			1000000, 9223372036854775807, 1, 9223372036853775807, 9223372036854775807,
			0.00001, false, false, false, true, 5,
		),
		makeSequenceRow(
			"public", "users_id_seq", "bigint", "users", "id", "bigint",
			500000, 9223372036854775807, 1, 9223372036854275807, 9223372036854775807,
			0.000005, false, false, false, true, 3,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
	require.Equal(t, 3, len(report.Results))

	// All three subchecks should be OK
	for _, finding := range report.Results {
		require.Equal(t, check.SeverityOK, finding.Severity)
	}
}

func TestSequenceHealth_NearExhaustion_Critical(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "orders_id_seq", "integer", "orders", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, true, 2,
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
	require.Contains(t, exhaustionFinding.Details, ">90%")
	require.NotNil(t, exhaustionFinding.Table)
	require.Equal(t, 1, len(exhaustionFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, exhaustionFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_NearExhaustion_Warning(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "products_id_seq", "integer", "products", "id", "integer",
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity) // overall is FAIL because should_be_bigint is true

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
			90.0, false, false, true, true, 0,
		),
		makeSequenceRow(
			"public", "warning_seq", "integer", "warning_table", "id", "integer",
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, true, 0,
		),
		makeSequenceRow(
			"public", "healthy_seq", "bigint", "healthy_table", "id", "bigint",
			1000000, 9223372036854775807, 1, 9223372036853775807, 9223372036854775807,
			0.00001, false, false, false, true, 0,
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
	require.Contains(t, exhaustionFinding.Details, "1 sequence(s) at >90%")
	require.Contains(t, exhaustionFinding.Details, "1 more at >75%")
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
			90.0, true, false, true, false, 0, // is_cyclic = true
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
	require.Equal(t, check.SeverityOK, exhaustionFinding.Severity)
	require.Contains(t, exhaustionFinding.Details, "sufficient headroom")
}

func TestSequenceHealth_IntegerShouldBeBigint_Warning(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "users_id_seq", "integer", "users", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, true, 5,
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
	require.Contains(t, integerFinding.Details, ">50%")
	require.Contains(t, integerFinding.Details, "migrated to bigint")
	require.NotNil(t, integerFinding.Table)
	require.Equal(t, 1, len(integerFinding.Table.Rows))
	require.Equal(t, check.SeverityWarn, integerFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_IntegerShouldBeBigint_Critical(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "bookings_id_seq", "integer", "bookings", "id", "integer",
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, true, 3,
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
			1610612735, 2147483647, 1, 536870912, 2147483647,
			75.0, false, false, true, true, 0,
		),
		makeSequenceRow(
			"public", "warning_seq", "integer", "warning_table", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, true, 0,
		),
		makeSequenceRow(
			"public", "healthy_seq", "integer", "healthy_table", "id", "integer",
			100000, 2147483647, 1, 2147383647, 2147483647,
			0.0047, false, false, false, true, 0,
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
			"public", "problem_seq", "bigint", "problem_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, false, true, 0, // sequence_exceeds_column = true
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	// Find the type-mismatch finding
	var mismatchFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTypeMismatch {
			mismatchFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, mismatchFinding)
	require.Equal(t, check.SeverityFail, mismatchFinding.Severity)
	require.Contains(t, mismatchFinding.Details, "exceeding their column's capacity")
	require.NotNil(t, mismatchFinding.Table)
	require.Equal(t, 1, len(mismatchFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, mismatchFinding.Table.Rows[0].Severity)
}

func TestSequenceHealth_TypeMismatch_Multiple(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "problem1_seq", "bigint", "problem1_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, false, true, 0,
		),
		makeSequenceRow(
			"public", "problem2_seq", "bigint", "problem2_table", "id", "integer",
			500000, 9223372036854775807, 1, 9223372036854275807, 2147483647,
			0.000005, false, true, false, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	// Find the type-mismatch finding
	var mismatchFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTypeMismatch {
			mismatchFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, mismatchFinding)
	require.Equal(t, check.SeverityFail, mismatchFinding.Severity)
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
			90.0, false, false, true, true, 2,
		),
		// Should be bigint - warning
		makeSequenceRow(
			"public", "needs_bigint_seq", "integer", "needs_bigint_table", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, true, 0,
		),
		// Type mismatch
		makeSequenceRow(
			"public", "mismatch_seq", "bigint", "mismatch_table", "id", "integer",
			100000, 9223372036854775807, 1, 9223372036854675807, 2147483647,
			0.000001, false, true, false, true, 1,
		),
		// Healthy
		makeSequenceRow(
			"public", "healthy_seq", "bigint", "healthy_table", "id", "bigint",
			1000000, 9223372036854775807, 1, 9223372036853775807, 9223372036854775807,
			0.00001, false, false, false, true, 0,
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
	// integer-columns will be FAIL because exhausted_seq at 90% is also >75%
	require.Equal(t, check.SeverityFail, findingIDs[findingIDIntegerColumns])
	require.Equal(t, check.SeverityFail, findingIDs[findingIDTypeMismatch])
}

func TestSequenceHealth_EdgeCase_ExactThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		usagePercent     float64
		expectedSeverity check.Severity
		subcheckID       string
	}{
		{
			name:             "exactly 75% - warning threshold",
			usagePercent:     75.0,
			expectedSeverity: check.SeverityWarn,
			subcheckID:       findingIDNearExhaustion,
		},
		{
			name:             "exactly 90% - critical threshold",
			usagePercent:     90.0,
			expectedSeverity: check.SeverityFail,
			subcheckID:       findingIDNearExhaustion,
		},
		{
			name:             "just below 75%",
			usagePercent:     74.99,
			expectedSeverity: check.SeverityOK,
			subcheckID:       findingIDNearExhaustion,
		},
		{
			name:             "just above 90%",
			usagePercent:     90.01,
			expectedSeverity: check.SeverityFail,
			subcheckID:       findingIDNearExhaustion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := []db.SequenceHealthRow{
				makeSequenceRow(
					"public", "test_seq", "integer", "test_table", "id", "integer",
					int64(tt.usagePercent*2147483647/100), 2147483647, 1,
					int64((100-tt.usagePercent)*2147483647/100), 2147483647,
					tt.usagePercent, false, false, tt.usagePercent > 50, true, 0,
				),
			}

			queryer := &mockQueryer{rows: rows}
			checker := sequencehealth.New(queryer)

			report, err := checker.Check(context.Background())

			require.NoError(t, err)

			// Find the specific subcheck
			var targetFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == tt.subcheckID {
					targetFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, targetFinding)
			assert.Equal(t, tt.expectedSeverity, targetFinding.Severity,
				"usage=%.2f%% should be %s", tt.usagePercent, tt.expectedSeverity)
		})
	}
}

func TestSequenceHealth_TableFormatting_NearExhaustion(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "bookings_id_seq", "integer", "bookings", "id", "integer",
			1932735283, 2147483647, 1, 214748364, 2147483647,
			90.0, false, false, true, true, 5,
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

	require.Equal(t, "bookings_id_seq", table.Rows[0].Cells[0])
	require.Equal(t, "bookings.id", table.Rows[0].Cells[1])
	require.Contains(t, table.Rows[0].Cells[2], "90.0%")
	require.NotEmpty(t, table.Rows[0].Cells[3]) // Remaining values (formatted)
	require.Equal(t, "integer", table.Rows[0].Cells[4])
}

func TestSequenceHealth_TableFormatting_IntegerColumns(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "users_id_seq", "integer", "users", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, true, 3,
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
	require.Equal(t, []string{"Table", "Column", "Type", "Usage", "Current Value"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))

	require.Equal(t, "users", table.Rows[0].Cells[0])
	require.Equal(t, "id", table.Rows[0].Cells[1])
	require.Equal(t, "integer", table.Rows[0].Cells[2])
	require.Contains(t, table.Rows[0].Cells[3], "50.0%")
	require.NotEmpty(t, table.Rows[0].Cells[4]) // Current value (formatted)
}

func TestSequenceHealth_TableFormatting_TypeMismatch(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "problem_seq", "bigint", "problem_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, false, true, 0,
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
	require.Equal(t, []string{"Sequence", "Table.Column", "Column Type", "Seq Max", "Column Max"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))

	require.Equal(t, "problem_seq", table.Rows[0].Cells[0])
	require.Equal(t, "problem_table.id", table.Rows[0].Cells[1])
	require.Equal(t, "integer", table.Rows[0].Cells[2])
	require.NotEmpty(t, table.Rows[0].Cells[3]) // Seq Max (formatted)
	require.NotEmpty(t, table.Rows[0].Cells[4]) // Column Max (formatted)
}

func TestSequenceHealth_SequenceWithoutColumn(t *testing.T) {
	t.Parallel()

	rows := []db.SequenceHealthRow{
		makeSequenceRow(
			"public", "standalone_seq", "bigint", "", "", "",
			1932735283, 9223372036854775807, 1, 9223372035922040524, 0,
			0.00002, false, false, false, false, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)

	// All subchecks should be OK (no column means no problems)
	for _, finding := range report.Results {
		require.Equal(t, check.SeverityOK, finding.Severity)
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
			90.0, false, false, true, true, 2,
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
			"public", "users_id_seq", "integer", "users", "id", "integer",
			1073741824, 2147483647, 1, 1073741823, 2147483647,
			50.01, false, false, true, true, 3,
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
			"public", "problem_seq", "bigint", "problem_table", "id", "integer",
			1000000, 9223372036854775807, 1, 9223372036853775807, 2147483647,
			0.00001, false, true, false, true, 0,
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
			75.0, false, false, true, true, 0,
		),
	}

	queryer := &mockQueryer{rows: rows}
	checker := sequencehealth.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity) // FAIL because >75% triggers FAIL in integer-columns

	// Should trigger both near-exhaustion and should-be-bigint
	findingIDs := map[string]check.Severity{}
	for _, finding := range report.Results {
		findingIDs[finding.ID] = finding.Severity
	}

	require.Equal(t, check.SeverityWarn, findingIDs[findingIDNearExhaustion])
	require.Equal(t, check.SeverityFail, findingIDs[findingIDIntegerColumns])
}
