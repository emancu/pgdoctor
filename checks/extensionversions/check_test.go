package extensionversions

import (
	"context"
	"testing"

	goversion "github.com/hashicorp/go-version"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/emancu/pgdoctor/internal/checktest"
)

type mockQueries struct {
	data []db.InstalledExtensionsRow
	err  error
}

func (m *mockQueries) InstalledExtensions(_ context.Context) ([]db.InstalledExtensionsRow, error) {
	return m.data, m.err
}

func findingByID(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()
	for _, f := range report.Results {
		if f.ID == id {
			return f
		}
	}
	require.Failf(t, "finding not found", "no finding with ID %q", id)
	return check.Finding{}
}

func TestExtensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     []db.InstalledExtensionsRow
		severity check.Severity
	}{
		{
			name:     "no extensions installed",
			data:     []db.InstalledExtensionsRow{},
			severity: check.SeverityPass,
		},
		{
			name: "compliant version",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "pg_partman", InstalledVersion: "5.1.0"},
			},
			severity: check.SeverityPass,
		},
		{
			name: "deprecated 5.0.x (pre-REPLICA IDENTITY inheritance)",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "pg_partman", InstalledVersion: "5.0.1"},
			},
			severity: check.SeverityWarn,
		},
		{
			name: "deprecated 4.x",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "pg_partman", InstalledVersion: "4.7.4"},
			},
			severity: check.SeverityWarn,
		},
		{
			name: "unsupported below 3.0",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "pg_partman", InstalledVersion: "2.5.0"},
			},
			severity: check.SeverityWarn,
		},
		{
			name: "unparseable installed version for policied extension",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "pg_partman", InstalledVersion: "unpackaged"},
			},
			severity: check.SeverityWarn,
		},
		{
			name: "postgis compliant",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "postgis", InstalledVersion: "3.5.1"},
			},
			severity: check.SeverityPass,
		},
		{
			name: "postgis on supported 3.4.x does not warn",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "postgis", InstalledVersion: "3.4.6"},
			},
			severity: check.SeverityPass,
		},
		{
			name: "postgis deprecated below warn floor",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "postgis", InstalledVersion: "3.2.0"},
			},
			severity: check.SeverityWarn,
		},
		{
			name: "postgis unsupported below fail floor",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "postgis", InstalledVersion: "2.5.0"},
			},
			severity: check.SeverityWarn,
		},
		{
			name: "extension with no policy",
			data: []db.InstalledExtensionsRow{
				{ExtensionName: "plpgsql", InstalledVersion: "1.0"},
			},
			severity: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queries := &mockQueries{data: tt.data}
			checker := New(queries)

			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			require.Equal(t, tt.severity, report.Severity)
			checktest.AssertSeverityInvariant(t, report)

			support := findingByID(t, report, "version-support")
			require.Equal(t, tt.severity, support.Severity)
		})
	}
}

func TestExtensionsMixedSetReportsMaxSeverity(t *testing.T) {
	t.Parallel()

	queries := &mockQueries{data: []db.InstalledExtensionsRow{
		{ExtensionName: "plpgsql", InstalledVersion: "1.0"},
		{ExtensionName: "pg_partman", InstalledVersion: "2.6.1"},
		{ExtensionName: "postgis", InstalledVersion: "3.4.2"},
	}}

	report, err := New(queries).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	require.Equal(t, check.SeverityWarn, report.Severity)

	finding := findingByID(t, report, "version-support")
	require.Equal(t, check.SeverityWarn, finding.Severity)
	require.NotNil(t, finding.Table)

	var violatingRow *check.TableRow
	for i := range finding.Table.Rows {
		if finding.Table.Rows[i].Cells[0] == "pg_partman" {
			violatingRow = &finding.Table.Rows[i]
			break
		}
	}
	require.NotNil(t, violatingRow, "expected pg_partman row in finding table")
	require.Equal(t, check.SeverityWarn, violatingRow.Severity)
	require.Equal(t, "2.6.1", violatingRow.Cells[1])

	// Failing rows must sort ahead of OK rows.
	require.Equal(t, "pg_partman", finding.Table.Rows[0].Cells[0])
}

func TestSupportFindingTableOnlyFlaggedRows(t *testing.T) {
	t.Parallel()

	t.Run("table lists only flagged extensions, not the OK inventory", func(t *testing.T) {
		t.Parallel()

		data := []db.InstalledExtensionsRow{
			{ExtensionName: "plpgsql", InstalledVersion: "1.0"},      // no policy -> OK
			{ExtensionName: "pg_partman", InstalledVersion: "4.7.4"}, // deprecated -> WARN
		}

		report, err := New(&mockQueries{data: data}).Check(context.Background())
		require.NoError(t, err)
		checktest.AssertSeverityInvariant(t, report)

		support := findingByID(t, report, "version-support")
		require.NotNil(t, support.Table)
		require.Len(t, support.Table.Rows, 1, "OK rows are excluded from the table")
		require.Equal(t, "pg_partman", support.Table.Rows[0].Cells[0])
	})

	t.Run("a clean run carries no table", func(t *testing.T) {
		t.Parallel()

		data := []db.InstalledExtensionsRow{
			{ExtensionName: "plpgsql", InstalledVersion: "1.0"},
			{ExtensionName: "pg_partman", InstalledVersion: "5.1.0"},
		}

		report, err := New(&mockQueries{data: data}).Check(context.Background())
		require.NoError(t, err)
		checktest.AssertSeverityInvariant(t, report)

		support := findingByID(t, report, "version-support")
		require.Equal(t, check.SeverityPass, support.Severity)
		require.Nil(t, support.Table, "no table when every extension is supported")
	})
}

func TestPendingUpdateFinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        []db.InstalledExtensionsRow
		severity    check.Severity
		wantRows    int
		wantCells   []string // expected cells of the single flagged row, when wantRows == 1
		wantDetails string   // when wantRows > 0
	}{
		{
			name:     "no rows",
			data:     []db.InstalledExtensionsRow{},
			severity: check.SeverityPass,
			wantRows: 0,
		},
		{
			name: "installed behind default warns",
			data: []db.InstalledExtensionsRow{
				{
					ExtensionName:    "pg_stat_statements",
					InstalledVersion: "1.11",
					DefaultVersion:   pgtype.Text{String: "1.12", Valid: true},
					ServerVersionNum: 170004,
				},
			},
			severity:    check.SeverityInfo,
			wantRows:    1,
			wantCells:   []string{"pg_stat_statements", "1.11", "1.12"},
			wantDetails: "1 extension(s) behind the version bundled with PostgreSQL 17",
		},
		{
			name: "installed ahead of default is skipped",
			data: []db.InstalledExtensionsRow{
				{
					ExtensionName:    "pg_stat_statements",
					InstalledVersion: "1.12",
					DefaultVersion:   pgtype.Text{String: "1.11", Valid: true},
					ServerVersionNum: 170004,
				},
			},
			severity: check.SeverityPass,
			wantRows: 0,
		},
		{
			name: "installed equal to default is skipped",
			data: []db.InstalledExtensionsRow{
				{
					ExtensionName:    "pg_stat_statements",
					InstalledVersion: "1.12",
					DefaultVersion:   pgtype.Text{String: "1.12", Valid: true},
					ServerVersionNum: 170004,
				},
			},
			severity: check.SeverityPass,
			wantRows: 0,
		},
		{
			name: "null default version is skipped",
			data: []db.InstalledExtensionsRow{
				{
					ExtensionName:    "pg_stat_statements",
					InstalledVersion: "1.11",
					DefaultVersion:   pgtype.Text{Valid: false},
					ServerVersionNum: 170004,
				},
			},
			severity: check.SeverityPass,
			wantRows: 0,
		},
		{
			name: "unparseable installed warns for manual review",
			data: []db.InstalledExtensionsRow{
				{
					ExtensionName:    "pg_stat_statements",
					InstalledVersion: "unpackaged",
					DefaultVersion:   pgtype.Text{String: "1.12", Valid: true},
					ServerVersionNum: 170004,
				},
			},
			severity:    check.SeverityInfo,
			wantRows:    1,
			wantCells:   []string{"pg_stat_statements", "unpackaged", "1.12"},
			wantDetails: "1 extension(s) behind the version bundled with PostgreSQL 17",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queries := &mockQueries{data: tt.data}
			report, err := New(queries).Check(context.Background())
			require.NoError(t, err)
			checktest.AssertSeverityInvariant(t, report)

			finding := findingByID(t, report, "pending-update")
			require.Equal(t, tt.severity, finding.Severity)

			if tt.wantRows == 0 {
				require.Nil(t, finding.Table)
				require.Equal(t, "All extensions are at the bundled default version", finding.Details)
				return
			}

			require.NotNil(t, finding.Table)
			require.Len(t, finding.Table.Rows, tt.wantRows)
			require.Equal(t, []string{"Extension", "Installed", "Available"}, finding.Table.Headers)
			require.Equal(t, tt.wantDetails, finding.Details)
			if tt.wantCells != nil {
				require.Equal(t, tt.wantCells, finding.Table.Rows[0].Cells)
				require.Equal(t, check.SeverityInfo, finding.Table.Rows[0].Severity)
			}
		})
	}
}

func TestClassifyPendingUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		installed  string
		defaultVer string
		wantOK     bool
		wantStatus string
	}{
		{
			name:       "behind default",
			installed:  "1.11",
			defaultVer: "1.12",
			wantOK:     true,
			wantStatus: "behind bundled default",
		},
		{
			name:       "equal version skipped",
			installed:  "1.12",
			defaultVer: "1.12",
			wantOK:     false,
		},
		{
			name:       "ahead of default skipped",
			installed:  "1.12",
			defaultVer: "1.11",
			wantOK:     false,
		},
		{
			name:       "unparseable installed",
			installed:  "unpackaged",
			defaultVer: "1.12",
			wantOK:     true,
			wantStatus: "version unparseable — manual review",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, ok := classifyPendingUpdate(tt.installed, tt.defaultVer)
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.Equal(t, tt.wantStatus, status)
			}
		})
	}
}

func TestBothFindingsAlwaysEmitted(t *testing.T) {
	t.Parallel()

	report, err := New(&mockQueries{}).Check(context.Background())
	require.NoError(t, err)
	checktest.AssertSeverityInvariant(t, report)

	require.Len(t, report.Results, 2)

	support := findingByID(t, report, "version-support")
	require.Equal(t, check.SeverityPass, support.Severity)
	require.Equal(t, "No extensions installed", support.Details)

	pending := findingByID(t, report, "pending-update")
	require.Equal(t, check.SeverityPass, pending.Severity)
	require.Equal(t, "All extensions are at the bundled default version", pending.Details)
}

func TestExtensionPoliciesValid(t *testing.T) {
	t.Parallel()

	for _, p := range ExtensionPolicies {
		t.Run(p.Name, func(t *testing.T) {
			t.Parallel()

			require.NotEmpty(t, p.Name, "policy Name must be set")
			require.True(t, p.WarnBelow != "" || p.FailBelow != "",
				"policy must set at least one of WarnBelow/FailBelow")

			var warn, fail *goversion.Version
			if p.WarnBelow != "" {
				v, err := goversion.NewVersion(p.WarnBelow)
				require.NoError(t, err, "WarnBelow must parse")
				warn = v
			}
			if p.FailBelow != "" {
				v, err := goversion.NewVersion(p.FailBelow)
				require.NoError(t, err, "FailBelow must parse")
				fail = v
			}

			if warn != nil && fail != nil {
				require.True(t, fail.LessThanOrEqual(warn),
					"FailBelow (%s) must be <= WarnBelow (%s)", p.FailBelow, p.WarnBelow)
			}
		})
	}
}
