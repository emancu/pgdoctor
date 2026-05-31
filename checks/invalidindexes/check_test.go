package invalidindexes_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/invalidindexes"
	"github.com/emancu/pgdoctor/db"
	"github.com/stretchr/testify/require"
)

// Mock queryer for testing.
type mockInvalidIndexesQueryer struct {
	indexes []db.BrokenIndexesRow
	err     error
}

func (m *mockInvalidIndexesQueryer) BrokenIndexes(context.Context) ([]db.BrokenIndexesRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.indexes, nil
}

func newMockQueryer(indexes []db.BrokenIndexesRow) *mockInvalidIndexesQueryer {
	return &mockInvalidIndexesQueryer{indexes: indexes}
}

func newMockQueryerWithError(err error) *mockInvalidIndexesQueryer {
	return &mockInvalidIndexesQueryer{err: err}
}

func brokenIndex(schema, table, index string) db.BrokenIndexesRow {
	return db.BrokenIndexesRow{SchemaName: schema, TableName: table, IndexName: index, IsLeftover: false}
}

func leftoverIndex(schema, table, index string) db.BrokenIndexesRow {
	return db.BrokenIndexesRow{SchemaName: schema, TableName: table, IndexName: index, IsLeftover: true}
}

// onlyFinding returns the single finding the check always emits.
func onlyFinding(t *testing.T, report *check.Report) check.Finding {
	t.Helper()
	require.Len(t, report.Results, 1, "invalid-indexes emits exactly one finding")
	return report.Results[0]
}

func Test_InvalidIndexes_Severity(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name     string
		Indexes  []db.BrokenIndexesRow
		Severity check.Severity
	}{
		{
			Name:     "no invalid indexes - OK",
			Indexes:  []db.BrokenIndexesRow{},
			Severity: check.SeverityOK,
		},
		{
			Name:     "broken index - WARN",
			Indexes:  []db.BrokenIndexesRow{brokenIndex("public", "users", "idx_users_email")},
			Severity: check.SeverityWarn,
		},
		{
			Name:     "abandoned leftover - WARN",
			Indexes:  []db.BrokenIndexesRow{leftoverIndex("public", "users", "idx_users_email_ccnew")},
			Severity: check.SeverityWarn,
		},
		{
			Name: "mixed - WARN",
			Indexes: []db.BrokenIndexesRow{
				brokenIndex("public", "users", "idx_users_email"),
				leftoverIndex("app", "orders", "idx_orders_status_ccnew"),
			},
			Severity: check.SeverityWarn,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			checker := invalidindexes.New(newMockQueryer(tc.Indexes))
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			require.Equal(t, check.CategoryIndexes, report.Category)
			require.Equal(t, "invalid-indexes", report.CheckID)
			require.Equal(t, tc.Severity, report.Severity)

			finding := onlyFinding(t, report)
			require.Equal(t, "invalid-indexes", finding.ID)
			require.Equal(t, tc.Severity, finding.Severity)
		})
	}
}

func Test_InvalidIndexes_OK_NoDetailsNoTable(t *testing.T) {
	t.Parallel()

	checker := invalidindexes.New(newMockQueryer(nil))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	finding := onlyFinding(t, report)
	require.Equal(t, check.SeverityOK, finding.Severity)
	require.Empty(t, finding.Details, "OK finding carries no details")
	require.Nil(t, finding.Table, "OK finding carries no table")
}

func Test_InvalidIndexes_ClassifiesRowsByType(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		brokenIndex("public", "users", "idx_users_email"),
		brokenIndex("public", "orders", "idx_orders_status"),
		leftoverIndex("app", "posts", "idx_posts_created_at_ccnew"),
	}

	checker := invalidindexes.New(newMockQueryer(indexes))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	finding := onlyFinding(t, report)
	require.Equal(t, check.SeverityWarn, finding.Severity)

	// Terse summary with the per-class breakdown.
	require.Contains(t, finding.Details, "3 invalid indexes")
	require.Contains(t, finding.Details, "2 broken")
	require.Contains(t, finding.Details, "1 leftover")

	// Fix instructions stay out of the run output (README / explain only).
	require.NotContains(t, finding.Details, "CONCURRENTLY")

	// Table carries the broken/leftover distinction in a Type column.
	require.NotNil(t, finding.Table)
	require.Equal(t, []string{"Schema", "Table", "Index", "Type"}, finding.Table.Headers)
	require.Len(t, finding.Table.Rows, 3)
	require.Equal(t, []string{"public", "users", "idx_users_email", "broken"}, finding.Table.Rows[0].Cells)
	require.Equal(t, []string{"app", "posts", "idx_posts_created_at_ccnew", "leftover"}, finding.Table.Rows[2].Cells)
	for _, row := range finding.Table.Rows {
		require.Equal(t, check.SeverityWarn, row.Severity)
	}
}

func Test_InvalidIndexes_SingularPhrasing(t *testing.T) {
	t.Parallel()

	checker := invalidindexes.New(newMockQueryer([]db.BrokenIndexesRow{
		leftoverIndex("public", "users", "idx_users_email_ccnew"),
	}))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	finding := onlyFinding(t, report)
	require.Contains(t, finding.Details, "1 invalid index ")
	require.NotContains(t, finding.Details, "invalid indexes")
}

func Test_InvalidIndexes_QueryError(t *testing.T) {
	t.Parallel()

	checker := invalidindexes.New(newMockQueryerWithError(fmt.Errorf("database connection error")))
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.ErrorContains(t, err, "invalid-indexes")
}

func Test_InvalidIndexes_Metadata(t *testing.T) {
	t.Parallel()

	m := invalidindexes.Metadata()

	require.Equal(t, "invalid-indexes", m.CheckID)
	require.Equal(t, "Invalid Indexes", m.Name)
	require.Equal(t, check.CategoryIndexes, m.Category)
	require.NotEmpty(t, m.Description)
	require.NotEmpty(t, m.SQL)
	require.NotEmpty(t, m.Readme)
}
