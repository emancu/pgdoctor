package vacuumsettings_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/vacuumsettings"
	"github.com/emancu/pgdoctor/db"
)

type mockVacuumSettingsQueries struct {
	rows []db.VacuumSettingsRow
}

func (m *mockVacuumSettingsQueries) VacuumSettings(context.Context) ([]db.VacuumSettingsRow, error) {
	return m.rows, nil
}

// ExpectedResult represents an expected test result.
type ExpectedResult struct {
	ID       string
	Severity check.Severity
}

// Helper to check if a result exists.
func hasResult(results []check.Finding, id string, severity check.Severity) bool {
	for _, result := range results {
		if result.ID == id && result.Severity == severity {
			return true
		}
	}
	return false
}

func vacuumRow(name, setting string) db.VacuumSettingsRow {
	return db.VacuumSettingsRow{
		Name:    pgtype.Text{String: name, Valid: true},
		Setting: pgtype.Text{String: setting, Valid: true},
		Unit:    pgtype.Text{String: "", Valid: false},
	}
}

func mapToVacuumSettingsRows(settings map[string]string) []db.VacuumSettingsRow {
	rows := make([]db.VacuumSettingsRow, 0, len(settings))
	for name, setting := range settings {
		rows = append(rows, vacuumRow(name, setting))
	}
	return rows
}

func optimalVacuumSettings() map[string]string {
	return map[string]string{
		"autovacuum_analyze_scale_factor": "0.05",
		"autovacuum_vacuum_scale_factor":  "0.1",
		"autovacuum_max_workers":          "4",
		"maintenance_work_mem":            "131072", // 128MB (128MB × 4 workers = 512MB = 6.25% of 8GB RAM)
		"vacuum_cost_delay":               "5",
		"vacuum_cost_limit":               "300",
		"work_mem":                        "16384", // 16MB (16MB × 100 connections = 1600MB = 19.5% of 8GB RAM)
		"max_connections":                 "100",
		"active_connections":              "10",
	}
}

func overrideOptimalWith(name string, value string) []db.VacuumSettingsRow {
	settings := optimalVacuumSettings()
	settings[name] = value
	return mapToVacuumSettingsRows(settings)
}

/*
 * Tests
 */

// mockMetadata returns a t4g.large instance (2 vCPU, 8GB RAM)
// This instance size makes the "optimal" test settings actually safe.
func mockMetadata() *check.InstanceMetadata {
	return &check.InstanceMetadata{
		InstanceClass: "db.t4g.large",
		VCPUCores:     2,
		MemoryGB:      8,
	}
}

func Test_VacuumSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		Name     string
		Rows     []db.VacuumSettingsRow
		Expected []ExpectedResult
	}{
		{
			Name: "optimal values",
			Rows: mapToVacuumSettingsRows(optimalVacuumSettings()),
			Expected: []ExpectedResult{
				{"vacuum-settings", check.SeverityOK},
			},
		},
		// Scale factor tests
		{
			Name: "autovacuum_analyze_scale_factor too high",
			Rows: overrideOptimalWith("autovacuum_analyze_scale_factor", "0.2"),
			Expected: []ExpectedResult{
				{"autovacuum_analyze_scale_factor", check.SeverityWarn},
			},
		},
		{
			Name: "autovacuum_analyze_scale_factor too low",
			Rows: overrideOptimalWith("autovacuum_analyze_scale_factor", "0.005"),
			Expected: []ExpectedResult{
				{"autovacuum_analyze_scale_factor", check.SeverityWarn},
			},
		},
		{
			Name: "autovacuum_vacuum_scale_factor too high",
			Rows: overrideOptimalWith("autovacuum_vacuum_scale_factor", "0.3"),
			Expected: []ExpectedResult{
				{"autovacuum_vacuum_scale_factor", check.SeverityWarn},
			},
		},
		{
			Name: "autovacuum_vacuum_scale_factor too low",
			Rows: overrideOptimalWith("autovacuum_vacuum_scale_factor", "0.01"),
			Expected: []ExpectedResult{
				{"autovacuum_vacuum_scale_factor", check.SeverityWarn},
			},
		},
		// Worker tests
		{
			Name: "autovacuum_max_workers too low",
			Rows: overrideOptimalWith("autovacuum_max_workers", "1"),
			Expected: []ExpectedResult{
				{"autovacuum_max_workers", check.SeverityWarn},
			},
		},
		{
			Name: "autovacuum_max_workers too high",
			Rows: overrideOptimalWith("autovacuum_max_workers", "15"), // 128MB × 15 workers = 1920MB (23.4% of 8GB RAM)
			Expected: []ExpectedResult{
				{"autovacuum_max_workers", check.SeverityWarn}, // Too many workers
				{"maintenance_work_mem", check.SeverityWarn},   // High RAM budget (> 12.5%)
			},
		},
		// Memory tests
		{
			Name: "maintenance_work_mem too low",
			Rows: overrideOptimalWith("maintenance_work_mem", "16384"), // 16MB (below 32MB threshold)
			Expected: []ExpectedResult{
				{"maintenance_work_mem", check.SeverityWarn},
			},
		},
		{
			Name: "maintenance_work_mem very high",
			Rows: overrideOptimalWith("maintenance_work_mem", "10485760"), // 10GB
			Expected: []ExpectedResult{
				{"maintenance_work_mem", check.SeverityWarn},
			},
		},
		{
			Name: "work_mem too low",
			Rows: overrideOptimalWith("work_mem", "2048"), // 2MB (below 4MB threshold)
			Expected: []ExpectedResult{
				{"work_mem", check.SeverityFail},
			},
		},
		{
			Name: "work_mem very high",
			Rows: overrideOptimalWith("work_mem", "2097152"), // 2GB (2048MB × 100 connections = 204800MB = 2500% of 8GB RAM!)
			Expected: []ExpectedResult{
				{"work_mem", check.SeverityFail}, // Correctly fails due to dangerous RAM budget
			},
		},
		// Cost settings tests
		{
			Name: "vacuum_cost_delay too high",
			Rows: overrideOptimalWith("vacuum_cost_delay", "50"),
			Expected: []ExpectedResult{
				{"vacuum_cost_delay", check.SeverityWarn},
			},
		},
		{
			Name: "vacuum_cost_limit too low",
			Rows: overrideOptimalWith("vacuum_cost_limit", "100"),
			Expected: []ExpectedResult{
				{"vacuum_cost_limit", check.SeverityWarn},
			},
		},
		{
			Name: "vacuum_cost_limit very high",
			Rows: overrideOptimalWith("vacuum_cost_limit", "15000"),
			Expected: []ExpectedResult{
				{"vacuum_cost_limit", check.SeverityWarn},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockVacuumSettingsQueries{rows: tt.Rows}
			checker := vacuumsettings.New(queryer)

			// Add metadata to context
			ctx := check.ContextWithInstanceMetadata(context.Background(), mockMetadata())
			report, err := checker.Check(ctx)
			require.NoError(t, err)

			results := report.Results
			require.Len(t, results, len(tt.Expected))

			for _, expected := range tt.Expected {
				require.True(t, hasResult(results, expected.ID, expected.Severity))
			}
		})
	}
}

func Test_VacuumSettings_MultipleIssues(t *testing.T) {
	t.Parallel()

	// Create settings with multiple problems
	settings := map[string]string{
		"autovacuum_analyze_scale_factor": "0.3",   // too high (> 0.1)
		"autovacuum_vacuum_scale_factor":  "0.01",  // too low (< 0.02)
		"autovacuum_max_workers":          "1",     // too low (= 1)
		"maintenance_work_mem":            "16384", // too low (16MB < 32MB)
		"vacuum_cost_delay":               "50",    // too high (> 20)
		"vacuum_cost_limit":               "100",   // too low (< 200)
		"work_mem":                        "2048",  // too low (2MB < 4MB)
		"max_connections":                 "100",
		"active_connections":              "10",
	}

	queryer := &mockVacuumSettingsQueries{rows: mapToVacuumSettingsRows(settings)}
	checker := vacuumsettings.New(queryer)

	// Add metadata to context
	ctx := check.ContextWithInstanceMetadata(context.Background(), mockMetadata())
	report, err := checker.Check(ctx)
	require.NoError(t, err)

	results := report.Results

	// Should detect all 7 issues
	require.Equal(t, len(results), 7, "Should detect all configuration issues")
}

func Test_VacuumSettings_DefaultsUsedOnParseError(t *testing.T) {
	t.Parallel()

	// Invalid values should fall back to PostgreSQL defaults
	settings := overrideOptimalWith("autovacuum_vacuum_scale_factor", "invalid")

	queryer := &mockVacuumSettingsQueries{rows: settings}
	checker := vacuumsettings.New(queryer)

	// Add metadata to context
	ctx := check.ContextWithInstanceMetadata(context.Background(), mockMetadata())

	// Should not panic - uses defaults
	var report *check.Report
	var err error
	require.NotPanics(t, func() {
		report, err = checker.Check(ctx)
		require.NoError(t, err)
	})

	results := report.Results
	// Defaults (0.1 and 0.2) are acceptable, so should get OK
	require.Equal(t, 1, len(results))
	require.Equal(t, "vacuum-settings", results[0].ID)
	require.Equal(t, "PostgreSQL Vacuum & Maintenance Configs", results[0].Name)
	require.Equal(t, check.SeverityOK, results[0].Severity)
}
