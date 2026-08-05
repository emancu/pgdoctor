package tableactivity_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tableactivity"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type mockQueryer struct {
	rows []db.TableActivityRow
	err  error
}

func (m *mockQueryer) TableActivity(context.Context) ([]db.TableActivityRow, error) {
	return m.rows, m.err
}

func pgInt8(i int64) pgtype.Int8  { return pgtype.Int8{Int64: i, Valid: true} }
func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

type activity struct {
	schema, table         string
	ins, upd, del         int64
	hotUpd, liveTup, size int64
}

func mkRow(a activity) db.TableActivityRow {
	return db.TableActivityRow{
		Schemaname:     pgText(a.schema),
		Relname:        pgText(a.table),
		NTupIns:        pgInt8(a.ins),
		NTupUpd:        pgInt8(a.upd),
		NTupDel:        pgInt8(a.del),
		NTupHotUpd:     pgInt8(a.hotUpd),
		NLiveTup:       pgInt8(a.liveTup),
		TableSizeBytes: pgInt8(a.size),
	}
}

func runCheck(t *testing.T, rows []db.TableActivityRow) *check.Report {
	t.Helper()

	report, err := tableactivity.New(&mockQueryer{rows: rows}).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)
	return report
}

func finding(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()

	for _, f := range report.Results {
		if f.ID == id {
			return f
		}
	}
	require.Failf(t, "missing finding", "no finding with id %q", id)
	return check.Finding{}
}

func Test_Metadata(t *testing.T) {
	t.Parallel()

	m := tableactivity.Metadata()
	require.Equal(t, "table-activity", m.CheckID)
	require.Equal(t, check.CategoryPerformance, m.Category)
	require.NotEmpty(t, m.Description)
	require.NotEmpty(t, m.SQL)
	require.NotEmpty(t, m.Readme)
}

func Test_QueryError(t *testing.T) {
	t.Parallel()

	_, err := tableactivity.New(&mockQueryer{err: fmt.Errorf("connection refused")}).Check(context.Background())
	require.ErrorContains(t, err, "table-activity")
}

func Test_NoActivity_Pass(t *testing.T) {
	t.Parallel()

	report := runCheck(t, nil)
	require.Len(t, report.Results, 1)
	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_HighChurn_Informational(t *testing.T) {
	t.Parallel()

	// >1M writes; too few live rows for the HOT finding to fire.
	report := runCheck(t, []db.TableActivityRow{
		mkRow(activity{schema: "public", table: "events", ins: 2_000_000, liveTup: 500_000}),
	})

	high := finding(t, report, "high-churn-tables")
	require.Equal(t, check.SeverityInfo, high.Severity)
	require.Len(t, high.Table.Rows, 1)
	require.Equal(t, check.SeverityInfo, high.Table.Rows[0].Severity)

	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_LowHOT_Informational(t *testing.T) {
	t.Parallel()

	// >1M live rows, enough updates, 1% HOT ratio; total writes stay under the churn threshold.
	report := runCheck(t, []db.TableActivityRow{
		mkRow(activity{schema: "public", table: "orders", upd: 100_000, hotUpd: 1_000, liveTup: 2_000_000}),
	})

	low := finding(t, report, "low-hot-ratio")
	require.Equal(t, check.SeverityInfo, low.Severity)
	require.Len(t, low.Table.Rows, 1)
	require.Equal(t, check.SeverityInfo, low.Table.Rows[0].Severity)

	require.Equal(t, check.SeverityPass, report.Severity)
}

func Test_BothFindings_NeverEscalate(t *testing.T) {
	t.Parallel()

	report := runCheck(t, []db.TableActivityRow{
		mkRow(activity{schema: "public", table: "big", ins: 2_000_000, upd: 100_000, hotUpd: 1_000, liveTup: 2_000_000}),
	})

	require.Equal(t, check.SeverityInfo, finding(t, report, "high-churn-tables").Severity)
	require.Equal(t, check.SeverityInfo, finding(t, report, "low-hot-ratio").Severity)
	require.Equal(t, check.SeverityPass, report.Severity)
}
