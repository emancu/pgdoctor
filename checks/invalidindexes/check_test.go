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
	return db.BrokenIndexesRow{
		SchemaName: schema,
		TableName:  table,
		IndexName:  index,
		IsLeftover: false,
	}
}

func leftoverIndex(schema, table, index string) db.BrokenIndexesRow {
	return db.BrokenIndexesRow{
		SchemaName: schema,
		TableName:  table,
		IndexName:  index,
		IsLeftover: true,
	}
}

// findingByID returns the finding with the given ID, failing the test if absent.
func findingByID(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()
	for _, f := range report.Results {
		if f.ID == id {
			return f
		}
	}
	require.Failf(t, "finding not found", "no finding with ID %q", id)
	return check.Finding{}
}

func Test_InvalidIndexes(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Indexes          []db.BrokenIndexesRow
		ReportSeverity   check.Severity
		BrokenSeverity   check.Severity
		LeftoverSeverity check.Severity
	}

	testCases := []testCase{
		{
			Name:             "no rows - both OK, report OK",
			Indexes:          []db.BrokenIndexesRow{},
			ReportSeverity:   check.SeverityOK,
			BrokenSeverity:   check.SeverityOK,
			LeftoverSeverity: check.SeverityOK,
		},
		{
			Name: "only broken - broken FAIL, leftovers OK, report FAIL",
			Indexes: []db.BrokenIndexesRow{
				brokenIndex("public", "users", "idx_users_email"),
				brokenIndex("public", "posts", "idx_posts_created_at"),
			},
			ReportSeverity:   check.SeverityFail,
			BrokenSeverity:   check.SeverityFail,
			LeftoverSeverity: check.SeverityOK,
		},
		{
			Name: "only leftovers - broken OK, leftovers WARN, report WARN",
			Indexes: []db.BrokenIndexesRow{
				leftoverIndex("public", "users", "idx_users_email_ccnew"),
				leftoverIndex("public", "posts", "idx_posts_created_at_ccold1"),
			},
			ReportSeverity:   check.SeverityWarn,
			BrokenSeverity:   check.SeverityOK,
			LeftoverSeverity: check.SeverityWarn,
		},
		{
			Name: "mixed - broken FAIL, leftovers WARN, report FAIL",
			Indexes: []db.BrokenIndexesRow{
				brokenIndex("public", "users", "idx_users_email"),
				leftoverIndex("app", "orders", "idx_orders_status_ccnew"),
			},
			ReportSeverity:   check.SeverityFail,
			BrokenSeverity:   check.SeverityFail,
			LeftoverSeverity: check.SeverityWarn,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			checker := invalidindexes.New(newMockQueryer(tc.Indexes))
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			require.Equal(t, check.CategoryIndexes, report.Category, "Category should be indexes")
			require.Len(t, report.Results, 2, "Should always emit exactly 2 findings")
			require.Equal(t, tc.ReportSeverity, report.Severity, "Report severity should be the max")

			broken := findingByID(t, report, "broken-indexes")
			require.Equal(t, tc.BrokenSeverity, broken.Severity, "broken-indexes severity")

			leftovers := findingByID(t, report, "abandoned-leftovers")
			require.Equal(t, tc.LeftoverSeverity, leftovers.Severity, "abandoned-leftovers severity")
		})
	}
}

func Test_InvalidIndexes_NoRows_Details(t *testing.T) {
	t.Parallel()

	checker := invalidindexes.New(newMockQueryer(nil))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	broken := findingByID(t, report, "broken-indexes")
	require.Equal(t, check.SeverityOK, broken.Severity)
	require.Nil(t, broken.Table, "OK broken finding should have no table")
	require.Contains(t, broken.Details, "No broken indexes")

	leftovers := findingByID(t, report, "abandoned-leftovers")
	require.Equal(t, check.SeverityOK, leftovers.Severity)
	require.Nil(t, leftovers.Table, "OK leftover finding should have no table")
	require.Contains(t, leftovers.Details, "No abandoned")
}

func Test_InvalidIndexes_Broken_TableAndRemediation(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		brokenIndex("public", "users", "idx_users_email"),
		brokenIndex("app", "posts", "idx_posts_created_at"),
	}

	checker := invalidindexes.New(newMockQueryer(indexes))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	broken := findingByID(t, report, "broken-indexes")
	require.Equal(t, check.SeverityFail, broken.Severity)

	// Count phrasing (plural).
	require.Contains(t, broken.Details, "2 broken indexes")

	// Remediation commands present.
	require.Contains(t, broken.Details, "REINDEX INDEX CONCURRENTLY")
	require.Contains(t, broken.Details, "DROP INDEX CONCURRENTLY")

	// Table contains exactly the broken rows with the right cells.
	require.NotNil(t, broken.Table)
	require.Equal(t, []string{"Schema", "Table", "Index"}, broken.Table.Headers)
	require.Len(t, broken.Table.Rows, 2)
	require.Equal(t, []string{"public", "users", "idx_users_email"}, broken.Table.Rows[0].Cells)
	require.Equal(t, []string{"app", "posts", "idx_posts_created_at"}, broken.Table.Rows[1].Cells)
	require.Equal(t, check.SeverityFail, broken.Table.Rows[0].Severity)

	// The leftover finding stays OK with no table.
	leftovers := findingByID(t, report, "abandoned-leftovers")
	require.Equal(t, check.SeverityOK, leftovers.Severity)
	require.Nil(t, leftovers.Table)
}

func Test_InvalidIndexes_Leftovers_TableAndRemediation(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		leftoverIndex("public", "users", "idx_users_email_ccnew"),
	}

	checker := invalidindexes.New(newMockQueryer(indexes))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	leftovers := findingByID(t, report, "abandoned-leftovers")
	require.Equal(t, check.SeverityWarn, leftovers.Severity)

	// Count phrasing (singular).
	require.Contains(t, leftovers.Details, "1 abandoned leftover index")

	// Remediation command present (DROP only for leftovers).
	require.Contains(t, leftovers.Details, "DROP INDEX CONCURRENTLY")
	require.NotContains(t, leftovers.Details, "REINDEX INDEX CONCURRENTLY")

	require.NotNil(t, leftovers.Table)
	require.Equal(t, []string{"Schema", "Table", "Index"}, leftovers.Table.Headers)
	require.Len(t, leftovers.Table.Rows, 1)
	require.Equal(t, []string{"public", "users", "idx_users_email_ccnew"}, leftovers.Table.Rows[0].Cells)
	require.Equal(t, check.SeverityWarn, leftovers.Table.Rows[0].Severity)

	// The broken finding stays OK with no table.
	broken := findingByID(t, report, "broken-indexes")
	require.Equal(t, check.SeverityOK, broken.Severity)
	require.Nil(t, broken.Table)
}

func Test_InvalidIndexes_Mixed_Partitioning(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		brokenIndex("public", "users", "idx_users_email"),
		brokenIndex("public", "orders", "idx_orders_status"),
		leftoverIndex("app", "posts", "idx_posts_created_at_ccnew"),
	}

	checker := invalidindexes.New(newMockQueryer(indexes))
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	require.Equal(t, check.SeverityFail, report.Severity)

	broken := findingByID(t, report, "broken-indexes")
	require.Equal(t, check.SeverityFail, broken.Severity)
	require.Contains(t, broken.Details, "2 broken indexes")
	require.NotNil(t, broken.Table)
	require.Len(t, broken.Table.Rows, 2)

	leftovers := findingByID(t, report, "abandoned-leftovers")
	require.Equal(t, check.SeverityWarn, leftovers.Severity)
	require.Contains(t, leftovers.Details, "1 abandoned leftover index")
	require.NotNil(t, leftovers.Table)
	require.Len(t, leftovers.Table.Rows, 1)
	require.Equal(t, []string{"app", "posts", "idx_posts_created_at_ccnew"}, leftovers.Table.Rows[0].Cells)
}

func Test_InvalidIndexes_QueryError(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryerWithError(fmt.Errorf("database connection error"))

	checker := invalidindexes.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.ErrorContains(t, err, "invalid-indexes")
}

func Test_InvalidIndexes_Metadata(t *testing.T) {
	t.Parallel()

	m := invalidindexes.Metadata()

	require.Equal(t, "invalid-indexes", m.CheckID, "CheckID should match")
	require.Equal(t, "Invalid Indexes", m.Name, "Name should match")
	require.Equal(t, check.CategoryIndexes, m.Category, "Category should be indexes")
	require.NotEmpty(t, m.Description, "Description should not be empty")
	require.NotEmpty(t, m.SQL, "SQL should not be empty")
	require.NotEmpty(t, m.Readme, "Readme should not be empty")
}
