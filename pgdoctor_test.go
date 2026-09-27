package pgdoctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/replicationlag"
	"github.com/emancu/pgdoctor/checks/sessionsettings"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		filters       []string
		expectedValid []string
		expectedInval []string
	}{
		{
			name:          "valid check ID",
			filters:       []string{"pg-version"},
			expectedValid: []string{"pg-version"},
			expectedInval: nil,
		},
		{
			name:          "valid category",
			filters:       []string{"configs"},
			expectedValid: []string{"configs"},
			expectedInval: nil,
		},
		{
			name:          "subcheck ID extracts check ID",
			filters:       []string{"connection-efficiency/sessions-fatal"},
			expectedValid: []string{"connection-efficiency"},
			expectedInval: nil,
		},
		{
			name:          "invalid filter",
			filters:       []string{"nonexistent-check"},
			expectedValid: nil,
			expectedInval: []string{"nonexistent-check"},
		},
		{
			name:          "mixed valid and invalid",
			filters:       []string{"pg-version", "invalid-check", "connection-efficiency/subcheck"},
			expectedValid: []string{"pg-version", "connection-efficiency"},
			expectedInval: []string{"invalid-check"},
		},
		{
			name:          "duplicate filters after normalization",
			filters:       []string{"connection-efficiency", "connection-efficiency/sessions-fatal"},
			expectedValid: []string{"connection-efficiency"},
			expectedInval: nil,
		},
		{
			name:          "multiple subchecks same check",
			filters:       []string{"connection-efficiency/sessions-fatal", "connection-efficiency/sessions-idle"},
			expectedValid: []string{"connection-efficiency"},
			expectedInval: nil,
		},
		{
			name:          "category-qualified check ID from list output",
			filters:       []string{"configs/pg-version", "configs/connection-efficiency/sessions-fatal"},
			expectedValid: []string{"pg-version", "connection-efficiency"},
			expectedInval: nil,
		},
		{
			name:          "category-qualified check ID in the wrong category",
			filters:       []string{"vacuum/pg-version", "configs/nonexistent-check"},
			expectedValid: nil,
			expectedInval: []string{"vacuum/pg-version", "configs/nonexistent-check"},
		},
		{
			name:          "category and check from same category",
			filters:       []string{"configs", "pg-version"},
			expectedValid: []string{"configs", "pg-version"},
			expectedInval: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			valid, invalid := ValidateFilters(AllChecks(), tt.filters)

			assert.ElementsMatch(t, tt.expectedValid, valid, "valid filters should match")
			assert.ElementsMatch(t, tt.expectedInval, invalid, "invalid filters should match")
		})
	}
}

func TestAllChecks_RegistersDecodeConfig(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("checks/*/*.go")
	require.NoError(t, err)

	exported := regexp.MustCompile(`(?m)^func DefaultConfig\(`)
	var want []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		require.NoError(t, err)
		if exported.Match(src) {
			want = append(want, "github.com/emancu/pgdoctor/checks/"+filepath.Base(filepath.Dir(file))+".Metadata")
		}
	}
	require.NotEmpty(t, want)

	var got []string
	for _, pkg := range AllChecks() {
		if pkg.DecodeConfig != nil {
			got = append(got, runtime.FuncForPC(reflect.ValueOf(pkg.Metadata).Pointer()).Name())
		}
	}

	assert.ElementsMatch(t, want, got)
}

func findPackage(t *testing.T, checkID string) check.Package {
	t.Helper()
	for _, pkg := range AllChecks() {
		if pkg.Metadata().CheckID == checkID {
			return pkg
		}
	}
	t.Fatalf("no check %q", checkID)
	return check.Package{}
}

func TestAllChecks_New(t *testing.T) {
	t.Parallel()

	for _, pkg := range AllChecks() {
		_, err := pkg.New(nil, nil)
		require.NoError(t, err, pkg.Metadata().CheckID)
	}

	tests := []struct {
		name    string
		checkID string
		cfg     check.Config
		wantErr string
	}{
		{"typed config", "session-settings", check.Config{"session-settings": sessionsettings.Config{Timeout: 2000}}, ""},
		{"invalid value", "session-settings", check.Config{"session-settings": sessionsettings.Config{}}, "invalid config: timeout"},
		{"wrong type", "session-settings", check.Config{"session-settings": replicationlag.DefaultConfig()}, "want sessionsettings.Config"},
		{"pointer", "session-settings", check.Config{"session-settings": &sessionsettings.Config{Timeout: 2000}}, "want sessionsettings.Config"},
		{"check without settings", "pg-version", check.Config{"pg-version": struct{}{}}, "accepts no settings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := findPackage(t, tt.checkID).New(nil, tt.cfg)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestAllChecks_DecodeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		checkID string
		yaml    string
		want    any
		wantErr string
	}{
		{"empty", "session-settings", "", sessionsettings.DefaultConfig(), ""},
		{"null", "session-settings", "null", sessionsettings.DefaultConfig(), ""},
		{
			"lists and maps", "session-settings", "ignore_roles: [dba_ro, migrations]\ntimeout_by_role: {dba_ro: 300000}",
			sessionsettings.Config{IgnoreRoles: []string{"dba_ro", "migrations"}, Timeout: 5000, TimeoutByRole: map[string]int64{"dba_ro": 300000}}, "",
		},
		{
			"nested map", "replication-lag", "physical_lag_by_application: {delayed: {warn_seconds: 305, fail_seconds: 360}}",
			replicationlag.Config{PhysicalLagWarnSeconds: 5, PhysicalLagFailSeconds: 60, PhysicalLagByApplication: map[string]replicationlag.LagThresholds{"delayed": {WarnSeconds: 305, FailSeconds: 360}}}, "",
		},
		{"replica without warn", "replication-lag", "physical_lag_by_application: {delayed: {fail_seconds: 360}}", nil, "physical_lag_by_application.delayed"},
		{"unknown key", "session-settings", "timeuot: 2000", nil, "field timeuot not found"},
		{"unknown nested key", "replication-lag", "physical_lag_by_application: {delayed: {fial_seconds: 360}}", nil, "field fial_seconds not found"},
		{"comma string for a list", "table-vacuum-health", "ignore_tables: public.a,public.b", nil, "cannot unmarshal"},
		{"not a number", "connection-health", "long_idle_warn_count: many", nil, "cannot unmarshal"},
		{"validation", "pk-types", "usage_warn_percent: 95", nil, "must be lower than usage_fail_percent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := findPackage(t, tt.checkID).DecodeConfig([]byte(tt.yaml))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRun_InvalidConfigSkipsCheck(t *testing.T) {
	t.Parallel()

	var reports []*check.Report
	Run(context.Background(), &fakeDB{versionNum: 170004}, Options{
		Checks:   Filter(AllChecks(), []string{"session-settings"}, nil),
		Config:   check.Config{"session-settings": sessionsettings.Config{Timeout: -1}},
		OnReport: Collect(&reports),
	})
	require.Len(t, reports, 1)

	assert.Equal(t, "session-settings", reports[0].CheckID)
	assert.Equal(t, check.SeveritySkip, reports[0].Severity)
	require.Len(t, reports[0].Results, 1)
	assert.Contains(t, reports[0].Results[0].Details, "invalid config: timeout")
}

// fakeChecker is a test double that implements check.Checker.
type fakeChecker struct {
	metadata check.Metadata
	report   *check.Report
	err      error
}

func (f *fakeChecker) Metadata() check.Metadata { return f.metadata }

func (f *fakeChecker) Check(_ context.Context) (*check.Report, error) {
	return f.report, f.err
}

func fakePackage(id string, category check.Category, report *check.Report, err error) check.Package {
	meta := check.Metadata{CheckID: id, Name: id, Category: category}
	return check.Package{
		Metadata: func() check.Metadata { return meta },
		New: func(_ db.DBTX, _ check.Config) (check.Checker, error) {
			return &fakeChecker{metadata: meta, report: report, err: err}, nil
		},
	}
}

func TestRun_ContinuesAfterStatementTimeout(t *testing.T) {
	t.Parallel()

	// Simulate a PostgreSQL statement_timeout error (SQLSTATE 57014)
	pgErr := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}

	fastReport := check.NewReport(check.Metadata{CheckID: "fast-check", Name: "Fast", Category: check.CategoryConfigs})
	fastReport.AddFinding(check.Finding{ID: "ok", Name: "OK", Severity: check.SeverityPass, Details: "all good"})

	var reports []*check.Report
	Run(context.Background(), &fakeDB{versionNum: 170004}, Options{
		Checks: []check.Package{
			fakePackage("slow-check", check.CategoryConfigs, nil, pgErr),
			fakePackage("fast-check", check.CategoryConfigs, fastReport, nil),
		},
		OnReport: Collect(&reports),
	})
	require.Len(t, reports, 2)

	assert.Equal(t, check.SeveritySkip, reports[0].Severity)
	assert.Equal(t, "slow-check", reports[0].CheckID)
	require.Len(t, reports[0].Results, 1)
	assert.Contains(t, reports[0].Results[0].Details, "statement_timeout")

	assert.Equal(t, check.SeverityPass, reports[1].Severity)
	assert.Equal(t, "fast-check", reports[1].CheckID)
}

func TestRun_ContinuesAfterCheckError(t *testing.T) {
	t.Parallel()

	goodReport := check.NewReport(check.Metadata{CheckID: "good-check", Name: "Good", Category: check.CategoryConfigs})
	goodReport.AddFinding(check.Finding{ID: "ok", Name: "OK", Severity: check.SeverityPass})

	var reports []*check.Report
	Run(context.Background(), &fakeDB{versionNum: 170004}, Options{
		Checks: []check.Package{
			fakePackage("broken-check", check.CategoryConfigs, nil, fmt.Errorf("connection refused")),
			fakePackage("good-check", check.CategoryConfigs, goodReport, nil),
		},
		OnReport: Collect(&reports),
	})
	require.Len(t, reports, 2)

	assert.Equal(t, check.SeveritySkip, reports[0].Severity)
	assert.Equal(t, "broken-check", reports[0].CheckID)
	require.Len(t, reports[0].Results, 1)
	assert.Contains(t, reports[0].Results[0].Details, "connection refused")

	assert.Equal(t, check.SeverityPass, reports[1].Severity)
	assert.Equal(t, "good-check", reports[1].CheckID)
}

type fakeDB struct {
	versionNum int32
	err        error
	queries    int
}

func (f *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	f.queries++
	return fakeRow{versionNum: f.versionNum, err: f.err}
}

type fakeRow struct {
	versionNum int32
	err        error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*int32) = r.versionNum / 10000
	*dest[1].(*int32) = r.versionNum % 100
	return nil
}

type metadataCapture struct {
	seen *check.InstanceMetadata
}

func (m *metadataCapture) Metadata() check.Metadata {
	return check.Metadata{CheckID: "capture", Name: "Capture", Category: check.CategoryConfigs}
}

func (m *metadataCapture) Check(ctx context.Context) (*check.Report, error) {
	m.seen = check.InstanceMetadataFromContext(ctx)
	return check.NewReport(m.Metadata()), nil
}

func TestRun_ServerVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		meta        *check.InstanceMetadata
		dbErr       error
		wantQueries int
		want        *check.InstanceMetadata
	}{
		{
			name:        "no metadata",
			meta:        nil,
			wantQueries: 1,
			want:        &check.InstanceMetadata{EngineVersion: "17.4", EngineVersionMajor: 17, EngineVersionMinor: 4},
		},
		{
			name:        "metadata without version",
			meta:        &check.InstanceMetadata{InstanceID: "db-1", VCPUCores: 4, MemoryGB: 16},
			wantQueries: 1,
			want:        &check.InstanceMetadata{InstanceID: "db-1", VCPUCores: 4, MemoryGB: 16, EngineVersion: "17.4", EngineVersionMajor: 17, EngineVersionMinor: 4},
		},
		{
			name:        "metadata with version",
			meta:        &check.InstanceMetadata{InstanceID: "db-1", EngineVersion: "15.2", EngineVersionMajor: 15, EngineVersionMinor: 2},
			wantQueries: 0,
			want:        &check.InstanceMetadata{InstanceID: "db-1", EngineVersion: "15.2", EngineVersionMajor: 15, EngineVersionMinor: 2},
		},
		{
			name:        "query error",
			meta:        nil,
			dbErr:       fmt.Errorf("permission denied"),
			wantQueries: 1,
			want:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var callerCopy check.InstanceMetadata
			if tt.meta != nil {
				callerCopy = *tt.meta
			}

			conn := &fakeDB{versionNum: 170004, err: tt.dbErr}
			capture := &metadataCapture{}
			var reports []*check.Report

			ctx := context.Background()
			if tt.meta != nil {
				ctx = check.ContextWithInstanceMetadata(ctx, tt.meta)
			}

			Run(ctx, conn, Options{
				Checks: []check.Package{{
					Metadata: capture.Metadata,
					New:      func(db.DBTX, check.Config) (check.Checker, error) { return capture, nil },
				}},
				OnReport: Collect(&reports),
			})

			require.Len(t, reports, 1)
			assert.Equal(t, tt.wantQueries, conn.queries)
			assert.Equal(t, tt.want, capture.seen)
			if tt.meta != nil {
				assert.Equal(t, callerCopy, *tt.meta, "caller metadata must not change")
			}
		})
	}
}
