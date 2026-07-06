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

func (m *mockQueryer) IndexBloat(ctx context.Context) ([]db.IndexBloatRow, error) {
	return m.rows, m.err
}

// makeIndexRow builds a pre-filtered row as the SQL would return it (≥30% bloat,
// ≥100 MiB wasted). Qualification lives in SQL, so tests feed already-qualifying rows.
func makeIndexRow(schemaName, tableName, indexName string, bloatPct float64, bloatBytes, actualBytes int64) db.IndexBloatRow {
	var bloatNumeric pgtype.Numeric
	_ = bloatNumeric.Scan(fmt.Sprintf("%.2f", bloatPct))

	return db.IndexBloatRow{
		Schemaname:   pgtype.Text{String: schemaName, Valid: true},
		Tablename:    pgtype.Text{String: tableName, Valid: true},
		Indexname:    pgtype.Text{String: indexName, Valid: true},
		BloatPercent: bloatNumeric,
		BloatBytes:   pgtype.Int8{Int64: bloatBytes, Valid: true},
		ActualBytes:  pgtype.Int8{Int64: actualBytes, Valid: true},
	}
}

func TestIndexBloat_SeverityInvariant(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public", "users", "users_email_idx", 80.0, 2*1024*1024*1024, 4*1024*1024*1024),
		},
	}

	report, err := indexbloat.New(queryer).Check(context.Background())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
}

func TestIndexBloat_BloatedIndexesFinding(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public", "users", "users_email_idx", 55.0, oneGB, 3*oneGB),
		},
	}

	report, err := indexbloat.New(queryer).Check(context.Background())

	require.NoError(t, err)
	require.Len(t, report.Results, 1)

	finding := report.Results[0]
	assert.Equal(t, "bloated-indexes", finding.ID)
	assert.Equal(t, "Bloated Indexes", finding.Name)
	assert.Equal(t, check.SeverityWarn, finding.Severity)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	require.NotNil(t, finding.Table)
	assert.Equal(t, []string{"Table", "Index", "Size", "Bloat %", "Wasted"}, finding.Table.Headers)
	require.Len(t, finding.Table.Rows, 1)

	row := finding.Table.Rows[0]
	assert.Equal(t, "public.users", row.Cells[0])
	assert.Equal(t, "users_email_idx", row.Cells[1])
	assert.Equal(t, check.FormatBytes(3*oneGB), row.Cells[2]) // actual size
	assert.Equal(t, "55.0", row.Cells[3])                     // bare number, one decimal
	assert.Equal(t, check.FormatBytes(oneGB), row.Cells[4])   // wasted
	assert.Equal(t, check.SeverityWarn, row.Severity)
}

func TestIndexBloat_WastedDescOrderingAndReclaimSum(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	// SQL returns rows already wasted-desc; the check must preserve that order.
	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public", "events", "events_ts_idx", 60.0, 2*oneGB, 5*oneGB),
			makeIndexRow("public", "users", "users_email_idx", 55.0, oneGB, 3*oneGB),
			makeIndexRow("app", "orders", "orders_idx", 40.0, 150*1024*1024, 400*1024*1024),
		},
	}

	report, err := indexbloat.New(queryer).Check(context.Background())

	require.NoError(t, err)
	require.Len(t, report.Results, 1)

	finding := report.Results[0]
	require.NotNil(t, finding.Table)
	require.Len(t, finding.Table.Rows, 3)

	// Order preserved from SQL (wasted-desc).
	assert.Equal(t, "events_ts_idx", finding.Table.Rows[0].Cells[1])
	assert.Equal(t, "users_email_idx", finding.Table.Rows[1].Cells[1])
	assert.Equal(t, "orders_idx", finding.Table.Rows[2].Cells[1])

	// Schema-qualified table cell for a non-public schema.
	assert.Equal(t, "app.orders", finding.Table.Rows[2].Cells[0])

	// Details reports count and summed reclaimable bytes.
	totalWasted := int64(2*oneGB + oneGB + 150*1024*1024)
	assert.Contains(t, finding.Details, "Found 3 bloated index(es)")
	assert.Contains(t, finding.Details, check.FormatBytes(totalWasted))
	assert.Contains(t, finding.Details, "reclaimable via REINDEX CONCURRENTLY")

	for _, row := range finding.Table.Rows {
		assert.Equal(t, check.SeverityWarn, row.Severity)
	}
}

func TestIndexBloat_EmptyResult(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.IndexBloatRow{}}
	report, err := indexbloat.New(queryer).Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	require.Len(t, report.Results, 1)
	assert.Equal(t, "index-bloat", report.Results[0].ID)
	assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
	assert.Contains(t, report.Results[0].Details, "No significant index bloat detected")
}

func TestIndexBloat_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.IndexBloatRow{}}
	checker := indexbloat.New(queryer)

	metadata := checker.Metadata()
	assert.Equal(t, "index-bloat", metadata.CheckID)
	assert.Equal(t, "Index Bloat", metadata.Name)
	assert.Equal(t, check.CategoryIndexes, metadata.Category)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
	assert.NotEmpty(t, metadata.Description)
}
