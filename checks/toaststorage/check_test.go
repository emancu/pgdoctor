package toaststorage_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/toaststorage"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

const (
	findingIDToastRatio           = "toast-ratio"
	findingIDToastBloat           = "toast-bloat"
	findingIDWideColumns          = "wide-columns"
	findingIDCompressionAlgorithm = "compression-algorithm"
	findingIDCompressionDefault   = "compression-default"
)

type mockQueryer struct {
	rows        []db.ToastStorageRow
	err         error
	defaultComp string
}

func (m *mockQueryer) ToastStorage(context.Context) ([]db.ToastStorageRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rows, nil
}

func (m *mockQueryer) ToastDefaultCompression(context.Context) (string, error) {
	return m.defaultComp, nil
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

	queryer := &mockQueryer{rows: []db.ToastStorageRow{}, defaultComp: "lz4"}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)
	require.Equal(t, 2, len(report.Results))
	require.Equal(t, "compression-default", report.Results[0].ID)
	require.Contains(t, report.Results[1].Details, "No tables with significant TOAST storage")
}

func Test_ToastStorage_CompressionDefault_FiresWithoutToast(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{rows: []db.ToastStorageRow{}, defaultComp: "pglz"}
	checker := toaststorage.New(queryer)

	report, err := checker.Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	def := report.Results[0]
	require.Equal(t, "compression-default", def.ID)
	require.Equal(t, check.SeverityInfo, def.Severity)
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_ToastStorage_Heavy_MergedGate_Info(t *testing.T) {
	t.Parallel()

	rows := []db.ToastStorageRow{
		// ratio arm only: 75% ratio, 3GiB TOAST (under the 10GB size gate)
		makeToastRow("public", "hot_small", "pg_toast.pg_toast_10001", 1*check.GiB, 3*check.GiB, 4*check.GiB, 75.0),
		// size arm only: 40% ratio, 77GiB TOAST (over the 10GB size gate)
		makeToastRow("public", "audit_big", "pg_toast.pg_toast_10002", 115*check.GiB, 77*check.GiB, 192*check.GiB, 40.0),
		// both arms: 60% ratio, 20GiB TOAST
		makeToastRow("public", "mixed", "pg_toast.pg_toast_10003", 13*check.GiB, 20*check.GiB, 33*check.GiB, 60.0),
		// under both gates: 45% ratio, 9GiB TOAST — not listed
		makeToastRow("public", "small", "pg_toast.pg_toast_10004", 11*check.GiB, 9*check.GiB, 20*check.GiB, 45.0),
	}

	report, err := toaststorage.New(&mockQueryer{rows: rows, defaultComp: "lz4"}).Check(context.Background())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := findingByID(report, findingIDToastRatio)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Contains(t, f.Details, "Found 3 TOAST-heavy table(s) (>50% ratio or >10GB)")
	require.NotNil(t, f.Table)
	require.Equal(t, []string{"TOAST Size", "TOAST %", "Main Size", "Total", "Table"}, f.Table.Headers)
	require.Len(t, f.Table.Rows, 3)

	// Sorted by TOAST size desc; table name is the last cell.
	require.Equal(t, "public.audit_big", f.Table.Rows[0].Cells[4])
	require.Equal(t, "public.mixed", f.Table.Rows[1].Cells[4])
	require.Equal(t, "public.hot_small", f.Table.Rows[2].Cells[4])

	for _, r := range f.Table.Rows {
		require.Equal(t, check.SeverityInfo, r.Severity)
	}

	require.Equal(t, check.SeverityPass, report.Severity, "Info finding must not escalate the report")
}

func Test_ToastStorage_Heavy_GateBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		toastSize  int64
		percent    float64
		wantListed bool
	}{
		{name: "under both gates", toastSize: 9 * check.GiB, percent: 49.9, wantListed: false},
		{name: "ratio exactly at gate", toastSize: 1 * check.GiB, percent: 50.0, wantListed: true},
		{name: "size exactly at gate", toastSize: 10 * check.GiB, percent: 5.0, wantListed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := makeToastRow("public", "t", "pg_toast.pg_toast_20001", 10*check.GiB, tt.toastSize, 20*check.GiB, tt.percent)
			report, err := toaststorage.New(&mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "lz4"}).Check(context.Background())

			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			f := findingByID(report, findingIDToastRatio)
			require.NotNil(t, f)
			if tt.wantListed {
				require.Equal(t, check.SeverityInfo, f.Severity)
				require.NotNil(t, f.Table)
				require.Len(t, f.Table.Rows, 1)
			} else {
				require.Equal(t, check.SeverityPass, f.Severity)
				require.Nil(t, f.Table)
				require.Contains(t, f.Details, "No TOAST-heavy tables")
			}
		})
	}
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

func findingByID(report *check.Report, id string) *check.Finding {
	for i := range report.Results {
		if report.Results[i].ID == id {
			return &report.Results[i]
		}
	}
	return nil
}

func compressionFinding(t *testing.T, report *check.Report) *check.Finding {
	t.Helper()
	return findingByID(report, findingIDCompressionAlgorithm)
}

func pg14Ctx() context.Context {
	return check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{EngineVersion: "14.8"})
}

func Test_ToastStorage_CompressionAlgorithm_Info_DetailsOnly(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_90123", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.ColumnCompressionInfo = []string{
		"payload:default:EXTENDED:jsonb",
		"metadata:pglz:EXTENDED:jsonb",
	}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "pglz"}
	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := compressionFinding(t, report)
	require.NotNil(t, f, "compression-algorithm subcheck should be present")
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Contains(t, f.Details, "2 column(s) on 1 table(s) use pglz compression")
	require.Nil(t, f.Table, "itemization moved to Debug; no table in normal output")
	require.Contains(t, f.Debug, "public.events.payload")
	require.Contains(t, f.Debug, "effective pglz")
}

func Test_ToastStorage_CompressionAlgorithm_EffectiveAwareness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		algo        string
		clusterAlgo string
		wantCounted bool
	}{
		{name: "explicit pglz counts on lz4 default", algo: "pglz", clusterAlgo: "lz4", wantCounted: true},
		{name: "unset does not count on lz4 default", algo: "default", clusterAlgo: "lz4", wantCounted: false},
		{name: "unset counts on pglz default", algo: "default", clusterAlgo: "pglz", wantCounted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := makeToastRow("public", "events", "pg_toast.pg_toast_90124", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
			row.ColumnCompressionInfo = []string{"payload:" + tt.algo + ":EXTENDED:jsonb"}

			queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: tt.clusterAlgo}
			report, err := toaststorage.New(queryer).Check(pg14Ctx())

			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			f := compressionFinding(t, report)
			require.NotNil(t, f)
			if tt.wantCounted {
				require.Equal(t, check.SeverityInfo, f.Severity)
				require.Contains(t, f.Details, "1 column(s) on 1 table(s) use pglz compression")
			} else {
				require.Equal(t, check.SeverityPass, f.Severity)
				require.Contains(t, f.Details, "optimal compression")
			}
		})
	}
}

func Test_ToastStorage_CompressionAlgorithm_DoesNotEscalate(t *testing.T) {
	t.Parallel()

	// Below every other subcheck's threshold: ratio <50%, TOAST <10GiB, no bloat, no wide columns.
	row := makeToastRow("public", "small", "pg_toast.pg_toast_55550", 10*check.GiB, 2*check.GiB, 12*check.GiB, 16.0)
	row.ColumnCompressionInfo = []string{"payload:default:EXTENDED:jsonb"}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "pglz"}
	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := compressionFinding(t, report)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Equal(t, check.SeverityPass, report.Severity, "Info finding must not escalate the report")
}

func Test_ToastStorage_CompressionAlgorithm_DebugFloorBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toastSize int64
		wantDebug bool
	}{
		{name: "just under 1GiB is not itemized in Debug", toastSize: check.GiB, wantDebug: false},
		{name: "just over 1GiB is itemized in Debug", toastSize: check.GiB + 1, wantDebug: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := makeToastRow("public", "events", "pg_toast.pg_toast_66660", 10*check.GiB, tt.toastSize, 12*check.GiB, 16.0)
			row.ColumnCompressionInfo = []string{"payload:default:EXTENDED:jsonb"}

			queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "pglz"}
			report, err := toaststorage.New(queryer).Check(pg14Ctx())

			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			f := compressionFinding(t, report)
			require.NotNil(t, f)
			require.Equal(t, check.SeverityInfo, f.Severity)
			require.Contains(t, f.Details, "1 column(s) on 1 table(s) use pglz compression")
			require.Nil(t, f.Table)
			if tt.wantDebug {
				require.Contains(t, f.Debug, "public.events.payload")
			} else {
				require.Empty(t, f.Debug, "sub-floor columns carry the aggregate Details but no Debug listing")
			}
		})
	}
}

func Test_ToastStorage_CompressionAlgorithm_DebugAlwaysListsBigToast(t *testing.T) {
	t.Parallel()

	// lz4 everywhere: PASS path, but big-TOAST columns must still appear in Debug, sorted by size desc.
	rows := []db.ToastStorageRow{
		makeToastRow("public", "small_lz4", "pg_toast.pg_toast_77771", 5*check.GiB, 2*check.GiB, 7*check.GiB, 28.0),
		makeToastRow("public", "big_lz4", "pg_toast.pg_toast_77772", 5*check.GiB, 4*check.GiB, 9*check.GiB, 44.0),
	}
	rows[0].ColumnCompressionInfo = []string{"a:lz4:EXTENDED:jsonb"}
	rows[1].ColumnCompressionInfo = []string{"b:lz4:EXTENDED:jsonb"}

	queryer := &mockQueryer{rows: rows, defaultComp: "lz4"}
	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := compressionFinding(t, report)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityPass, f.Severity, "all lz4 is a PASS")
	require.Contains(t, f.Debug, "public.big_lz4.b")
	require.Contains(t, f.Debug, "public.small_lz4.a")
	require.Contains(t, f.Debug, "effective lz4")
	require.Less(t,
		strings.Index(f.Debug, "public.big_lz4.b"),
		strings.Index(f.Debug, "public.small_lz4.a"),
		"Debug is sorted by TOAST size descending")
}

func Test_ToastStorage_CompressionAlgorithm_OptimalLZ4(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_12340", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.ColumnCompressionInfo = []string{
		"payload:lz4:EXTENDED:jsonb",
		"metadata:lz4:EXTENDED:jsonb",
	}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "lz4"}
	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := compressionFinding(t, report)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Contains(t, f.Details, "optimal compression")
}

func Test_ToastStorage_CompressionAlgorithm_SkipsOnPG13(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_23450", 10*check.GiB, 15*check.GiB, 25*check.GiB, 60.0)
	row.ColumnCompressionInfo = []string{
		"payload:default:EXTENDED:jsonb",
	}

	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{
		EngineVersion: "13.11",
	})

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}}
	report, err := toaststorage.New(queryer).Check(ctx)

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	require.Nil(t, compressionFinding(t, report), "compression-algorithm subcheck should not run on PG < 14")
	require.Nil(t, findingByID(report, findingIDCompressionDefault), "compression-default should not run on PG < 14")
}

func Test_ToastStorage_CompressionDefault_PglzInfo(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_34561", 5*check.GiB, 2*check.GiB, 7*check.GiB, 28.0)
	row.ColumnCompressionInfo = []string{"payload:lz4:EXTENDED:jsonb"}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "pglz"}
	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := findingByID(report, findingIDCompressionDefault)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityInfo, f.Severity)
	require.Contains(t, f.Details, "default_toast_compression is pglz")
	require.Nil(t, f.Table)
	require.Equal(t, check.SeverityPass, report.Severity, "Info finding must not escalate the report")
}

func Test_ToastStorage_CompressionDefault_Lz4Pass(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "events", "pg_toast.pg_toast_34562", 5*check.GiB, 2*check.GiB, 7*check.GiB, 28.0)
	row.ColumnCompressionInfo = []string{"payload:lz4:EXTENDED:jsonb"}

	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "lz4"}
	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	f := findingByID(report, findingIDCompressionDefault)
	require.NotNil(t, f)
	require.Equal(t, check.SeverityPass, f.Severity)
	require.Contains(t, f.Details, "default_toast_compression is lz4")
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

func Test_ToastStorage_DebugLabelsExternalStorage(t *testing.T) {
	t.Parallel()

	row := makeToastRow("public", "blobs", "pg_toast.pg_toast_555", 10*check.GiB, 2*check.GiB, 12*check.GiB, 20.0)
	row.ColumnCompressionInfo = []string{"payload:default:EXTERNAL:bytea"}
	queryer := &mockQueryer{rows: []db.ToastStorageRow{row}, defaultComp: "pglz"}

	report, err := toaststorage.New(queryer).Check(pg14Ctx())

	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	var debug string
	for _, f := range report.Results {
		if f.ID == "compression-algorithm" {
			debug = f.Debug
		}
	}
	require.Contains(t, debug, "external")
	require.NotContains(t, debug, "effective pglz")
}
