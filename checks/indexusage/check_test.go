package indexusage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/indexusage"
	"github.com/emancu/pgdoctor/db"
)

const (
	indexCacheRatioID = "index-cache-ratio"
)

type mockIndexUsageQueryer struct {
	rows []db.IndexUsageStatsRow
	err  error
}

func (m *mockIndexUsageQueryer) IndexUsageStats(context.Context) ([]db.IndexUsageStatsRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func newMockQueryer(rows []db.IndexUsageStatsRow) *mockIndexUsageQueryer {
	return &mockIndexUsageQueryer{rows: rows}
}

func newMockQueryerWithError(err error) *mockIndexUsageQueryer {
	return &mockIndexUsageQueryer{err: err}
}

func makeNumeric(value float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.2f", value))
	return n
}

func Test_IndexUsage(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Rows             []db.IndexUsageStatsRow
		ExpectedSeverity check.Severity
		ExpectedFindings int
	}

	testCases := []testCase{
		{
			Name:             "no issues - OK",
			Rows:             []db.IndexUsageStatsRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name: "unused index (0 scans, >10MB) - FAIL",
			Rows: []db.IndexUsageStatsRow{
				{
					TableName:      pgtype.Text{String: "users", Valid: true},
					IndexName:      pgtype.Text{String: "idx_users_unused", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 20971520, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
					CacheHitRatio:  makeNumeric(98.0),
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
		{
			Name: "low usage index (<1000 scans, >10k writes) - WARN",
			Rows: []db.IndexUsageStatsRow{
				{
					TableName:      pgtype.Text{String: "posts", Valid: true},
					IndexName:      pgtype.Text{String: "idx_posts_status", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 500, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: 20000, Valid: true},
					CacheHitRatio:  makeNumeric(95.0),
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
		{
			Name: "low cache ratio large index (>100MB) - FAIL",
			Rows: []db.IndexUsageStatsRow{
				{
					TableName:      pgtype.Text{String: "orders", Valid: true},
					IndexName:      pgtype.Text{String: "idx_orders_created", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 157286400, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
					CacheHitRatio:  makeNumeric(85.0),
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
		{
			Name: "mixed issues - FAIL",
			Rows: []db.IndexUsageStatsRow{
				{
					TableName:      pgtype.Text{String: "users", Valid: true},
					IndexName:      pgtype.Text{String: "idx_users_unused", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 20971520, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
					CacheHitRatio:  makeNumeric(98.0),
				},
				{
					TableName:      pgtype.Text{String: "posts", Valid: true},
					IndexName:      pgtype.Text{String: "idx_posts_status", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 500, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: 20000, Valid: true},
					CacheHitRatio:  makeNumeric(92.0),
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer(tc.Rows)

			checker := indexusage.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, tc.ExpectedFindings, len(results), "Should have expected number of findings")
			require.Equal(t, tc.ExpectedSeverity, report.Severity, "Report severity should match")
			require.Equal(t, check.CategoryIndexes, report.Category, "Category should be indexes")
		})
	}
}

func Test_IndexUsage_UnusedIndexes(t *testing.T) {
	t.Parallel()

	rows := []db.IndexUsageStatsRow{
		{
			TableName:      pgtype.Text{String: "users", Valid: true},
			IndexName:      pgtype.Text{String: "idx_users_unused_1", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 20971520, Valid: true},
			IsPrimary:      false,
			IsUnique:       false,
			TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
			CacheHitRatio:  makeNumeric(98.0),
		},
		{
			TableName:      pgtype.Text{String: "posts", Valid: true},
			IndexName:      pgtype.Text{String: "idx_posts_unused", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 31457280, Valid: true},
			IsPrimary:      false,
			IsUnique:       false,
			TableWrites:    pgtype.Int8{Int64: 30000, Valid: true},
			CacheHitRatio:  makeNumeric(97.0),
		},
	}

	queryer := newMockQueryer(rows)

	checker := indexusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var unusedResult *check.Finding
	for _, result := range report.Results {
		if result.ID == "unused-indexes" {
			unusedResult = &result
			break
		}
	}

	require.NotNil(t, unusedResult, "Should have unused-indexes finding")
	require.Equal(t, check.SeverityWarn, unusedResult.Severity)
	require.Contains(t, unusedResult.Details, "2 unused indexes")
	require.Contains(t, unusedResult.Details, "idx_users_unused_1")
}

func Test_IndexUsage_LowUsageIndexes(t *testing.T) {
	t.Parallel()

	rows := []db.IndexUsageStatsRow{
		{
			TableName:      pgtype.Text{String: "comments", Valid: true},
			IndexName:      pgtype.Text{String: "idx_comments_status", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 500, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
			IsPrimary:      false,
			IsUnique:       false,
			TableWrites:    pgtype.Int8{Int64: 20000, Valid: true},
			CacheHitRatio:  makeNumeric(96.0),
		},
	}

	queryer := newMockQueryer(rows)

	checker := indexusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var lowUsageResult *check.Finding
	for _, result := range report.Results {
		if result.ID == "low-usage-indexes" {
			lowUsageResult = &result
			break
		}
	}

	require.NotNil(t, lowUsageResult, "Should have low-usage-indexes finding")
	require.Equal(t, check.SeverityWarn, lowUsageResult.Severity)
	require.Contains(t, lowUsageResult.Details, "low read usage but high write cost")
	require.Contains(t, lowUsageResult.Details, "scans: 500")
	require.Contains(t, lowUsageResult.Details, "writes: 20000")
}

func Test_IndexUsage_LowCacheRatio(t *testing.T) {
	t.Parallel()

	rows := []db.IndexUsageStatsRow{
		{
			TableName:      pgtype.Text{String: "orders", Valid: true},
			IndexName:      pgtype.Text{String: "idx_orders_created", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 157286400, Valid: true},
			IsPrimary:      false,
			IsUnique:       false,
			TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
			CacheHitRatio:  makeNumeric(85.0),
		},
	}

	queryer := newMockQueryer(rows)

	checker := indexusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var cacheResult *check.Finding
	for _, result := range report.Results {
		if result.ID == indexCacheRatioID {
			cacheResult = &result
			break
		}
	}

	require.NotNil(t, cacheResult, "Should have index-cache-ratio finding")
	require.Equal(t, check.SeverityWarn, cacheResult.Severity)
	require.Contains(t, cacheResult.Details, "85.0%")
}

func Test_IndexUsage_SkipPrimaryAndUnique(t *testing.T) {
	t.Parallel()

	rows := []db.IndexUsageStatsRow{
		{
			TableName:      pgtype.Text{String: "users", Valid: true},
			IndexName:      pgtype.Text{String: "users_pkey", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 20971520, Valid: true},
			IsPrimary:      true,
			IsUnique:       true,
			TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
			CacheHitRatio:  makeNumeric(98.0),
		},
		{
			TableName:      pgtype.Text{String: "users", Valid: true},
			IndexName:      pgtype.Text{String: "idx_users_email_unique", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 15728640, Valid: true},
			IsPrimary:      false,
			IsUnique:       true,
			TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
			CacheHitRatio:  makeNumeric(97.0),
		},
	}

	queryer := newMockQueryer(rows)

	checker := indexusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	for _, result := range report.Results {
		require.Equal(t, check.SeverityOK, result.Severity, "Primary/unique indexes should not be flagged")
	}
}

func Test_IndexUsage_SizeThresholds(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		IdxScan          int64
		IndexSizeBytes   int64
		TableWrites      int64
		ShouldBeUnused   bool
		ShouldBeLowUsage bool
	}

	testCases := []testCase{
		{
			Name:           "unused but small (<10MB) - not flagged",
			IdxScan:        0,
			IndexSizeBytes: 5242880,
			TableWrites:    50000,
			ShouldBeUnused: false,
		},
		{
			Name:           "unused and large (>10MB) - flagged",
			IdxScan:        0,
			IndexSizeBytes: 20971520,
			TableWrites:    50000,
			ShouldBeUnused: true,
		},
		{
			Name:             "low scans (<1000) but low writes - not flagged",
			IdxScan:          500,
			IndexSizeBytes:   10485760,
			TableWrites:      5000,
			ShouldBeLowUsage: false,
		},
		{
			Name:             "low scans (<1000) and high writes (>10k) - flagged",
			IdxScan:          500,
			IndexSizeBytes:   10485760,
			TableWrites:      20000,
			ShouldBeLowUsage: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			rows := []db.IndexUsageStatsRow{
				{
					TableName:      pgtype.Text{String: "test_table", Valid: true},
					IndexName:      pgtype.Text{String: "idx_test", Valid: true},
					IdxScan:        pgtype.Int8{Int64: tc.IdxScan, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: tc.IndexSizeBytes, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: tc.TableWrites, Valid: true},
					CacheHitRatio:  makeNumeric(98.0),
				},
			}

			queryer := newMockQueryer(rows)

			checker := indexusage.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			for _, result := range report.Results {
				if result.ID == "unused-indexes" {
					if tc.ShouldBeUnused {
						require.Equal(t, check.SeverityWarn, result.Severity)
					} else {
						require.Equal(t, check.SeverityOK, result.Severity)
					}
				}
				if result.ID == "low-usage-indexes" {
					if tc.ShouldBeLowUsage {
						require.Equal(t, check.SeverityWarn, result.Severity)
					} else {
						require.Equal(t, check.SeverityOK, result.Severity)
					}
				}
			}
		})
	}
}

func Test_IndexUsage_CacheRatioThresholds(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		CacheRatio       float64
		IndexSizeBytes   int64
		ExpectedSeverity check.Severity
	}

	testCases := []testCase{
		{
			Name:             "good cache ratio (>95%) - OK",
			CacheRatio:       98.0,
			IndexSizeBytes:   157286400,
			ExpectedSeverity: check.SeverityOK,
		},
		{
			Name:             "low cache ratio (<95%), small index - WARN",
			CacheRatio:       92.0,
			IndexSizeBytes:   20971520,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "low cache ratio (<90%), large index - FAIL",
			CacheRatio:       85.0,
			IndexSizeBytes:   157286400,
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "very small index ignored",
			CacheRatio:       80.0,
			IndexSizeBytes:   5242880,
			ExpectedSeverity: check.SeverityOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			rows := []db.IndexUsageStatsRow{
				{
					TableName:      pgtype.Text{String: "test_table", Valid: true},
					IndexName:      pgtype.Text{String: "idx_test", Valid: true},
					IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IndexSizeBytes: pgtype.Int8{Int64: tc.IndexSizeBytes, Valid: true},
					IsPrimary:      false,
					IsUnique:       false,
					TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
					CacheHitRatio:  makeNumeric(tc.CacheRatio),
				},
			}

			queryer := newMockQueryer(rows)

			checker := indexusage.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			var cacheResult *check.Finding
			for _, result := range report.Results {
				if result.ID == indexCacheRatioID {
					cacheResult = &result
					break
				}
			}

			require.NotNil(t, cacheResult)
			require.Equal(t, tc.ExpectedSeverity, cacheResult.Severity)
		})
	}
}

func Test_IndexUsage_NoCacheData(t *testing.T) {
	t.Parallel()

	rows := []db.IndexUsageStatsRow{
		{
			TableName:      pgtype.Text{String: "new_table", Valid: true},
			IndexName:      pgtype.Text{String: "idx_new", Valid: true},
			IdxScan:        pgtype.Int8{Int64: 5000, Valid: true},
			IndexSizeBytes: pgtype.Int8{Int64: 20971520, Valid: true},
			IsPrimary:      false,
			IsUnique:       false,
			TableWrites:    pgtype.Int8{Int64: 50000, Valid: true},
			CacheHitRatio:  pgtype.Numeric{Valid: false},
		},
	}

	queryer := newMockQueryer(rows)

	checker := indexusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var cacheResult *check.Finding
	for _, result := range report.Results {
		if result.ID == indexCacheRatioID {
			cacheResult = &result
			break
		}
	}

	require.NotNil(t, cacheResult)
	require.Equal(t, check.SeverityOK, cacheResult.Severity, "Should be OK when no cache data")
}

func Test_IndexUsage_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := indexusage.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "index-usage", "Error should mention check ID")
}

func Test_IndexUsage_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.IndexUsageStatsRow{})
	checker := indexusage.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "index-usage", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Index Usage", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryIndexes, metadata.Category, "Category should be indexes")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
	require.NotEmpty(t, metadata.SQL, "SQL should not be empty")
	require.NotEmpty(t, metadata.Readme, "Readme should not be empty")
}

func Test_IndexUsage_OKResult(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.IndexUsageStatsRow{})

	checker := indexusage.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have 1 result when no indexes")

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Result should be OK")
	require.Equal(t, "index-usage", result.ID, "ID should be index-usage")
	require.Empty(t, result.Details, "Details should be empty for OK result")
}
