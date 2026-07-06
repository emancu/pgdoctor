package indexusage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/indexusage"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

const (
	unusedIndexesID   = "unused-indexes"
	lowUsageIndexesID = "low-usage-indexes"
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

// indexRow builds a well-formed row so tests only set the fields they exercise.
// table is schema-qualified (schema.table), mirroring the SQL projection.
type indexRow struct {
	table       string
	index       string
	scans       int64
	sizeBytes   int64
	tableWrites int64
	isPrimary   bool
	isUnique    bool
}

func (r indexRow) build() db.IndexUsageStatsRow {
	return db.IndexUsageStatsRow{
		TableName:      pgtype.Text{String: r.table, Valid: true},
		IndexName:      pgtype.Text{String: r.index, Valid: true},
		IdxScan:        pgtype.Int8{Int64: r.scans, Valid: true},
		IndexSizeBytes: pgtype.Int8{Int64: r.sizeBytes, Valid: true},
		TableWrites:    pgtype.Int8{Int64: r.tableWrites, Valid: true},
		IsPrimary:      r.isPrimary,
		IsUnique:       r.isUnique,
	}
}

func rows(rr ...indexRow) []db.IndexUsageStatsRow {
	out := make([]db.IndexUsageStatsRow, 0, len(rr))
	for _, r := range rr {
		out = append(out, r.build())
	}
	return out
}

func findingByID(report *check.Report, id string) *check.Finding {
	for i := range report.Results {
		if report.Results[i].ID == id {
			return &report.Results[i]
		}
	}
	return nil
}

func runCheck(t *testing.T, data []db.IndexUsageStatsRow) *check.Report {
	t.Helper()
	report, err := indexusage.New(newMockQueryer(data)).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

// Canonical rows reused across cases.
var (
	unusedRow = indexRow{
		table: "public.users", index: "idx_users_unused",
		scans: 0, sizeBytes: 20 * 1024 * 1024, tableWrites: 50000,
	}
	lowUsageRow = indexRow{
		table: "public.posts", index: "idx_posts_status",
		scans: 500, sizeBytes: 10*1024*1024 + 1, tableWrites: 20000,
	}
)

func Test_IndexUsage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Rows             []db.IndexUsageStatsRow
		ExpectedSeverity check.Severity
		ExpectedFindings int
	}{
		{
			Name:             "no rows - single OK finding",
			Rows:             []db.IndexUsageStatsRow{},
			ExpectedSeverity: check.SeverityOK,
			ExpectedFindings: 1,
		},
		{
			Name:             "unused index - WARN",
			Rows:             rows(unusedRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
		{
			Name:             "low usage index - WARN",
			Rows:             rows(lowUsageRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
		{
			Name:             "mixed issues - WARN",
			Rows:             rows(unusedRow, lowUsageRow),
			ExpectedSeverity: check.SeverityWarn,
			ExpectedFindings: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			report := runCheck(t, tc.Rows)

			require.Len(t, report.Results, tc.ExpectedFindings)
			require.Equal(t, tc.ExpectedSeverity, report.Severity)
			require.Equal(t, check.CategoryIndexes, report.Category)
		})
	}
}

func Test_IndexUsage_UnusedIndexesTable(t *testing.T) {
	t.Parallel()

	// Supplied smaller-first to prove rows are sorted by size DESC.
	report := runCheck(t, rows(
		unusedRow, // public.users, 20 MiB
		indexRow{
			table: "public.posts", index: "idx_posts_unused",
			scans: 0, sizeBytes: 30 * 1024 * 1024, tableWrites: 30000,
		},
	))

	f := findingByID(report, unusedIndexesID)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityWarn, f.Severity)
	require.Contains(t, f.Details, "2 unused indexes")
	require.Contains(t, f.Details, "verify usage on EVERY replica")

	require.NotNil(t, f.Table)
	require.Equal(t, []string{"Table", "Index", "Size"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 2)

	// Sorted size DESC: 30 MiB posts index before 20 MiB users index.
	require.Equal(t, []string{"public.posts", "idx_posts_unused", "30.0MiB"}, f.Table.Rows[0].Cells)
	require.Equal(t, check.SeverityWarn, f.Table.Rows[0].Severity)
	require.Equal(t, []string{"public.users", "idx_users_unused", "20.0MiB"}, f.Table.Rows[1].Cells)
}

func Test_IndexUsage_LowUsageIndexesTable(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(indexRow{
		table: "public.comments", index: "idx_comments_status",
		scans: 500, sizeBytes: 10*1024*1024 + 1, tableWrites: 20000,
	}))

	f := findingByID(report, lowUsageIndexesID)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityWarn, f.Severity)
	require.Contains(t, f.Details, "low read usage but high write cost")
	require.Contains(t, f.Details, "verify usage on EVERY replica")

	require.NotNil(t, f.Table)
	require.Equal(t, []string{"Table", "Index", "Size", "Scans", "Table Writes"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 1)

	row := f.Table.Rows[0]
	require.Equal(t, check.SeverityWarn, row.Severity)
	require.Equal(t, []string{"public.comments", "idx_comments_status", "10.0MiB", "500", "20.0K"}, row.Cells)
}

func Test_IndexUsage_SkipPrimaryAndUnique(t *testing.T) {
	t.Parallel()

	report := runCheck(t, rows(
		indexRow{
			table: "public.users", index: "users_pkey",
			scans: 0, sizeBytes: 20 * 1024 * 1024, tableWrites: 50000,
			isPrimary: true, isUnique: true,
		},
		indexRow{
			table: "public.users", index: "idx_users_email_unique",
			scans: 0, sizeBytes: 15 * 1024 * 1024, tableWrites: 50000,
			isUnique: true,
		},
	))

	require.Equal(t, check.SeverityOK, report.Severity)
	for _, result := range report.Results {
		require.Equal(t, check.SeverityOK, result.Severity, "Primary/unique indexes should not be flagged")
	}
}

func Test_IndexUsage_SizeThresholds(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		Name             string
		Scans            int64
		SizeBytes        int64
		TableWrites      int64
		ShouldBeUnused   bool
		ShouldBeLowUsage bool
	}{
		{
			Name:           "unused but small (<10MB) - not flagged",
			Scans:          0,
			SizeBytes:      5 * 1024 * 1024,
			TableWrites:    50000,
			ShouldBeUnused: false,
		},
		{
			Name:           "unused and large (>10MB) - flagged",
			Scans:          0,
			SizeBytes:      20 * 1024 * 1024,
			TableWrites:    50000,
			ShouldBeUnused: true,
		},
		{
			Name:             "low scans (<1000) but low writes - not flagged",
			Scans:            500,
			SizeBytes:        10 * 1024 * 1024,
			TableWrites:      5000,
			ShouldBeLowUsage: false,
		},
		{
			Name:             "low scans (<1000) and high writes (>10k) - flagged",
			Scans:            500,
			SizeBytes:        10 * 1024 * 1024,
			TableWrites:      20000,
			ShouldBeLowUsage: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			report := runCheck(t, rows(indexRow{
				table: "public.test_table", index: "idx_test",
				scans: tc.Scans, sizeBytes: tc.SizeBytes, tableWrites: tc.TableWrites,
			}))

			unused := findingByID(report, unusedIndexesID)
			require.NotNil(t, unused)
			if tc.ShouldBeUnused {
				require.Equal(t, check.SeverityWarn, unused.Severity)
			} else {
				require.Equal(t, check.SeverityOK, unused.Severity)
			}

			lowUsage := findingByID(report, lowUsageIndexesID)
			require.NotNil(t, lowUsage)
			if tc.ShouldBeLowUsage {
				require.Equal(t, check.SeverityWarn, lowUsage.Severity)
			} else {
				require.Equal(t, check.SeverityOK, lowUsage.Severity)
			}
		})
	}
}

func Test_IndexUsage_QueryError(t *testing.T) {
	t.Parallel()

	queryer := newMockQueryerWithError(fmt.Errorf("database connection error"))

	_, err := indexusage.New(queryer).Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "index-usage", "Error should mention check ID")
}

func Test_IndexUsage_Metadata(t *testing.T) {
	t.Parallel()

	metadata := indexusage.New(newMockQueryer(nil)).Metadata()

	require.Equal(t, "index-usage", metadata.CheckID)
	require.Equal(t, "Index Usage", metadata.Name)
	require.Equal(t, check.CategoryIndexes, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
}

func Test_IndexUsage_OKResult(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.IndexUsageStatsRow{})

	require.Len(t, report.Results, 1)
	result := report.Results[0]
	require.Equal(t, check.SeverityOK, result.Severity)
	require.Equal(t, "index-usage", result.ID)
	require.Empty(t, result.Details)
	require.Nil(t, result.Table)
}
