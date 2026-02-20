package indexbloat_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/indexbloat"
	"github.com/emancu/pgdoctor/db"
)

type mockQueryer struct {
	rows []db.IndexBloatRow
	err  error
}

func (m *mockQueryer) IndexBloat(ctx context.Context) ([]db.IndexBloatRow, error) {
	return m.rows, m.err
}

func makeIndexRow(tableName, indexName string, bloatPct float64, bloatBytes, actualBytes int64) db.IndexBloatRow {
	var bloatNumeric pgtype.Numeric
	_ = bloatNumeric.Scan(fmt.Sprintf("%.2f", bloatPct))

	return db.IndexBloatRow{
		Tablename:    pgtype.Text{String: tableName, Valid: true},
		Indexname:    pgtype.Text{String: indexName, Valid: true},
		BloatPercent: bloatNumeric,
		BloatBytes:   pgtype.Int8{Int64: bloatBytes, Valid: true},
		ActualBytes:  pgtype.Int8{Int64: actualBytes, Valid: true},
	}
}

func TestIndexBloat_AllHealthy(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public.users", "users_pkey", 10.0, 10*1024*1024, 100*1024*1024),
			makeIndexRow("public.orders", "orders_pkey", 15.0, 5*1024*1024, 50*1024*1024),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 2)
	assert.Equal(t, "high-bloat", report.Results[0].ID)
	assert.Equal(t, "large-bloat", report.Results[1].ID)
	assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
	assert.Equal(t, check.SeverityOK, report.Results[1].Severity)
}

func TestIndexBloat_HighPercentageWarning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public.users", "users_email_idx", 55.0, 50*1024*1024, 100*1024*1024),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)
	assert.Len(t, report.Results, 2)

	highBloatFinding := report.Results[0]
	assert.Equal(t, "high-bloat", highBloatFinding.ID)
	assert.Equal(t, check.SeverityWarn, highBloatFinding.Severity)
	assert.Contains(t, highBloatFinding.Details, "1 index(es)")
	assert.NotNil(t, highBloatFinding.Table)
	assert.Len(t, highBloatFinding.Table.Rows, 1)
}

func TestIndexBloat_HighPercentageCritical(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public.users", "users_email_idx", 75.0, 750*1024*1024, 1000*1024*1024),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	highBloatFinding := report.Results[0]
	assert.Equal(t, "high-bloat", highBloatFinding.ID)
	assert.Equal(t, check.SeverityWarn, highBloatFinding.Severity)
	assert.Contains(t, highBloatFinding.Details, "1 index(es)")
}

func TestIndexBloat_LargeAbsoluteWarning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public.orders", "orders_created_at_idx", 40.0, 150*1024*1024, 375*1024*1024),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	largeBloatFinding := report.Results[1]
	assert.Equal(t, "large-bloat", largeBloatFinding.ID)
	assert.Equal(t, check.SeverityWarn, largeBloatFinding.Severity)
	assert.Contains(t, largeBloatFinding.Details, "1 index(es)")
}

func TestIndexBloat_LargeAbsoluteCritical(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			makeIndexRow("public.events", "events_timestamp_idx", 50.0, 2*oneGB, 4*oneGB),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	largeBloatFinding := report.Results[1]
	assert.Equal(t, "large-bloat", largeBloatFinding.ID)
	assert.Equal(t, check.SeverityWarn, largeBloatFinding.Severity)
}

func TestIndexBloat_MixedSeverity(t *testing.T) {
	t.Parallel()

	const oneGB = 1024 * 1024 * 1024
	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			// High percentage, critical
			makeIndexRow("public.users", "users_email_idx", 80.0, 800*1024*1024, 1000*1024*1024),
			// High percentage, warning
			makeIndexRow("public.orders", "orders_status_idx", 60.0, 50*1024*1024, 83*1024*1024),
			// Large absolute, critical
			makeIndexRow("public.events", "events_idx", 45.0, 2*oneGB, 4*oneGB+500*1024*1024),
			// Low bloat, should be ignored
			makeIndexRow("public.products", "products_pkey", 10.0, 10*1024*1024, 100*1024*1024),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	// Check high-bloat finding
	highBloatFinding := report.Results[0]
	assert.Equal(t, check.SeverityWarn, highBloatFinding.Severity)
	assert.NotNil(t, highBloatFinding.Table)
	criticalCount := 0
	warnCount := 0
	for _, row := range highBloatFinding.Table.Rows {
		switch row.Severity {
		case check.SeverityFail:
			criticalCount++
		case check.SeverityWarn:
			warnCount++
		}
	}
	assert.Equal(t, 1, criticalCount, "should have 1 critical high-bloat index")
	assert.Equal(t, 1, warnCount, "should have 1 warning high-bloat index")
}

func TestIndexBloat_BelowBloatThreshold(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		rows: []db.IndexBloatRow{
			// Bloat < 30%, should be ignored by large-bloat check
			makeIndexRow("public.users", "users_idx", 25.0, 20*1024*1024, 80*1024*1024),
		},
	}

	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
	assert.Equal(t, "high-bloat", report.Results[0].ID)
	assert.Equal(t, check.SeverityOK, report.Results[1].Severity)
	assert.Equal(t, "large-bloat", report.Results[1].ID)
}

func TestIndexBloat_EdgeCases_ExactThresholds(t *testing.T) {
	t.Parallel()

	const hundredMB = 100 * 1024 * 1024
	const oneGB = 1024 * 1024 * 1024

	tests := []struct {
		name                       string
		row                        db.IndexBloatRow
		expectedHighBloatSeverity  check.Severity
		expectedLargeBloatSeverity check.Severity
	}{
		{
			name:                       "exactly 50% bloat - warning threshold",
			row:                        makeIndexRow("public.t1", "idx1", 50.0, 500*1024*1024, 1000*1024*1024),
			expectedHighBloatSeverity:  check.SeverityWarn,
			expectedLargeBloatSeverity: check.SeverityWarn, // 500MB >= 100MB and 50% >= 30%
		},
		{
			name:                       "exactly 70% bloat - critical threshold",
			row:                        makeIndexRow("public.t2", "idx2", 70.0, 700*1024*1024, 1000*1024*1024),
			expectedHighBloatSeverity:  check.SeverityWarn,
			expectedLargeBloatSeverity: check.SeverityWarn, // 700MB < 1GB, so warning
		},
		{
			name:                       "exactly 100MB bloat - warning threshold",
			row:                        makeIndexRow("public.t3", "idx3", 40.0, hundredMB, 250*1024*1024),
			expectedHighBloatSeverity:  check.SeverityOK,
			expectedLargeBloatSeverity: check.SeverityWarn,
		},
		{
			name:                       "exactly 1GB bloat - critical threshold",
			row:                        makeIndexRow("public.t4", "idx4", 35.0, oneGB, 3*oneGB),
			expectedHighBloatSeverity:  check.SeverityOK,
			expectedLargeBloatSeverity: check.SeverityWarn,
		},
		{
			name:                       "just below 50% - OK for high-bloat",
			row:                        makeIndexRow("public.t5", "idx5", 49.9, 499*1024*1024, 1000*1024*1024),
			expectedHighBloatSeverity:  check.SeverityOK,
			expectedLargeBloatSeverity: check.SeverityWarn, // 499MB >= 100MB and 49.9% >= 30%
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{rows: []db.IndexBloatRow{tt.row}}
			checker := indexbloat.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.expectedHighBloatSeverity, report.Results[0].Severity, "high-bloat severity")
			assert.Equal(t, tt.expectedLargeBloatSeverity, report.Results[1].Severity, "large-bloat severity")
		})
	}
}

func TestIndexBloat_EmptyResult(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.IndexBloatRow{}}
	checker := indexbloat.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 1)
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
