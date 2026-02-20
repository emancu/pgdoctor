package pktypes

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

type mockQueryer struct {
	rows []db.InvalidPrimaryKeyTypesRow
	err  error
}

//nolint:revive,staticcheck
func (m *mockQueryer) InvalidPrimaryKeyTypes(context.Context) ([]db.InvalidPrimaryKeyTypesRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func makePKRow(table, column, colType string, estRows, seqCurrent, maxValue int64, usagePct float64) db.InvalidPrimaryKeyTypesRow {
	return db.InvalidPrimaryKeyTypesRow{
		TableName:       pgtype.Text{String: table, Valid: true},
		ColumnName:      pgtype.Text{String: column, Valid: true},
		ColumnType:      pgtype.Text{String: colType, Valid: true},
		EstimatedRows:   pgtype.Int8{Int64: estRows, Valid: estRows > 0},
		SequenceCurrent: pgtype.Int8{Int64: seqCurrent, Valid: seqCurrent > 0},
		TypeMaxValue:    pgtype.Int8{Int64: maxValue, Valid: maxValue > 0},
		UsagePct:        pgtype.Numeric{Valid: usagePct >= 0, Int: nil, Exp: 0, NaN: false, InfinityModifier: 0},
	}
}

func makePKRowWithUsage(table, column, colType string, estRows, seqCurrent, maxValue int64, usagePct float64) db.InvalidPrimaryKeyTypesRow {
	row := makePKRow(table, column, colType, estRows, seqCurrent, maxValue, usagePct)
	if usagePct >= 0 {
		numeric := &pgtype.Numeric{}
		_ = numeric.Scan(fmt.Sprintf("%.10f", usagePct))
		row.UsagePct = *numeric
	}
	return row
}

func TestPKTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		data              []db.InvalidPrimaryKeyTypesRow
		severity          check.Severity
		wantOK            bool
		wantTableRows     int
		wantDetailsSubstr string
	}{
		{
			name:     "no issues",
			data:     []db.InvalidPrimaryKeyTypesRow{},
			severity: check.SeverityOK,
			wantOK:   true,
		},
		{
			name: "single table - high usage - FAIL",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.bookings", "id", "int4", 1_400_000_000, 1_495_000_000, 2_147_483_647, 0.696),
			},
			severity:          check.SeverityFail,
			wantOK:            false,
			wantTableRows:     1,
			wantDetailsSubstr: "1 CRITICAL",
		},
		{
			name: "single table - low usage - WARN",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.kyc_statuses", "id", "int4", 1_200_000, 0, 2_147_483_647, 0.001),
			},
			severity:          check.SeverityWarn,
			wantOK:            false,
			wantTableRows:     1,
			wantDetailsSubstr: "1 WARNING",
		},
		{
			name: "multiple tables - mixed severity",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.bookings", "id", "int4", 1_400_000_000, 1_495_000_000, 2_147_483_647, 0.696),
				makePKRowWithUsage("public.appointments", "id", "int4", 1_000_000_000, 1_108_000_000, 2_147_483_647, 0.516),
				makePKRowWithUsage("public.kyc_statuses", "id", "int4", 1_200_000, 0, 2_147_483_647, 0.001),
				makePKRowWithUsage("public.events", "id", "int4", 500_000, 0, 2_147_483_647, 0.0002),
			},
			severity:          check.SeverityFail,
			wantOK:            false,
			wantTableRows:     4,
			wantDetailsSubstr: "4 table(s)",
		},
		{
			name: "severity threshold - exactly 50%",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.test", "id", "int4", 1_073_741_824, 0, 2_147_483_647, 0.50),
			},
			severity:      check.SeverityFail,
			wantOK:        false,
			wantTableRows: 1,
		},
		{
			name: "severity threshold - just below 50%",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.test", "id", "int4", 1_073_741_823, 0, 2_147_483_647, 0.4999),
			},
			severity:      check.SeverityWarn,
			wantOK:        false,
			wantTableRows: 1,
		},
		{
			name: "int2 - smallint type",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.lookup", "id", "int2", 15_000, 0, 32_767, 0.458),
			},
			severity:      check.SeverityWarn,
			wantOK:        false,
			wantTableRows: 1,
		},
		{
			name: "empty table - zero usage",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.new_table", "id", "int4", 0, 0, 2_147_483_647, 0.0),
			},
			severity:      check.SeverityWarn,
			wantOK:        false,
			wantTableRows: 1,
		},
		{
			name: "all tables are violations",
			data: []db.InvalidPrimaryKeyTypesRow{
				makePKRowWithUsage("public.high_usage", "id", "int4", 1_000_000_000, 0, 2_147_483_647, 0.466),
				makePKRowWithUsage("public.medium_usage", "id", "int4", 100_000_000, 0, 2_147_483_647, 0.047),
				makePKRowWithUsage("public.low_usage", "id", "int4", 1_000, 0, 2_147_483_647, 0.0000005),
				makePKRowWithUsage("public.empty", "id", "int4", 0, 0, 2_147_483_647, 0.0),
			},
			severity:      check.SeverityWarn,
			wantOK:        false,
			wantTableRows: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: tt.data}
			checker := New(queryer)

			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.severity, report.Severity)

			if tt.wantOK {
				require.Len(t, report.Results, 1)
				assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
				assert.Contains(t, report.Results[0].Details, "All tables use bigint or UUID primary keys")
			} else {
				require.Len(t, report.Results, 1)
				assert.NotEqual(t, check.SeverityOK, report.Results[0].Severity)
				assert.NotNil(t, report.Results[0].Table)
				if tt.wantTableRows > 0 {
					assert.Len(t, report.Results[0].Table.Rows, tt.wantTableRows)
				}
				if tt.wantDetailsSubstr != "" {
					assert.Contains(t, report.Results[0].Details, tt.wantDetailsSubstr)
				}
			}
		})
	}
}

func TestPKTypes_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{err: expectedErr}
	checker := New(queryer)

	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "primary key types")
}

func TestPKTypes_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.InvalidPrimaryKeyTypesRow{}}
	checker := New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "pk-types", metadata.CheckID)
	require.Equal(t, "Primary Key Type Validation", metadata.Name)
	require.Equal(t, check.CategorySchema, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
	require.Contains(t, metadata.Description, "bigint or UUID")
}

func TestPKTypes_TableFormatting(t *testing.T) {
	t.Parallel()

	rows := []db.InvalidPrimaryKeyTypesRow{
		makePKRowWithUsage("public.bookings", "id", "int4", 1_400_000_000, 1_495_000_000, 2_147_483_647, 0.696),
		makePKRowWithUsage("public.kyc_statuses", "id", "int4", 1_200_000, 0, 2_147_483_647, 0.001),
	}

	queryer := &mockQueryer{rows: rows}
	checker := New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, len(report.Results))
	require.NotNil(t, report.Results[0].Table)

	table := report.Results[0].Table
	require.Equal(t, []string{"Table", "Column", "Type", "Usage %", "Rows"}, table.Headers)
	require.Equal(t, 2, len(table.Rows))

	require.Equal(t, "public.bookings", table.Rows[0].Cells[0])
	require.Equal(t, "id", table.Rows[0].Cells[1])
	require.Equal(t, "int4", table.Rows[0].Cells[2])
	require.Contains(t, table.Rows[0].Cells[3], "69.6%")
	require.Contains(t, table.Rows[0].Cells[4], "1.4B")
	require.Equal(t, check.SeverityFail, table.Rows[0].Severity)

	require.Equal(t, "public.kyc_statuses", table.Rows[1].Cells[0])
	require.Equal(t, "id", table.Rows[1].Cells[1])
	require.Equal(t, "int4", table.Rows[1].Cells[2])
	require.Contains(t, table.Rows[1].Cells[3], "0.1%")
	require.Contains(t, table.Rows[1].Cells[4], "1.2M")
	require.Equal(t, check.SeverityWarn, table.Rows[1].Severity)
}

func TestPKTypes_UsageDisplay(t *testing.T) {
	t.Parallel()

	row := makePKRowWithUsage("public.test", "id", "int4", 1_000_000, 1_500_000, 2_147_483_647, 0.0007)
	queryer := &mockQueryer{rows: []db.InvalidPrimaryKeyTypesRow{row}}
	checker := New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.NotNil(t, report.Results[0].Table)
	require.Len(t, report.Results[0].Table.Rows, 1)

	usageCell := report.Results[0].Table.Rows[0].Cells[3]
	assert.Contains(t, usageCell, "~0.1%")
}

func TestFormatDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		criticalCount int
		warningCount  int
		want          string
	}{
		{
			name:          "only critical",
			criticalCount: 3,
			warningCount:  0,
			want:          "Found 3 CRITICAL table(s) with non-bigint/UUID primary keys",
		},
		{
			name:          "only warnings",
			criticalCount: 0,
			warningCount:  5,
			want:          "Found 5 WARNING table(s) with non-bigint/UUID primary keys",
		},
		{
			name:          "mixed",
			criticalCount: 2,
			warningCount:  3,
			want:          "Found 5 table(s) with non-bigint/UUID primary keys: 2 CRITICAL, 3 WARNING",
		},
		{
			name:          "single critical",
			criticalCount: 1,
			warningCount:  0,
			want:          "Found 1 CRITICAL table(s) with non-bigint/UUID primary keys",
		},
		{
			name:          "single warning",
			criticalCount: 0,
			warningCount:  1,
			want:          "Found 1 WARNING table(s) with non-bigint/UUID primary keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatDetails(tt.criticalCount, tt.warningCount)
			assert.Equal(t, tt.want, got)
		})
	}
}
