package duplicateindexes_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/duplicateindexes"
	"github.com/emancu/pgdoctor/db"
)

const (
	exactDuplicatesID  = "exact-duplicates"
	prefixDuplicatesID = "prefix-duplicates"
)

type mockDuplicateIndexesQueryer struct {
	rows []db.DuplicateIndexesRow
	err  error
}

func (m *mockDuplicateIndexesQueryer) DuplicateIndexes(context.Context) ([]db.DuplicateIndexesRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func newMockQueryer(rows []db.DuplicateIndexesRow) *mockDuplicateIndexesQueryer {
	return &mockDuplicateIndexesQueryer{rows: rows}
}

func newMockQueryerWithError(err error) *mockDuplicateIndexesQueryer {
	return &mockDuplicateIndexesQueryer{err: err}
}

func Test_DuplicateIndexes(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Rows             []db.DuplicateIndexesRow
		ExpectedSeverity check.Severity
		ExpectedFindings int
	}

	testCases := []testCase{
		{
			Name:             "no duplicate indexes - OK",
			Rows:             []db.DuplicateIndexesRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name: "exact duplicates - FAIL",
			Rows: []db.DuplicateIndexesRow{
				{
					TableName:     pgtype.Text{String: "users", Valid: true},
					IndexNameA:    pgtype.Text{String: "idx_users_email", Valid: true},
					IndexNameB:    pgtype.Text{String: "idx_users_email_dup", Valid: true},
					SizeA:         pgtype.Int8{Int64: 10485760, Valid: true},
					SizeB:         pgtype.Int8{Int64: 10485760, Valid: true},
					DuplicateType: pgtype.Text{String: "exact", Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
		{
			Name: "prefix duplicates small - WARN",
			Rows: []db.DuplicateIndexesRow{
				{
					TableName:     pgtype.Text{String: "posts", Valid: true},
					IndexNameA:    pgtype.Text{String: "idx_posts_author", Valid: true},
					IndexNameB:    pgtype.Text{String: "idx_posts_author_created", Valid: true},
					SizeA:         pgtype.Int8{Int64: 5242880, Valid: true},
					SizeB:         pgtype.Int8{Int64: 10485760, Valid: true},
					DuplicateType: pgtype.Text{String: "prefix", Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
		{
			Name: "prefix duplicates large - FAIL",
			Rows: []db.DuplicateIndexesRow{
				{
					TableName:     pgtype.Text{String: "orders", Valid: true},
					IndexNameA:    pgtype.Text{String: "idx_orders_user", Valid: true},
					IndexNameB:    pgtype.Text{String: "idx_orders_user_created", Valid: true},
					SizeA:         pgtype.Int8{Int64: 157286400, Valid: true},
					SizeB:         pgtype.Int8{Int64: 209715200, Valid: true},
					DuplicateType: pgtype.Text{String: "prefix", Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
		{
			Name: "mixed exact and prefix - FAIL",
			Rows: []db.DuplicateIndexesRow{
				{
					TableName:     pgtype.Text{String: "users", Valid: true},
					IndexNameA:    pgtype.Text{String: "idx_users_email", Valid: true},
					IndexNameB:    pgtype.Text{String: "idx_users_email_dup", Valid: true},
					SizeA:         pgtype.Int8{Int64: 10485760, Valid: true},
					SizeB:         pgtype.Int8{Int64: 10485760, Valid: true},
					DuplicateType: pgtype.Text{String: "exact", Valid: true},
				},
				{
					TableName:     pgtype.Text{String: "posts", Valid: true},
					IndexNameA:    pgtype.Text{String: "idx_posts_author", Valid: true},
					IndexNameB:    pgtype.Text{String: "idx_posts_author_created", Valid: true},
					SizeA:         pgtype.Int8{Int64: 5242880, Valid: true},
					SizeB:         pgtype.Int8{Int64: 10485760, Valid: true},
					DuplicateType: pgtype.Text{String: "prefix", Valid: true},
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

			checker := duplicateindexes.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, tc.ExpectedFindings, len(results), "Should have expected number of findings")
			require.Equal(t, tc.ExpectedSeverity, report.Severity, "Report severity should match")
			require.Equal(t, check.CategoryIndexes, report.Category, "Category should be indexes")
		})
	}
}

func Test_DuplicateIndexes_ExactDuplicates(t *testing.T) {
	t.Parallel()

	rows := []db.DuplicateIndexesRow{
		{
			TableName:     pgtype.Text{String: "users", Valid: true},
			IndexNameA:    pgtype.Text{String: "idx_users_email", Valid: true},
			IndexNameB:    pgtype.Text{String: "idx_users_email_dup", Valid: true},
			SizeA:         pgtype.Int8{Int64: 10485760, Valid: true},
			SizeB:         pgtype.Int8{Int64: 10485760, Valid: true},
			DuplicateType: pgtype.Text{String: "exact", Valid: true},
		},
		{
			TableName:     pgtype.Text{String: "posts", Valid: true},
			IndexNameA:    pgtype.Text{String: "idx_posts_status", Valid: true},
			IndexNameB:    pgtype.Text{String: "idx_posts_status_v2", Valid: true},
			SizeA:         pgtype.Int8{Int64: 5242880, Valid: true},
			SizeB:         pgtype.Int8{Int64: 5242880, Valid: true},
			DuplicateType: pgtype.Text{String: "exact", Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := duplicateindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var exactDuplicateResult *check.Finding
	for _, result := range report.Results {
		if result.ID == exactDuplicatesID {
			exactDuplicateResult = &result
			break
		}
	}

	require.NotNil(t, exactDuplicateResult, "Should have exact-duplicates finding")
	require.Equal(t, check.SeverityWarn, exactDuplicateResult.Severity)
	require.Contains(t, exactDuplicateResult.Details, "2 exact duplicate")
	require.Contains(t, exactDuplicateResult.Details, "users")
	require.Contains(t, exactDuplicateResult.Details, "idx_users_email")
}

func Test_DuplicateIndexes_PrefixDuplicates(t *testing.T) {
	t.Parallel()

	rows := []db.DuplicateIndexesRow{
		{
			TableName:     pgtype.Text{String: "orders", Valid: true},
			IndexNameA:    pgtype.Text{String: "idx_orders_user", Valid: true},
			IndexNameB:    pgtype.Text{String: "idx_orders_user_created", Valid: true},
			SizeA:         pgtype.Int8{Int64: 20971520, Valid: true},
			SizeB:         pgtype.Int8{Int64: 41943040, Valid: true},
			DuplicateType: pgtype.Text{String: "prefix", Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := duplicateindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var prefixDuplicateResult *check.Finding
	for _, result := range report.Results {
		if result.ID == prefixDuplicatesID {
			prefixDuplicateResult = &result
			break
		}
	}

	require.NotNil(t, prefixDuplicateResult, "Should have prefix-duplicates finding")
	require.Contains(t, prefixDuplicateResult.Details, "prefix duplicate")
	require.Contains(t, prefixDuplicateResult.Details, "idx_orders_user")
	require.Contains(t, prefixDuplicateResult.Details, "idx_orders_user_created")
}

func Test_DuplicateIndexes_PrefixSizeThreshold(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		SizeA            int64
		ExpectedSeverity check.Severity
	}

	testCases := []testCase{
		{
			Name:             "small prefix duplicate (<100MB) - WARN",
			SizeA:            52428800,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "large prefix duplicate (>100MB) - FAIL",
			SizeA:            157286400,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "exactly 100MB threshold",
			SizeA:            104857600,
			ExpectedSeverity: check.SeverityWarn,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			rows := []db.DuplicateIndexesRow{
				{
					TableName:     pgtype.Text{String: "test_table", Valid: true},
					IndexNameA:    pgtype.Text{String: "idx_a", Valid: true},
					IndexNameB:    pgtype.Text{String: "idx_a_b", Valid: true},
					SizeA:         pgtype.Int8{Int64: tc.SizeA, Valid: true},
					SizeB:         pgtype.Int8{Int64: tc.SizeA * 2, Valid: true},
					DuplicateType: pgtype.Text{String: "prefix", Valid: true},
				},
			}

			queryer := newMockQueryer(rows)

			checker := duplicateindexes.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			var prefixResult *check.Finding
			for _, result := range report.Results {
				if result.ID == prefixDuplicatesID {
					prefixResult = &result
					break
				}
			}

			require.NotNil(t, prefixResult)
			require.Equal(t, tc.ExpectedSeverity, prefixResult.Severity)
		})
	}
}

func Test_DuplicateIndexes_SizeFormatting(t *testing.T) {
	t.Parallel()

	rows := []db.DuplicateIndexesRow{
		{
			TableName:     pgtype.Text{String: "large_table", Valid: true},
			IndexNameA:    pgtype.Text{String: "idx_large_a", Valid: true},
			IndexNameB:    pgtype.Text{String: "idx_large_b", Valid: true},
			SizeA:         pgtype.Int8{Int64: 104857600, Valid: true},
			SizeB:         pgtype.Int8{Int64: 104857600, Valid: true},
			DuplicateType: pgtype.Text{String: "exact", Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := duplicateindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var exactResult *check.Finding
	for _, result := range report.Results {
		if result.ID == exactDuplicatesID {
			exactResult = &result
			break
		}
	}

	require.NotNil(t, exactResult)
	require.Contains(t, exactResult.Details, "200.0 MB", "Should format total size as MB")
}

func Test_DuplicateIndexes_TruncationMessage(t *testing.T) {
	t.Parallel()

	rows := make([]db.DuplicateIndexesRow, 15)
	for i := range 15 {
		rows[i] = db.DuplicateIndexesRow{
			TableName:     pgtype.Text{String: fmt.Sprintf("table_%d", i), Valid: true},
			IndexNameA:    pgtype.Text{String: fmt.Sprintf("idx_%d_a", i), Valid: true},
			IndexNameB:    pgtype.Text{String: fmt.Sprintf("idx_%d_b", i), Valid: true},
			SizeA:         pgtype.Int8{Int64: 10485760, Valid: true},
			SizeB:         pgtype.Int8{Int64: 10485760, Valid: true},
			DuplicateType: pgtype.Text{String: "exact", Valid: true},
		}
	}

	queryer := newMockQueryer(rows)

	checker := duplicateindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var exactResult *check.Finding
	for _, result := range report.Results {
		if result.ID == exactDuplicatesID {
			exactResult = &result
			break
		}
	}

	require.NotNil(t, exactResult)
	require.Contains(t, exactResult.Details, "... and 5 more", "Should show truncation message")
}

func Test_DuplicateIndexes_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := duplicateindexes.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "duplicate-indexes", "Error should mention check ID")
}

func Test_DuplicateIndexes_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.DuplicateIndexesRow{})
	checker := duplicateindexes.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "duplicate-indexes", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Duplicate Indexes", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryIndexes, metadata.Category, "Category should be indexes")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
	require.NotEmpty(t, metadata.SQL, "SQL should not be empty")
	require.NotEmpty(t, metadata.Readme, "Readme should not be empty")
}

func Test_DuplicateIndexes_OKResult(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.DuplicateIndexesRow{})

	checker := duplicateindexes.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have 1 result when no duplicates")

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Result should be OK")
	require.Equal(t, "duplicate-indexes", result.ID, "ID should be duplicate-indexes")
	require.Empty(t, result.Details, "Details should be empty for OK result")
}
