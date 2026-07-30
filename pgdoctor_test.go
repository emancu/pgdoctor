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
