package tableseqscans_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tableseqscans"
	"github.com/emancu/pgdoctor/db"
)

const (
	highSeqScansID     = "high-seq-scans"
	moderateSeqScansID = "moderate-seq-scans"
)

type mockTableSeqScansQueryer struct {
	rows []db.HighSeqScanTablesRow
	err  error
}

func (m *mockTableSeqScansQueryer) HighSeqScanTables(context.Context) ([]db.HighSeqScanTablesRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func newMockQueryer(rows []db.HighSeqScanTablesRow) *mockTableSeqScansQueryer {
	return &mockTableSeqScansQueryer{rows: rows}
}

func newMockQueryerWithError(err error) *mockTableSeqScansQueryer {
	return &mockTableSeqScansQueryer{err: err}
}

func makeNumeric(value float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.2f", value))
	return n
}

func Test_TableSeqScans(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Rows             []db.HighSeqScanTablesRow
		ExpectedSeverity check.Severity
		ExpectedFindings int
	}

	testCases := []testCase{
		{
			Name:             "no high seq scan tables - OK",
			Rows:             []db.HighSeqScanTablesRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name: "moderate seq scans (>10k rows, >10 ratio) - WARN",
			Rows: []db.HighSeqScanTablesRow{
				{
					TableName:      pgtype.Text{String: "posts", Valid: true},
					SeqScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IdxScan:        pgtype.Int8{Int64: 200, Valid: true},
					SeqToIdxRatio:  makeNumeric(25.0),
					EstimatedRows:  pgtype.Int8{Int64: 15000, Valid: true},
					TableSizeBytes: pgtype.Int8{Int64: 15728640, Valid: true},
					IndexCount:     pgtype.Int8{Int64: 2, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 1,
		},
		{
			Name: "high seq scans (>50k rows, >50 ratio) - FAIL",
			Rows: []db.HighSeqScanTablesRow{
				{
					TableName:      pgtype.Text{String: "orders", Valid: true},
					SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
					IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
					SeqToIdxRatio:  makeNumeric(100.0),
					EstimatedRows:  pgtype.Int8{Int64: 75000, Valid: true},
					TableSizeBytes: pgtype.Int8{Int64: 78643200, Valid: true},
					IndexCount:     pgtype.Int8{Int64: 3, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityFail,
			ExpectedFindings: 1,
		},
		{
			Name: "table without indexes - skipped",
			Rows: []db.HighSeqScanTablesRow{
				{
					TableName:      pgtype.Text{String: "logs", Valid: true},
					SeqScan:        pgtype.Int8{Int64: 50000, Valid: true},
					IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
					SeqToIdxRatio:  pgtype.Numeric{Valid: false},
					EstimatedRows:  pgtype.Int8{Int64: 100000, Valid: true},
					TableSizeBytes: pgtype.Int8{Int64: 104857600, Valid: true},
					IndexCount:     pgtype.Int8{Int64: 0, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name: "mixed moderate and high seq scans - FAIL",
			Rows: []db.HighSeqScanTablesRow{
				{
					TableName:      pgtype.Text{String: "orders", Valid: true},
					SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
					IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
					SeqToIdxRatio:  makeNumeric(100.0),
					EstimatedRows:  pgtype.Int8{Int64: 75000, Valid: true},
					TableSizeBytes: pgtype.Int8{Int64: 78643200, Valid: true},
					IndexCount:     pgtype.Int8{Int64: 3, Valid: true},
				},
				{
					TableName:      pgtype.Text{String: "posts", Valid: true},
					SeqScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IdxScan:        pgtype.Int8{Int64: 200, Valid: true},
					SeqToIdxRatio:  makeNumeric(25.0),
					EstimatedRows:  pgtype.Int8{Int64: 15000, Valid: true},
					TableSizeBytes: pgtype.Int8{Int64: 15728640, Valid: true},
					IndexCount:     pgtype.Int8{Int64: 2, Valid: true},
				},
			},
			ExpectedSeverity: check.SeverityFail,
			ExpectedFindings: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			queryer := newMockQueryer(tc.Rows)

			checker := tableseqscans.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, tc.ExpectedFindings, len(results), "Should have expected number of findings")
			require.Equal(t, tc.ExpectedSeverity, report.Severity, "Report severity should match")
			require.Equal(t, check.CategoryPerformance, report.Category, "Category should be performance")
		})
	}
}

func Test_TableSeqScans_HighSeqScans(t *testing.T) {
	t.Parallel()

	rows := []db.HighSeqScanTablesRow{
		{
			TableName:      pgtype.Text{String: "orders", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
			SeqToIdxRatio:  makeNumeric(100.0),
			EstimatedRows:  pgtype.Int8{Int64: 75000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 78643200, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 3, Valid: true},
		},
		{
			TableName:      pgtype.Text{String: "invoices", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 8000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 80, Valid: true},
			SeqToIdxRatio:  makeNumeric(100.0),
			EstimatedRows:  pgtype.Int8{Int64: 60000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 62914560, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 2, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var highSeqResult *check.Finding
	for _, result := range report.Results {
		if result.ID == highSeqScansID {
			highSeqResult = &result
			break
		}
	}

	require.NotNil(t, highSeqResult, "Should have high-seq-scans finding")
	require.Equal(t, check.SeverityFail, highSeqResult.Severity)
	require.Contains(t, highSeqResult.Details, "2 tables")
	require.Contains(t, highSeqResult.Details, "orders")
	require.Contains(t, highSeqResult.Details, "seq: 10000")
	require.Contains(t, highSeqResult.Details, "idx: 100")
	require.Contains(t, highSeqResult.Details, "ratio: 100.0")
}

func Test_TableSeqScans_ModerateSeqScans(t *testing.T) {
	t.Parallel()

	rows := []db.HighSeqScanTablesRow{
		{
			TableName:      pgtype.Text{String: "comments", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 3000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 150, Valid: true},
			SeqToIdxRatio:  makeNumeric(20.0),
			EstimatedRows:  pgtype.Int8{Int64: 12000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 12582912, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 2, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var moderateSeqResult *check.Finding
	for _, result := range report.Results {
		if result.ID == moderateSeqScansID {
			moderateSeqResult = &result
			break
		}
	}

	require.NotNil(t, moderateSeqResult, "Should have moderate-seq-scans finding")
	require.Equal(t, check.SeverityWarn, moderateSeqResult.Severity)
	require.Contains(t, moderateSeqResult.Details, "comments")
}

func Test_TableSeqScans_ThresholdBoundaries(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name            string
		EstimatedRows   int64
		SeqToIdxRatio   float64
		ExpectedFinding string
	}

	testCases := []testCase{
		{
			Name:            "exactly 50k rows, 50 ratio - high",
			EstimatedRows:   50000,
			SeqToIdxRatio:   50.0,
			ExpectedFinding: "high-seq-scans",
		},
		{
			Name:            "just below 50k rows, high ratio - moderate",
			EstimatedRows:   49999,
			SeqToIdxRatio:   100.0,
			ExpectedFinding: "moderate-seq-scans",
		},
		{
			Name:            "high rows, just below 50 ratio - moderate",
			EstimatedRows:   75000,
			SeqToIdxRatio:   49.9,
			ExpectedFinding: "moderate-seq-scans",
		},
		{
			Name:            "exactly 10k rows, 10 ratio - moderate",
			EstimatedRows:   10000,
			SeqToIdxRatio:   10.0,
			ExpectedFinding: "moderate-seq-scans",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			rows := []db.HighSeqScanTablesRow{
				{
					TableName:      pgtype.Text{String: "test_table", Valid: true},
					SeqScan:        pgtype.Int8{Int64: 5000, Valid: true},
					IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
					SeqToIdxRatio:  makeNumeric(tc.SeqToIdxRatio),
					EstimatedRows:  pgtype.Int8{Int64: tc.EstimatedRows, Valid: true},
					TableSizeBytes: pgtype.Int8{Int64: 10485760, Valid: true},
					IndexCount:     pgtype.Int8{Int64: 2, Valid: true},
				},
			}

			queryer := newMockQueryer(rows)

			checker := tableseqscans.New(queryer)
			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			// Check that the expected finding exists and is not OK severity
			var foundExpected bool
			for _, result := range report.Results {
				if result.ID == tc.ExpectedFinding {
					if result.Severity != check.SeverityOK {
						foundExpected = true
					}
					break
				}
			}

			require.True(t, foundExpected, "Should have expected finding type with non-OK severity")
		})
	}
}

func Test_TableSeqScans_NoIndexCount(t *testing.T) {
	t.Parallel()

	rows := []db.HighSeqScanTablesRow{
		{
			TableName:      pgtype.Text{String: "logs", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 50000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			SeqToIdxRatio:  pgtype.Numeric{Valid: false},
			EstimatedRows:  pgtype.Int8{Int64: 100000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 104857600, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 0, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results))

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Tables without indexes should not be flagged")
}

func Test_TableSeqScans_InvalidRatio(t *testing.T) {
	t.Parallel()

	rows := []db.HighSeqScanTablesRow{
		{
			TableName:      pgtype.Text{String: "new_table", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 0, Valid: true},
			SeqToIdxRatio:  pgtype.Numeric{Valid: false},
			EstimatedRows:  pgtype.Int8{Int64: 75000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 78643200, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 2, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var highSeqResult *check.Finding
	for _, result := range report.Results {
		if result.ID == highSeqScansID {
			highSeqResult = &result
			break
		}
	}

	require.NotNil(t, highSeqResult)
	require.Equal(t, check.SeverityFail, highSeqResult.Severity, "Invalid ratio (no idx scans) should be treated as very high")
}

func Test_TableSeqScans_SizeFormatting(t *testing.T) {
	t.Parallel()

	rows := []db.HighSeqScanTablesRow{
		{
			TableName:      pgtype.Text{String: "large_table", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
			SeqToIdxRatio:  makeNumeric(100.0),
			EstimatedRows:  pgtype.Int8{Int64: 100000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 104857600, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 3, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var highSeqResult *check.Finding
	for _, result := range report.Results {
		if result.ID == highSeqScansID {
			highSeqResult = &result
			break
		}
	}

	require.NotNil(t, highSeqResult)
	require.Contains(t, highSeqResult.Details, "100.0 MB", "Should format size as MB")
}

func Test_TableSeqScans_TruncationMessage(t *testing.T) {
	t.Parallel()

	rows := make([]db.HighSeqScanTablesRow, 15)
	for i := range 15 {
		rows[i] = db.HighSeqScanTablesRow{
			TableName:      pgtype.Text{String: fmt.Sprintf("table_%d", i), Valid: true},
			SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
			SeqToIdxRatio:  makeNumeric(100.0),
			EstimatedRows:  pgtype.Int8{Int64: 75000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 78643200, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 3, Valid: true},
		}
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	var highSeqResult *check.Finding
	for _, result := range report.Results {
		if result.ID == highSeqScansID {
			highSeqResult = &result
			break
		}
	}

	require.NotNil(t, highSeqResult)
	require.Contains(t, highSeqResult.Details, "... and 5 more", "Should show truncation message")
}

func Test_TableSeqScans_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := newMockQueryerWithError(expectedErr)

	checker := tableseqscans.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err, "Should return error when query fails")
	require.Contains(t, err.Error(), "table-seq-scans", "Error should mention check ID")
}

func Test_TableSeqScans_Metadata(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.HighSeqScanTablesRow{})
	checker := tableseqscans.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "table-seq-scans", metadata.CheckID, "CheckID should match")
	require.Equal(t, "Table Sequential Scans", metadata.Name, "Name should match")
	require.Equal(t, check.CategoryPerformance, metadata.Category, "Category should be performance")
	require.NotEmpty(t, metadata.Description, "Description should not be empty")
	require.NotEmpty(t, metadata.SQL, "SQL should not be empty")
	require.NotEmpty(t, metadata.Readme, "Readme should not be empty")
}

func Test_TableSeqScans_OKResult(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryer([]db.HighSeqScanTablesRow{})

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have exactly 1 result")

	result := results[0]
	require.Equal(t, check.SeverityOK, result.Severity, "Should be OK when no high seq scan tables")
	require.Equal(t, "table-seq-scans", result.ID, "ID should be table-seq-scans when no rows")
	require.Empty(t, result.Details, "Details should be empty for OK result")
}

func Test_TableSeqScans_NoOKResultWhenIssuesFound(t *testing.T) {
	t.Parallel()

	rows := []db.HighSeqScanTablesRow{
		{
			TableName:      pgtype.Text{String: "orders", Valid: true},
			SeqScan:        pgtype.Int8{Int64: 10000, Valid: true},
			IdxScan:        pgtype.Int8{Int64: 100, Valid: true},
			SeqToIdxRatio:  makeNumeric(100.0),
			EstimatedRows:  pgtype.Int8{Int64: 75000, Valid: true},
			TableSizeBytes: pgtype.Int8{Int64: 78643200, Valid: true},
			IndexCount:     pgtype.Int8{Int64: 3, Valid: true},
		},
	}

	queryer := newMockQueryer(rows)

	checker := tableseqscans.New(queryer)
	report, err := checker.Check(context.Background())
	require.NoError(t, err)

	results := report.Results
	require.Equal(t, 1, len(results), "Should have 1 result when issues found")

	result := results[0]
	require.Equal(t, "high-seq-scans", result.ID)
	require.Equal(t, check.SeverityFail, result.Severity)
}
