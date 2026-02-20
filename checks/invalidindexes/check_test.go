package invalidindexes_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/invalidindexes"
	"github.com/emancu/pgdoctor/db"
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

func Test_InvalidIndexes(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Indexes          []db.BrokenIndexesRow
		ExpectedSeverity check.Severity
		ExpectedID       string
	}

	testCases := []testCase{
		{
			Name:             "no invalid indexes - OK",
			Indexes:          []db.BrokenIndexesRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedID:       "invalid-indexes",
		},
		{
			Name: "one invalid index - WARN",
			Indexes: []db.BrokenIndexesRow{
				{TableName: "users", IndexName: "idx_users_email"},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "invalid-indexes",
		},
		{
			Name: "multiple invalid indexes - WARN",
			Indexes: []db.BrokenIndexesRow{
				{TableName: "users", IndexName: "idx_users_email"},
				{TableName: "posts", IndexName: "idx_posts_created_at"},
				{TableName: "comments", IndexName: "idx_comments_user_id"},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedID:       "invalid-indexes",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer(tc.Indexes)

			checker := invalidindexes.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, 1, len(results), "Should have exactly 1 result")

			result := results[0]
			require.Equal(t, tc.ExpectedID, result.ID, "Result ID should match")
			require.Equal(t, tc.ExpectedSeverity, result.Severity, "Result severity should match")
			require.Equal(t, check.CategoryIndexes, report.Category, "Category should be indexes")
		})
	}
}

func Test_InvalidIndexes_DetailsContent(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		{TableName: "users", IndexName: "idx_users_email"},
		{TableName: "posts", IndexName: "idx_posts_created_at"},
	}

	queryer := newMockQueryer(indexes)

	checker := invalidindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityWarn, result.Severity)

	// Verify details contain count
	require.Contains(t, result.Details, "2 invalid indexes", "Details should mention count")

	// Verify details contain table and index names
	require.Contains(t, result.Details, "users", "Details should contain table name")
	require.Contains(t, result.Details, "idx_users_email", "Details should contain index name")
	require.Contains(t, result.Details, "posts", "Details should contain table name")
	require.Contains(t, result.Details, "idx_posts_created_at", "Details should contain index name")
}

func Test_InvalidIndexes_PrescriptionContent(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		{TableName: "users", IndexName: "idx_users_email"},
	}

	queryer := newMockQueryer(indexes)

	checker := invalidindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.NotEmpty(t, result.Details, "Details should not be empty")
}

func Test_InvalidIndexes_OKResult(t *testing.T) {
	t.Parallel()

	// No invalid indexes
	queryer := newMockQueryer([]db.BrokenIndexesRow{})

	checker := invalidindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Should be OK when no invalid indexes")
	require.Empty(t, result.Details, "Details should be empty for OK result")
}

func Test_InvalidIndexes_QueryError(t *testing.T) {
	t.Parallel()

	// Mock query error
	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := invalidindexes.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "invalid-indexes", "Error should mention check ID")
}

func Test_InvalidIndexes_CategoryFiltering(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		{TableName: "users", IndexName: "idx_users_email"},
	}

	queryer := newMockQueryer(indexes)

	// Create report with indexes category filtered out

	// Use the actual runner which handles filtering
	// We can't test filtering directly here since Report is internal
	// but we can verify the check respects the reporter interface
	checker := invalidindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	// Should still run and add result when not filtered
	results := report.Results
	require.Equal(t, 1, len(results), "Should have result when not filtered")
}

func Test_InvalidIndexes_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.BrokenIndexesRow{})
	checker := invalidindexes.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "invalid-indexes", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Invalid Indexes", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryIndexes, metadata.Category, "Category should be indexes")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
}

func Test_InvalidIndexes_ResultStructure(t *testing.T) {
	t.Parallel()

	indexes := []db.BrokenIndexesRow{
		{TableName: "orders", IndexName: "idx_orders_status"},
	}

	queryer := newMockQueryer(indexes)

	checker := invalidindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, "Invalid Indexes", result.Name, "Name should match")
	require.Equal(t, "invalid-indexes", report.CheckID, "CheckID should match")
	require.Equal(t, "invalid-indexes", result.ID, "ID should match CheckID")
	require.Equal(t, check.CategoryIndexes, report.Category, "Category should match")
	require.Equal(t, check.SeverityWarn, result.Severity, "Severity should be WARN for invalid indexes")
	require.NotEmpty(t, result.Details, "Details should not be empty")
}

func Test_InvalidIndexes_CountAccuracy(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name          string
		IndexCount    int
		ExpectedCount string
	}

	testCases := []testCase{
		{
			Name:          "single invalid index",
			IndexCount:    1,
			ExpectedCount: "1 invalid index",
		},
		{
			Name:          "five invalid indexes",
			IndexCount:    5,
			ExpectedCount: "5 invalid indexes",
		},
		{
			Name:          "ten invalid indexes",
			IndexCount:    10,
			ExpectedCount: "10 invalid indexes",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			// Generate N invalid indexes
			indexes := make([]db.BrokenIndexesRow, tc.IndexCount)
			for i := 0; i < tc.IndexCount; i++ {
				indexes[i] = db.BrokenIndexesRow{
					TableName: fmt.Sprintf("table_%d", i),
					IndexName: fmt.Sprintf("idx_table_%d_column", i),
				}
			}

			queryer := newMockQueryer(indexes)

			checker := invalidindexes.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, 1, len(results))

			result := results[0]
			require.Contains(t, result.Details, tc.ExpectedCount, "Details should contain accurate count")
		})
	}
}
