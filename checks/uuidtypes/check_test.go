package uuidtypes_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/uuidtypes"
	"github.com/emancu/pgdoctor/db"
)

type mockQueryer struct {
	rows []db.UuidColumnsAsStringRow
	err  error
}

//nolint:revive,staticcheck // Match sqlc-generated method name
func (m *mockQueryer) UuidColumnsAsString(context.Context) ([]db.UuidColumnsAsStringRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func makeUUIDColumn(table, column, colType string, sizeBytes int64) db.UuidColumnsAsStringRow {
	return db.UuidColumnsAsStringRow{
		TableName:      table,
		ColumnName:     column,
		ColumnType:     colType,
		TableSizeBytes: sizeBytes,
	}
}

func Test_UUIDTypes_NoIssues(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.UuidColumnsAsStringRow{}}
	checker := uuidtypes.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "No UUID columns")
}

func Test_UUIDTypes_SingleColumn(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnsAsStringRow{
		makeUUIDColumn("public.users", "user_uuid", "varchar", 1000000),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuidtypes.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "Found 1 UUID column(s)")
	require.NotNil(t, report.Results[0].Table)
	require.Equal(t, 1, len(report.Results[0].Table.Rows))
}

func Test_UUIDTypes_MultipleColumns(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnsAsStringRow{
		makeUUIDColumn("public.users", "user_uuid", "text", 5000000),
		makeUUIDColumn("public.orders", "order_uuid", "varchar", 3000000),
		makeUUIDColumn("public.products", "product_uuid", "text", 2000000),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuidtypes.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "Found 3 UUID column(s)")
	require.NotNil(t, report.Results[0].Table)
	require.Equal(t, 3, len(report.Results[0].Table.Rows))
}

func Test_UUIDTypes_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{err: expectedErr}
	checker := uuidtypes.New(queryer)

	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "UUID column types")
}

func Test_UUIDTypes_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.UuidColumnsAsStringRow{}}
	checker := uuidtypes.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "uuid-types", metadata.CheckID)
	require.Equal(t, "UUID Type Validation", metadata.Name)
	require.Equal(t, check.CategorySchema, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}

func Test_UUIDTypes_PrescriptionContent(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnsAsStringRow{
		makeUUIDColumn("public.users", "user_uuid", "varchar", 1000000),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuidtypes.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, len(report.Results))
}

func Test_UUIDTypes_TableFormatting(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnsAsStringRow{
		makeUUIDColumn("public.users", "user_uuid", "varchar", 1000000),
		makeUUIDColumn("public.orders", "order_uuid", "text", 2000000),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuidtypes.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, len(report.Results))
	require.NotNil(t, report.Results[0].Table)

	table := report.Results[0].Table
	require.Equal(t, []string{"Table", "Column", "Type", "Size"}, table.Headers)
	require.Equal(t, 2, len(table.Rows))

	require.Equal(t, "public.users", table.Rows[0].Cells[0])
	require.Equal(t, "user_uuid", table.Rows[0].Cells[1])
	require.Equal(t, "varchar", table.Rows[0].Cells[2])
	require.NotEmpty(t, table.Rows[0].Cells[3])
	require.Equal(t, check.SeverityFail, table.Rows[0].Severity)

	require.Equal(t, "public.orders", table.Rows[1].Cells[0])
	require.Equal(t, "order_uuid", table.Rows[1].Cells[1])
	require.Equal(t, "text", table.Rows[1].Cells[2])
	require.NotEmpty(t, table.Rows[1].Cells[3])
	require.Equal(t, check.SeverityFail, table.Rows[1].Severity)
}

func Test_UUIDTypes_DifferentColumnTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		columnType string
	}{
		{"varchar type", "varchar"},
		{"text type", "text"},
		{"bpchar type", "bpchar"},
		{"char type", "char"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := []db.UuidColumnsAsStringRow{
				makeUUIDColumn("public.test", "test_uuid", tt.columnType, 1000),
			}

			queryer := &mockQueryer{rows: rows}
			checker := uuidtypes.New(queryer)

			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			require.Equal(t, check.SeverityWarn, report.Severity)
			require.Equal(t, 1, len(report.Results[0].Table.Rows))
			require.Equal(t, tt.columnType, report.Results[0].Table.Rows[0].Cells[2])
		})
	}
}
