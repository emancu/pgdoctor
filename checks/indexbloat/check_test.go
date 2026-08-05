package indexbloat_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/indexbloat"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockQueryer struct {
	rows []db.IndexBloatRow
	err  error
}

func (m *mockQueryer) IndexBloat(_ context.Context) ([]db.IndexBloatRow, error) {
	return m.rows, m.err
}

func makeIndexRow(schema, table, index string, bloatPct float64, bloatBytes, actualBytes int64) db.IndexBloatRow {
	var bloatNumeric pgtype.Numeric
	_ = bloatNumeric.Scan(fmt.Sprintf("%.2f", bloatPct))

	return db.IndexBloatRow{
		TableName:    pgtype.Text{String: schema + "." + table, Valid: true},
		Indexname:    pgtype.Text{String: index, Valid: true},
		BloatPercent: bloatNumeric,
		BloatBytes:   pgtype.Int8{Int64: bloatBytes, Valid: true},
		ActualBytes:  pgtype.Int8{Int64: actualBytes, Valid: true},
	}
}

func runCheck(t *testing.T, rows []db.IndexBloatRow) *check.Report {
	t.Helper()

	checker := indexbloat.New(&mockQueryer{rows: rows})
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	return report
}

func TestIndexBloat_EmptyResult(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.IndexBloatRow{})

	assert.Equal(t, check.SeverityPass, report.Severity)
	require.Len(t, report.Results, 1)
	assert.Equal(t, "index-bloat", report.Results[0].ID)
	assert.Equal(t, check.SeverityPass, report.Results[0].Severity)
	assert.Contains(t, report.Results[0].Details, "No bloated indexes")
}

func TestIndexBloat_BelowThresholdsNotListed(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.IndexBloatRow{
		makeIndexRow("public", "users", "users_pkey", 10.0, 10*check.MiB, 100*check.MiB),
		makeIndexRow("public", "orders", "orders_pkey", 49.9, 50*check.MiB, 100*check.MiB),
		// 3GiB index at 20% bloat wastes ~614MiB: below both arms.
		makeIndexRow("public", "events", "events_created_at_idx", 20.0, 3*check.GiB/5, 3*check.GiB),
		makeIndexRow("public", "logs", "logs_ts_idx", 30.0, 2*check.GiB-1, 7*check.GiB),
	})

	assert.Equal(t, check.SeverityPass, report.Severity)
	require.Len(t, report.Results, 1)
	assert.Equal(t, "index-bloat", report.Results[0].ID)
	assert.Equal(t, check.SeverityPass, report.Results[0].Severity)
}

func TestIndexBloat_RowTiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		row         db.IndexBloatRow
		rowSeverity check.Severity
	}{
		{
			name:        "exactly 50 pct with small waste is INFO",
			row:         makeIndexRow("public", "t1", "idx1", 50.0, 50*check.MiB, 300*check.MiB),
			rowSeverity: check.SeverityInfo,
		},
		{
			name:        "69.9 pct with small waste is INFO",
			row:         makeIndexRow("public", "t2", "idx2", 69.9, 70*check.MiB, 300*check.MiB),
			rowSeverity: check.SeverityInfo,
		},
		{
			name:        "exactly 70 pct with small waste is WARN",
			row:         makeIndexRow("public", "t3", "idx3", 70.0, 70*check.MiB, 300*check.MiB),
			rowSeverity: check.SeverityWarn,
		},
		{
			name:        "exactly 2GiB wasted with low pct is WARN",
			row:         makeIndexRow("public", "t4", "idx4", 30.0, 2*check.GiB, 7*check.GiB),
			rowSeverity: check.SeverityWarn,
		},
		{
			name:        "3GiB wasted with low pct is WARN",
			row:         makeIndexRow("public", "t5", "idx5", 25.0, 3*check.GiB, 12*check.GiB),
			rowSeverity: check.SeverityWarn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := runCheck(t, []db.IndexBloatRow{tt.row})

			assert.Equal(t, check.SeverityWarn, report.Severity)
			require.Len(t, report.Results, 1)

			finding := report.Results[0]
			assert.Equal(t, "index-bloat", finding.ID)
			assert.Equal(t, check.SeverityWarn, finding.Severity)
			require.NotNil(t, finding.Table)
			require.Len(t, finding.Table.Rows, 1)
			assert.Equal(t, tt.rowSeverity, finding.Table.Rows[0].Severity)
		})
	}
}

func TestIndexBloat_FindingDetailsAndTable(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.IndexBloatRow{
		makeIndexRow("public", "users", "users_email_idx", 80.0, 800*check.MiB, check.GiB),
		makeIndexRow("public", "orders", "orders_status_idx", 55.0, 50*check.MiB, 300*check.MiB),
	})

	require.Len(t, report.Results, 1)
	finding := report.Results[0]

	assert.Equal(t, "index-bloat", finding.ID)
	assert.Equal(t, "Index Bloat", finding.Name)
	assert.Equal(t, "2 bloated index(es) (1 with >=2GiB wasted or >=70% bloat)", finding.Details)

	require.NotNil(t, finding.Table)
	assert.Equal(t, []string{"Table", "Index", "Size", "Wasted", "Bloat %"}, finding.Table.Headers)
	require.Len(t, finding.Table.Rows, 2)
	assert.Equal(t, []string{"public.users", "users_email_idx", "1.0GiB", "800.0MiB", "80.0%"}, finding.Table.Rows[0].Cells)
	assert.Equal(t, []string{"public.orders", "orders_status_idx", "300.0MiB", "50.0MiB", "55.0%"}, finding.Table.Rows[1].Cells)
}

func TestIndexBloat_SortsWorstFirst(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.IndexBloatRow{
		// INFO row with more waste than the WARN pct row: severity still wins.
		makeIndexRow("public", "small_info", "small_info_idx", 55.0, 300*check.MiB, 600*check.MiB),
		makeIndexRow("public", "warn_pct", "warn_pct_idx", 75.0, 100*check.MiB, 300*check.MiB),
		makeIndexRow("public", "warn_big", "warn_big_idx", 40.0, 3*check.GiB, 8*check.GiB),
		makeIndexRow("public", "tiny_info", "tiny_info_idx", 52.0, 40*check.MiB, 250*check.MiB),
	})

	require.Len(t, report.Results, 1)
	finding := report.Results[0]
	require.NotNil(t, finding.Table)
	require.Len(t, finding.Table.Rows, 4)

	var order []string
	for _, row := range finding.Table.Rows {
		order = append(order, row.Cells[1])
	}
	assert.Equal(t, []string{"warn_big_idx", "warn_pct_idx", "small_info_idx", "tiny_info_idx"}, order)

	assert.Equal(t, check.SeverityWarn, finding.Table.Rows[0].Severity)
	assert.Equal(t, check.SeverityWarn, finding.Table.Rows[1].Severity)
	assert.Equal(t, check.SeverityInfo, finding.Table.Rows[2].Severity)
	assert.Equal(t, check.SeverityInfo, finding.Table.Rows[3].Severity)
}

func TestIndexBloat_QueryError(t *testing.T) {
	t.Parallel()

	checker := indexbloat.New(&mockQueryer{err: fmt.Errorf("connection refused")})
	_, err := checker.Check(context.Background())

	require.ErrorContains(t, err, "index-bloat")
	require.ErrorContains(t, err, "connection refused")
}

func TestIndexBloat_Metadata(t *testing.T) {
	t.Parallel()

	metadata := indexbloat.Metadata()

	assert.Equal(t, "index-bloat", metadata.CheckID)
	assert.Equal(t, "Index Bloat", metadata.Name)
	assert.Equal(t, check.CategoryIndexes, metadata.Category)
	assert.NotEmpty(t, metadata.Description)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
}
