package toaststorage_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/toaststorage"
	"github.com/emancu/pgdoctor/db"
)

const (
	findingIDToastRatio           = "toast-ratio"
	findingIDLargeToast           = "large-toast"
	findingIDToastBloat           = "toast-bloat"
	findingIDWideColumns          = "wide-columns"
	findingIDCompressionAlgorithm = "compression-algorithm"
)

type mockQueryer struct {
	rows []db.ToastStorageRow
	err  error
}

func (m *mockQueryer) ToastStorage(context.Context) ([]db.ToastStorageRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func makeToastRow(schema, table, toastTable string, mainSize, toastSize, totalSize int64, toastPercent float64) db.ToastStorageRow {
	percentNumeric := &pgtype.Numeric{}
	_ = percentNumeric.Scan(fmt.Sprintf("%.2f", toastPercent))

	return db.ToastStorageRow{
		SchemaName:            pgtype.Text{String: schema, Valid: true},
		TableName:             pgtype.Text{String: table, Valid: true},
		ToastTableName:        pgtype.Text{String: toastTable, Valid: true},
		MainTableSize:         pgtype.Int8{Int64: mainSize, Valid: true},
		ToastSize:             pgtype.Int8{Int64: toastSize, Valid: true},
		TotalSize:             pgtype.Int8{Int64: totalSize, Valid: true},
		IndexesSize:           pgtype.Int8{Int64: 0, Valid: true},
		ToastPercent:          *percentNumeric,
		ToastLiveTuples:       pgtype.Int8{Int64: 1000, Valid: true},
		ToastDeadTuples:       pgtype.Int8{Int64: 0, Valid: true},
		WideColumns:           []string{},
		ColumnCompressionInfo: []string{},
	}
}

func Test_ToastStorage_NoIssues(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.ToastStorageRow{}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
	require.Equal(t, 1, len(report.Results))
	require.Contains(t, report.Results[0].Details, "No tables with significant TOAST storage")
}

func Test_ToastStorage_ExcessiveRatio_FAIL(t *testing.T) {
	t.Parallel()

	rows := []db.ToastStorageRow{
		makeToastRow("public", "events", "pg_toast.pg_toast_12345", 10*check.GiB, 85*check.GiB, 95*check.GiB, 89.47),
	}

	queryer := &mockQueryer{rows: rows}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	// Find toast-ratio subcheck
	var ratioFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDToastRatio {
			ratioFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, ratioFinding, "toast-ratio subcheck should be present")
	require.Equal(t, check.SeverityWarn, ratioFinding.Severity)
	require.Contains(t, ratioFinding.Details, "high TOAST storage ratio")
	require.NotNil(t, ratioFinding.Table)

	require.Equal(t, 1, len(ratioFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, ratioFinding.Table.Rows[0].Severity)
}

func Test_ToastStorage_ExcessiveRatio_WARN(t *testing.T) {
	t.Parallel()

	rows := []db.ToastStorageRow{
		makeToastRow("public", "logs", "pg_toast.pg_toast_23456", 30*check.GiB, 55*check.GiB, 85*check.GiB, 64.71),
	}

	queryer := &mockQueryer{rows: rows}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var ratioFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDToastRatio {
			ratioFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, ratioFinding)
	require.Equal(t, check.SeverityWarn, ratioFinding.Severity)
	require.Equal(t, check.SeverityWarn, ratioFinding.Table.Rows[0].Severity)
}

func Test_ToastStorage_LargeToast_FAIL(t *testing.T) {
	t.Parallel()

	rows := []db.ToastStorageRow{
		makeToastRow("public", "documents", "pg_toast.pg_toast_34567", 50*check.GiB, 150*check.GiB, 200*check.GiB, 75.0),
	}
	rows[0].WideColumns = []string{"content:50000:text", "metadata:10000:jsonb"}

	queryer := &mockQueryer{rows: rows}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeToast {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	require.Equal(t, check.SeverityWarn, largeFinding.Severity)
	require.Contains(t, largeFinding.Details, "very large TOAST storage")
	require.NotNil(t, largeFinding.Table)
	require.Equal(t, check.SeverityFail, largeFinding.Table.Rows[0].Severity)
	require.Contains(t, largeFinding.Table.Rows[0].Cells[3], "content")
}

func Test_ToastStorage_LargeToast_WARN(t *testing.T) {
	t.Parallel()

	rows := []db.ToastStorageRow{
		makeToastRow("public", "audit_logs", "pg_toast.pg_toast_45678", 5*check.GiB, 15*check.GiB, 20*check.GiB, 75.0),
	}

	queryer := &mockQueryer{rows: rows}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var largeFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDLargeToast {
			largeFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, largeFinding)
	require.Equal(t, check.SeverityWarn, largeFinding.Severity)
}

func Test_ToastStorage_Bloat_FAIL(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "users", "pg_toast.pg_toast_56789", 10*check.GiB, 20*check.GiB, 30*check.GiB, 66.67)
	row.ToastLiveTuples = pgtype.Int8{Int64: 5000, Valid: true}
	row.ToastDeadTuples = pgtype.Int8{Int64: 6000, Valid: true} // >50% dead

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var bloatFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDToastBloat {
			bloatFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, bloatFinding)
	require.Equal(t, check.SeverityWarn, bloatFinding.Severity)
	require.Contains(t, bloatFinding.Details, "excessive dead tuples")
	require.NotNil(t, bloatFinding.Table)
	require.Equal(t, check.SeverityFail, bloatFinding.Table.Rows[0].Severity)
}

func Test_ToastStorage_Bloat_WARN(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "orders", "pg_toast.pg_toast_67890", 10*check.GiB, 20*check.GiB, 30*check.GiB, 66.67)
	row.ToastLiveTuples = pgtype.Int8{Int64: 7000, Valid: true}
	row.ToastDeadTuples = pgtype.Int8{Int64: 3500, Valid: true} // 33% dead

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var bloatFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDToastBloat {
			bloatFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, bloatFinding)
	require.Equal(t, check.SeverityWarn, bloatFinding.Severity)
}

func Test_ToastStorage_WideColumns_JSONB(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_78901", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.WideColumns = []string{
		"payload:8000:jsonb",
		"metadata:6000:jsonb",
	}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var wideFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDWideColumns {
			wideFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, wideFinding)
	require.Equal(t, check.SeverityWarn, wideFinding.Severity)
	require.Contains(t, wideFinding.Details, "JSONB")
	require.NotNil(t, wideFinding.Table)
	require.Equal(t, 2, len(wideFinding.Table.Rows))
	require.Contains(t, wideFinding.Table.Rows[0].Cells[1], "payload")
	require.Contains(t, wideFinding.Table.Rows[0].Cells[3], "jsonb")
}

func Test_ToastStorage_WideColumns_Text(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "documents", "pg_toast.pg_toast_89012", 5*check.GiB, 10*check.GiB, 15*check.GiB, 66.67)
	row.WideColumns = []string{
		"content:15000:text",
	}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var wideFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDWideColumns {
			wideFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, wideFinding)
	require.Equal(t, check.SeverityWarn, wideFinding.Severity)
	require.NotNil(t, wideFinding.Table)
	require.Contains(t, wideFinding.Table.Rows[0].Cells[1], "content")
	require.Contains(t, wideFinding.Table.Rows[0].Cells[3], "text")
}

func Test_ToastStorage_CompressionAlgorithm_DefaultCompression(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_90123", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.ColumnCompressionInfo = []string{
		"payload:default:EXTENDED:jsonb",
		"metadata:default:EXTENDED:jsonb",
	}

	// Create context with PostgreSQL 14+ metadata
	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion: "14.8",
	})

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(ctx)

	require.NoError(t, err)

	var compressionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDCompressionAlgorithm {
			compressionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, compressionFinding, "compression-algorithm subcheck should be present")
	require.Equal(t, check.SeverityWarn, compressionFinding.Severity)
	require.Contains(t, compressionFinding.Details, "suboptimal compression")
	require.NotNil(t, compressionFinding.Table)
	require.Equal(t, 2, len(compressionFinding.Table.Rows))
	require.Contains(t, compressionFinding.Table.Rows[0].Cells[1], "payload")
	require.Contains(t, compressionFinding.Table.Rows[0].Cells[3], "default")
	require.Contains(t, compressionFinding.Table.Rows[0].Cells[5], "lz4")
}

func Test_ToastStorage_CompressionAlgorithm_PglzCompression(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "logs", "pg_toast.pg_toast_01234", 5*check.GiB, 10*check.GiB, 15*check.GiB, 66.67)
	row.ColumnCompressionInfo = []string{
		"message:pglz:EXTENDED:text",
	}

	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion: "15.2",
	})

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(ctx)

	require.NoError(t, err)

	var compressionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDCompressionAlgorithm {
			compressionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, compressionFinding)
	require.Equal(t, check.SeverityWarn, compressionFinding.Severity)
	require.Contains(t, compressionFinding.Table.Rows[0].Cells[3], "pglz")
}

func Test_ToastStorage_CompressionAlgorithm_OptimalLZ4(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_12340", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.ColumnCompressionInfo = []string{
		"payload:lz4:EXTENDED:jsonb",
		"metadata:lz4:EXTENDED:jsonb",
	}

	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion: "14.8",
	})

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(ctx)

	require.NoError(t, err)

	var compressionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDCompressionAlgorithm {
			compressionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, compressionFinding)
	require.Equal(t, check.SeverityOK, compressionFinding.Severity)
	require.Contains(t, compressionFinding.Details, "optimal compression")
}

func Test_ToastStorage_CompressionAlgorithm_SkipsOnPG13(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_23450", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.ColumnCompressionInfo = []string{
		"payload:default:EXTENDED:jsonb",
	}

	// PostgreSQL 13 (no LZ4 support)
	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion: "13.11",
	})

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(ctx)

	require.NoError(t, err)

	// compression-algorithm subcheck should not be present
	for i := range report.Results {
		require.NotEqual(t, "compression-algorithm", report.Results[i].ID,
			"compression-algorithm subcheck should not run on PG < 14")
	}
}

func Test_ToastStorage_CompressionAlgorithm_ByteaExternal(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "media", "pg_toast.pg_toast_34560", 5*check.GiB, 10*check.GiB, 15*check.GiB, 66.67)
	row.ColumnCompressionInfo = []string{
		"file_data:default:EXTENDED:bytea",
	}

	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion: "14.8",
	})

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(ctx)

	require.NoError(t, err)

	var compressionFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDCompressionAlgorithm {
			compressionFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, compressionFinding)
	require.Equal(t, check.SeverityWarn, compressionFinding.Severity)
	// For bytea + EXTENDED, should suggest considering EXTERNAL
	require.Contains(t, compressionFinding.Table.Rows[0].Cells[5], "EXTERNAL")
}

func Test_ToastStorage_MultipleTables_MultipleSeverities(t *testing.T) {
	t.Parallel()

	rows := []db.ToastStorageRow{
		makeToastRow("public", "events", "pg_toast.pg_toast_11111", 10*check.GiB, 85*check.GiB, 95*check.GiB, 89.47),
		makeToastRow("public", "logs", "pg_toast.pg_toast_22222", 30*check.GiB, 55*check.GiB, 85*check.GiB, 64.71),
		makeToastRow("public", "documents", "pg_toast.pg_toast_33333", 40*check.GiB, 30*check.GiB, 70*check.GiB, 42.86),
	}

	queryer := &mockQueryer{rows: rows}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	// Should have multiple findings
	require.GreaterOrEqual(t, len(report.Results), 4, "Should have multiple subcheck findings")

	// Check toast-ratio has mixed severities
	var ratioFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDToastRatio {
			ratioFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, ratioFinding)
	require.NotNil(t, ratioFinding.Table)
	require.Equal(t, 2, len(ratioFinding.Table.Rows), "Should have 2 tables over 50% threshold")
}

func Test_ToastStorage_QueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{err: expectedErr}
	checker := toaststorage.New(queryer)

	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "TOAST storage")
}

func Test_ToastStorage_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.ToastStorageRow{}}
	checker := toaststorage.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "toast-storage", metadata.CheckID)
	require.Equal(t, "TOAST Storage Analysis", metadata.Name)
	require.Equal(t, check.CategorySchema, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
	require.Contains(t, metadata.Description, "TOAST storage")
}

func Test_ToastStorage_PrescriptionContent(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_44444", 10*check.GiB, 85*check.GiB, 95*check.GiB, 89.47)

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.NotEmpty(t, report.Results)
}
