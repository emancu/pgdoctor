package pgdoctor

import (
	"context"
	"fmt"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
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
		New: func(_ db.DBTX, _ check.Config) check.Checker {
			return &fakeChecker{metadata: meta, report: report, err: err}
		},
	}
}

// reportWithFinding builds a report carrying one finding, standing in for the
// work a check completed before it failed.
func reportWithFinding(id string, severity check.Severity) *check.Report {
	report := check.NewReport(check.Metadata{CheckID: id, Name: id, Category: check.CategoryConfigs})
	report.AddFinding(check.Finding{ID: "partial", Name: "Partial", Severity: severity})

	return report
}

// A check that needs an absent extension reports it as an error, and the runner
// turns that into a uniform SKIP finding — no check assigns SeveritySkip itself.
func TestRun_MissingExtensionSkipsCheck(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("query pattern analysis: %w",
		&check.MissingExtensionError{Extension: "pg_stat_statements"})

	var reports []*check.Report
	Run(context.Background(), nil, Options{
		Checks:   []check.Package{fakePackage("needs-ext", check.CategoryPerformance, nil, err)},
		OnReport: Collect(&reports),
	})
	require.Len(t, reports, 1)

	assert.Equal(t, check.SeveritySkip, reports[0].Severity)
	require.Len(t, reports[0].Results, 1)
	assert.Equal(t, "extension-unavailable", reports[0].Results[0].ID)
	assert.Equal(t, "Extension Unavailable", reports[0].Results[0].Name)
	assert.Equal(t, check.SeveritySkip, reports[0].Results[0].Severity)
	assert.Contains(t, reports[0].Results[0].Details, "pg_stat_statements is not installed")
}

// A check may return findings alongside its error, for subchecks that completed
// before the failure. Those are kept, and the SKIP is recorded as one more
// finding without lowering the report's severity.
func TestRun_KeepsPartialFindingsOnError(t *testing.T) {
	t.Parallel()

	partial := reportWithFinding("partly-ran", check.SeverityWarn)
	err := &check.MissingExtensionError{Extension: "pg_stat_statements"}

	var reports []*check.Report
	Run(context.Background(), nil, Options{
		Checks:   []check.Package{fakePackage("partly-ran", check.CategoryPerformance, partial, err)},
		OnReport: Collect(&reports),
	})
	require.Len(t, reports, 1)

	assert.Equal(t, check.SeverityWarn, reports[0].Severity, "the SKIP must not mask a real finding")
	require.Len(t, reports[0].Results, 2)
	assert.Equal(t, "partial", reports[0].Results[0].ID)
	assert.Equal(t, "extension-unavailable", reports[0].Results[1].ID)
	assert.Equal(t, check.SeveritySkip, reports[0].Results[1].Severity)
}

// An empty report is nothing to preserve, so the check skips wholesale.
func TestRun_EmptyPartialReportSkipsWholesale(t *testing.T) {
	t.Parallel()

	empty := check.NewReport(check.Metadata{CheckID: "empty", Name: "empty", Category: check.CategoryConfigs})

	var reports []*check.Report
	Run(context.Background(), nil, Options{
		Checks:   []check.Package{fakePackage("empty", check.CategoryConfigs, empty, fmt.Errorf("boom"))},
		OnReport: Collect(&reports),
	})
	require.Len(t, reports, 1)

	assert.Equal(t, check.SeveritySkip, reports[0].Severity)
	require.Len(t, reports[0].Results, 1)
	assert.Equal(t, "error", reports[0].Results[0].ID)
}

// ctxChecker records the context its Check received.
type ctxChecker struct {
	metadata check.Metadata
	seen     context.Context
}

func (c *ctxChecker) Metadata() check.Metadata { return c.metadata }

func (c *ctxChecker) Check(ctx context.Context) (*check.Report, error) {
	c.seen = ctx
	report := check.NewReport(c.metadata)
	report.AddFinding(check.Finding{ID: "ok", Name: "OK", Severity: check.SeverityPass})

	return report, nil
}

// A consumer that discovered extensions itself keeps its set; the runner does
// not overwrite it.
func TestRun_KeepsConsumerSuppliedExtensions(t *testing.T) {
	t.Parallel()

	meta := check.Metadata{CheckID: "reads-ctx", Name: "reads-ctx", Category: check.CategoryConfigs}
	checker := &ctxChecker{metadata: meta}

	extensions := check.Extensions{"pg_buffercache": "1.5"}
	ctx := check.ContextWithExtensions(context.Background(), extensions)

	Run(ctx, nil, Options{
		Checks: []check.Package{{
			Metadata: func() check.Metadata { return meta },
			New:      func(_ db.DBTX, _ check.Config) check.Checker { return checker },
		}},
	})

	require.NotNil(t, checker.seen)
	assert.Equal(t, extensions, check.ExtensionsFromContext(checker.seen))
}

func TestRun_ContinuesAfterStatementTimeout(t *testing.T) {
	t.Parallel()

	// Simulate a PostgreSQL statement_timeout error (SQLSTATE 57014)
	pgErr := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}

	fastReport := check.NewReport(check.Metadata{CheckID: "fast-check", Name: "Fast", Category: check.CategoryConfigs})
	fastReport.AddFinding(check.Finding{ID: "ok", Name: "OK", Severity: check.SeverityPass, Details: "all good"})

	var reports []*check.Report
	Run(context.Background(), nil, Options{
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
	Run(context.Background(), nil, Options{
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
