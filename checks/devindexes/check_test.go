package devindexes_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/devindexes"
	"github.com/emancu/pgdoctor/db"
)

type mockDevIndexesQueryer struct {
	rows []db.DevIndexesRow
	err  error
}

func (m *mockDevIndexesQueryer) DevIndexes(context.Context) ([]db.DevIndexesRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func newMockQueryer(rows []db.DevIndexesRow) *mockDevIndexesQueryer {
	return &mockDevIndexesQueryer{rows: rows}
}

func newMockQueryerWithError(err error) *mockDevIndexesQueryer {
	return &mockDevIndexesQueryer{err: err}
}

func Test_DevIndexes(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Rows             []db.DevIndexesRow
		ExpectedSeverity check.Severity
		ExpectedFindings int
	}

	testCases := []testCase{
		{
			Name:             "no dev indexes - OK",
			Rows:             []db.DevIndexesRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name: "used dev indexes (>1000 scans) - WARN",
			Rows: []db.DevIndexesRow{
				{
					IndexName:      pgtype.Text{String: "_dev_users_email", Valid: true},
					TableName:      pgtype.Text{String: "users", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 1,
		},
		{
			Name: "unused dev indexes (<1000 scans) - WARN",
			Rows: []db.DevIndexesRow{
				{
					IndexName:      pgtype.Text{String: "_dev_posts_status", Valid: true},
					TableName:      pgtype.Text{String: "posts", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 50, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 5242880, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 1,
		},
		{
			Name: "mixed used and unused dev indexes - WARN",
			Rows: []db.DevIndexesRow{
				{
					IndexName:      pgtype.Text{String: "_dev_users_email", Valid: true},
					TableName:      pgtype.Text{String: "users", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
				},
				{
					IndexName:      pgtype.Text{String: "_dev_posts_status", Valid: true},
					TableName:      pgtype.Text{String: "posts", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 50, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 5242880, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer(tc.Rows)

			checker := devindexes.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, tc.ExpectedFindings, len(results), "Should have expected number of findings")
			require.Equal(t, tc.ExpectedSeverity, report.Severity, "Report severity should match")
			require.Equal(t, check.CategoryIndexes, report.Category, "Category should be indexes")
		})
	}
}

func Test_DevIndexes_UsedDevIndexes(t *testing.T) {
	t.Parallel()

	rows := []db.DevIndexesRow{
		{
			IndexName:      pgtype.Text{String: "_dev_users_email", Valid: true},
			TableName:      pgtype.Text{String: "users", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
		},
		{
			IndexName:      pgtype.Text{String: "_dev_orders_created_at", Valid: true},
			TableName:      pgtype.Text{String: "orders", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 12000, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 20971520, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := devindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, "used-dev-indexes", result.ID)
	require.Equal(t, check.SeverityWarn, result.Severity)
	require.Contains(t, result.Details, "2 development indexes")
	require.Contains(t, result.Details, "_dev_users_email")
	require.Contains(t, result.Details, "scans: 5000")
}

func Test_DevIndexes_UnusedDevIndexes(t *testing.T) {
	t.Parallel()

	rows := []db.DevIndexesRow{
		{
			IndexName:      pgtype.Text{String: "_dev_posts_status", Valid: true},
			TableName:      pgtype.Text{String: "posts", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 50, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 5242880, Valid: true},
		},
		{
			IndexName:      pgtype.Text{String: "_dev_comments_user_id", Valid: true},
			TableName:      pgtype.Text{String: "comments", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 2097152, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := devindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, "unused-dev-indexes", result.ID)
	require.Equal(t, check.SeverityWarn, result.Severity)
	require.Contains(t, result.Details, "2 development indexes")
	require.Contains(t, result.Details, "_dev_posts_status")
}

func Test_DevIndexes_ThresholdBoundary(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name            string
		IdxScan         int64
		ExpectedFinding string
	}

	testCases := []testCase{
		{
			Name:            "exactly 1000 scans - used",
			IdxScan:         1000,
			ExpectedFinding: "used-dev-indexes",
		},
		{
			Name:            "just below 1000 scans - unused",
			IdxScan:         999,
			ExpectedFinding: "unused-dev-indexes",
		},
		{
			Name:            "well above threshold - used",
			IdxScan:         5000,
			ExpectedFinding: "used-dev-indexes",
		},
		{
			Name:            "zero scans - unused",
			IdxScan:         0,
			ExpectedFinding: "unused-dev-indexes",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			rows := []db.DevIndexesRow{
				{
					IndexName:      pgtype.Text{String: "_dev_test_column", Valid: true},
					TableName:      pgtype.Text{String: "test", Valid: true},
					IdxScan:        pgtype.Int8{Int64: tc.IdxScan, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 5242880, Valid: true},
				},
			}

			queryer := newMockQueryer(rows)

			checker := devindexes.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, 1, len(results))

			result := results[0]
			require.Equal(t, tc.ExpectedFinding, result.ID)
		})
	}
}

func Test_DevIndexes_SizeFormatting(t *testing.T) {
	t.Parallel()

	rows := []db.DevIndexesRow{
		{
			IndexName:      pgtype.Text{String: "_dev_large_index", Valid: true},
			TableName:      pgtype.Text{String: "large_table", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 2000, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 104857600, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := devindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Contains(t, result.Details, "100.0 MB", "Should format size as MB")
}

func Test_DevIndexes_TruncationMessage(t *testing.T) {
	t.Parallel()

	rows := make([]db.DevIndexesRow, 15)
	for i := range 15 {
		rows[i] = db.DevIndexesRow{
			IndexName:      pgtype.Text{String: fmt.Sprintf("_dev_table_%d_column", i), Valid: true},
			TableName:      pgtype.Text{String: fmt.Sprintf("table_%d", i), Valid: true},
			IdxScan:        pgtype.Int8{Int64: 50, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 5242880, Valid: true},
		}
	}

	queryer := newMockQueryer(rows)

	checker := devindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Contains(t, result.Details, "... and 5 more", "Should show truncation message")
}

func Test_DevIndexes_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := devindexes.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "dev-indexes", "Error should mention check ID")
}

func Test_DevIndexes_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.DevIndexesRow{})
	checker := devindexes.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "dev-indexes", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Temporary Dev Indexes", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryIndexes, metadata.Category, "Category should be indexes")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
	require.NotEmpty(t, metadata.SQL, "SQL should not be empty")
	require.NotEmpty(t, metadata.Readme, "Readme should not be empty")
}

func Test_DevIndexes_OKResult(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.DevIndexesRow{})

	checker := devindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Should be OK when no dev indexes")
	require.Equal(t, "dev-indexes", result.ID)
	require.Empty(t, result.Details, "Details should be empty for OK result")
}
