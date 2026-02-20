package uuiddefaults_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/uuiddefaults"
	"github.com/emancu/pgdoctor/db"
)

type mockQueryer struct {
	rows []db.UuidColumnDefaultsRow
	err  error
}

//nolint:revive,staticcheck // Match sqlc-generated method name
func (m *mockQueryer) UuidColumnDefaults(context.Context) ([]db.UuidColumnDefaultsRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func makeUUIDDefault(table, column, defaultExpr string, hasIndex bool) db.UuidColumnDefaultsRow {
	return db.UuidColumnDefaultsRow{
		TableName:   pgtype.Text{String: table, Valid: true},
		ColumnName:  pgtype.Text{String: column, Valid: true},
		DefaultExpr: pgtype.Text{String: defaultExpr, Valid: true},
		HasIndex:    pgtype.Bool{Bool: hasIndex, Valid: true},
	}
}

func Test_UUIDDefaults_NoIssues(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.UuidColumnDefaultsRow{}}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "No indexed UUID")
}

func Test_UUIDDefaults_NoIndex(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.users", "uuid", "gen_random_uuid()", false),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
}

func Test_UUIDDefaults_NotRandomUUID(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.users", "id", "uuid_generate_v7()", true),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
}

func Test_UUIDDefaults_RandomUUIDIndexed(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.users", "id", "gen_random_uuid()", true),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "Found 1 indexed UUID column")
	require.NotNil(t, report.Results[0].Table)
	require.Equal(t, 1, len(report.Results[0].Table.Rows))
}

func Test_UUIDDefaults_UUIDGenerateV4(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.orders", "order_id", "uuid_generate_v4()", true),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "random v4 defaults")
}

func Test_UUIDDefaults_Multiple(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.users", "id", "gen_random_uuid()", true),
		makeUUIDDefault("public.orders", "order_id", "uuid_generate_v4()", true),
		makeUUIDDefault("public.products", "product_id", "gen_random_uuid()", true),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)
	require.Contains(t, report.Results[0].Details, "Found 3 indexed UUID column")
	require.NotNil(t, report.Results[0].Table)
	require.Equal(t, 3, len(report.Results[0].Table.Rows))
}

func Test_UUIDDefaults_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{err: expectedErr}
	checker := uuiddefaults.New(queryer)

	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "UUID column defaults")
}

func Test_UUIDDefaults_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.UuidColumnDefaultsRow{}}
	checker := uuiddefaults.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "uuid-defaults", metadata.CheckID)
	require.Equal(t, "UUID Default Value Analysis", metadata.Name)
	require.Equal(t, check.CategoryPerformance, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}

func Test_UUIDDefaults_PrescriptionContent(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.users", "id", "gen_random_uuid()", true),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, len(report.Results))
}

func Test_UUIDDefaults_TableFormatting(t *testing.T) {
	t.Parallel()

	rows := []db.UuidColumnDefaultsRow{
		makeUUIDDefault("public.users", "user_id", "gen_random_uuid()", true),
		makeUUIDDefault("public.orders", "order_id", "uuid_generate_v4()", true),
	}

	queryer := &mockQueryer{rows: rows}
	checker := uuiddefaults.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, len(report.Results))
	require.NotNil(t, report.Results[0].Table)

	table := report.Results[0].Table
	require.Equal(t, []string{"Table", "Column", "Default"}, table.Headers)
	require.Equal(t, 2, len(table.Rows))

	require.Equal(t, "public.users", table.Rows[0].Cells[0])
	require.Equal(t, "user_id", table.Rows[0].Cells[1])
	require.Equal(t, "gen_random_uuid()", table.Rows[0].Cells[2])
	require.Equal(t, check.SeverityWarn, table.Rows[0].Severity)

	require.Equal(t, "public.orders", table.Rows[1].Cells[0])
	require.Equal(t, "order_id", table.Rows[1].Cells[1])
	require.Equal(t, "uuid_generate_v4()", table.Rows[1].Cells[2])
	require.Equal(t, check.SeverityWarn, table.Rows[1].Severity)
}

func Test_UUIDDefaults_FilteringLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		rows             []db.UuidColumnDefaultsRow
		expectedSeverity check.Severity
		expectedCount    int
	}{
		{
			name: "all filtered - not indexed",
			rows: []db.UuidColumnDefaultsRow{
				makeUUIDDefault("t1", "id", "gen_random_uuid()", false),
				makeUUIDDefault("t2", "id", "gen_random_uuid()", false),
			},
			expectedSeverity: check.SeverityOK,
			expectedCount:    0,
		},
		{
			name: "all filtered - not random UUID",
			rows: []db.UuidColumnDefaultsRow{
				makeUUIDDefault("t1", "id", "uuid_generate_v7()", true),
				makeUUIDDefault("t2", "id", "uuidv7()", true),
			},
			expectedSeverity: check.SeverityOK,
			expectedCount:    0,
		},
		{
			name: "mixed - some filtered",
			rows: []db.UuidColumnDefaultsRow{
				makeUUIDDefault("t1", "id", "gen_random_uuid()", true),
				makeUUIDDefault("t2", "id", "gen_random_uuid()", true),
				makeUUIDDefault("t3", "id", "gen_random_uuid()", false),
				makeUUIDDefault("t4", "id", "uuid_generate_v7()", true),
			},
			expectedSeverity: check.SeverityWarn,
			expectedCount:    2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: tt.rows}
			checker := uuiddefaults.New(queryer)

			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			require.Equal(t, tt.expectedSeverity, report.Severity)
			if tt.expectedCount > 0 {
				require.NotNil(t, report.Results[0].Table)
				require.Equal(t, tt.expectedCount, len(report.Results[0].Table.Rows))
			}
		})
	}
}
