package vacuumsettings_test

import (
	"context"
	"maps"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/vacuumsettings"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
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
		"work_mem":                        "16384", // 16MB (16MB × 10 backends = 160MB = 1.95% of 8GB RAM)
		"max_connections":                 "100",
		"active_connections":              "10",
	}
}

func overrideOptimalWithAll(overrides map[string]string) []db.VacuumSettingsRow {
	settings := optimalVacuumSettings()
	maps.Copy(settings, overrides)
	return mapToVacuumSettingsRows(settings)
}

func overrideOptimalWith(name string, value string) []db.VacuumSettingsRow {
	return overrideOptimalWithAll(map[string]string{name: value})
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
				{"vacuum-settings", check.SeverityPass},
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
			Rows: overrideOptimalWith("work_mem", "2097152"), // 2GB (2048MB × 10 backends = 20480MB = 250% of 8GB RAM)
			Expected: []ExpectedResult{
				{"work_mem", check.SeverityFail}, // Correctly fails due to dangerous RAM budget
			},
		},
		{
			Name: "work_mem safe behind a connection pooler",
			Rows: overrideOptimalWithAll(map[string]string{"work_mem": "49152", "max_connections": "5000"}),
			Expected: []ExpectedResult{
				{"vacuum-settings", check.SeverityPass},
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

func Test_VacuumSettings_FindingNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setting string
		value   string
		name    string
	}{
		{"autovacuum_analyze_scale_factor", "0.2", "High autovacuum_analyze_scale_factor"},
		{"autovacuum_analyze_scale_factor", "0.005", "Low autovacuum_analyze_scale_factor"},
		{"autovacuum_vacuum_scale_factor", "0.3", "High autovacuum_vacuum_scale_factor"},
		{"autovacuum_vacuum_scale_factor", "0.01", "Low autovacuum_vacuum_scale_factor"},
		{"vacuum_cost_delay", "50", "High vacuum_cost_delay"},
		{"vacuum_cost_limit", "100", "Low vacuum_cost_limit"},
		{"vacuum_cost_limit", "15000", "High vacuum_cost_limit"},
	}

	for _, tt := range tests {
		t.Run(tt.setting+"="+tt.value, func(t *testing.T) {
			t.Parallel()

			queryer := &mockVacuumSettingsQueries{rows: overrideOptimalWith(tt.setting, tt.value)}
			report, err := vacuumsettings.New(queryer).Check(context.Background())
			require.NoError(t, err)

			finding := findResult(report.Results, tt.setting)
			require.NotNil(t, finding)
			require.Equal(t, tt.name, finding.Name)
		})
	}
}

func findResult(results []check.Finding, id string) *check.Finding {
	for i := range results {
		if results[i].ID == id {
			return &results[i]
		}
	}
	return nil
}

func Test_VacuumSettings_RAMBudgetMessages(t *testing.T) {
	t.Parallel()

	// All cases run against mockMetadata: db.t4g.large, 8GB RAM.
	tests := []struct {
		Name          string
		Overrides     map[string]string
		FindingID     string
		Severity      check.Severity
		Details       string
		DebugContains []string
	}{
		{
			Name:      "work_mem risky (WARN)",
			Overrides: map[string]string{"work_mem": "49152", "active_connections": "100", "max_connections": "500"},
			FindingID: "work_mem",
			Severity:  check.SeverityWarn,
			Details: "work_mem 48.0MiB × 100 backends → 4.7GiB (58.6% of 8.0GiB RAM)\n" +
				"Worst case at max_connections 500: 23.4GiB (293.0%) — only if every connection slot fills.",
			DebugContains: []string{
				"Instance: db.t4g.large (8.0GiB RAM)",
				"Observed backends: 100 using ~4.7GiB (58.6% of available RAM)",
				"Worst case at max_connections 500: 23.4GiB (293.0%) — shown for context, not graded",
				"Backend count is a single sample from pg_stat_activity; a quiet window under-reports.",
				"Still safe today, but more backends or multi-sort queries could cause memory pressure.",
			},
		},
		{
			Name:      "work_mem dangerous (FAIL)",
			Overrides: map[string]string{"work_mem": "2097152"},
			FindingID: "work_mem",
			Severity:  check.SeverityFail,
			Details: "work_mem 2.0GiB × 10 backends → 20.0GiB (250.0% of 8.0GiB RAM)\n" +
				"Worst case at max_connections 100: 200.0GiB (2500.0%) — only if every connection slot fills.",
			DebugContains: []string{
				"Instance: db.t4g.large (8.0GiB RAM)",
				"Observed backends: 10 using ~20.0GiB (250.0% of available RAM)",
				"Worst case at max_connections 100: 200.0GiB (2500.0%) — shown for context, not graded",
				"At the current backend count this can cause out-of-memory errors.",
				"Note: Each query operation (sort/hash) can use work_mem multiple times.",
			},
		},
		{
			Name:      "maintenance_work_mem high budget (WARN)",
			Overrides: map[string]string{"maintenance_work_mem": "524288"}, // 512MB × 4 workers = 2048MB = 25% of 8GB
			FindingID: "maintenance_work_mem",
			Severity:  check.SeverityWarn,
			Details:   "maintenance_work_mem 512.0MiB × autovacuum_max_workers 4 → total budget 2.0GiB (25.0% of 8.0GiB RAM)",
			DebugContains: []string{
				"Instance: db.t4g.large (8.0GiB RAM)",
				"Total RAM = maintenance_work_mem × autovacuum_max_workers",
				"Your config: 512.0MiB × 4 workers = 2.0GiB",
				"While not critical, consider keeping total under 12.5% RAM (1/8 of total).",
			},
		},
		{
			Name:      "maintenance_work_mem dangerous budget (FAIL)",
			Overrides: map[string]string{"maintenance_work_mem": "1048576"}, // 1024MB × 4 workers = 4096MB = 50% of 8GB
			FindingID: "maintenance_work_mem",
			Severity:  check.SeverityFail,
			Details:   "maintenance_work_mem 1.0GiB × autovacuum_max_workers 4 → total budget 4.0GiB (50.0% of 8.0GiB RAM)",
			DebugContains: []string{
				"Instance: db.t4g.large (8.0GiB RAM)",
				"Your config: 1.0GiB × 4 workers = 4.0GiB",
				"This can cause memory pressure. Keep total under 25% RAM.",
				"Manual VACUUM and CREATE INDEX operations also use this memory.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockVacuumSettingsQueries{rows: overrideOptimalWithAll(tt.Overrides)}
			checker := vacuumsettings.New(queryer)

			ctx := check.ContextWithInstanceMetadata(context.Background(), mockMetadata())
			report, err := checker.Check(ctx)
			require.NoError(t, err)

			finding := findResult(report.Results, tt.FindingID)
			require.NotNil(t, finding)
			require.Equal(t, tt.Severity, finding.Severity)
			require.Equal(t, tt.Details, finding.Details)
			for _, want := range tt.DebugContains {
				require.Contains(t, finding.Debug, want)
			}
		})
	}
}

func Test_VacuumSettings_LargeInstanceDefaultMaintenanceMem(t *testing.T) {
	t.Parallel()

	queryer := &mockVacuumSettingsQueries{rows: overrideOptimalWith("maintenance_work_mem", "65536")}
	checker := vacuumsettings.New(queryer)

	meta := &check.InstanceMetadata{InstanceClass: "db.r6g.4xlarge", VCPUCores: 16, MemoryGB: 128}
	report, err := checker.Check(check.ContextWithInstanceMetadata(context.Background(), meta))
	require.NoError(t, err)

	finding := findResult(report.Results, "maintenance_work_mem")
	require.NotNil(t, finding)
	require.Contains(t, finding.Details, "maintenance_work_mem is 64.0MiB on very large instance db.r6g.4xlarge (128.0GiB RAM)")
	require.Contains(t, finding.Details, "Current total budget: 64.0MiB × 4 workers = 256.0MiB (0.2% RAM)")
	require.Contains(t, finding.Details, "Recommended total: 1.0GiB × 4 workers = 4.0GiB (3.1% RAM)")
}

func Test_VacuumSettings_WorkMemGuards(t *testing.T) {
	t.Parallel()

	missingBackendRow := optimalVacuumSettings()
	delete(missingBackendRow, "active_connections")
	missingBackendRow["work_mem"] = "2097152"

	tests := []struct {
		Name string
		Rows []db.VacuumSettingsRow
		Meta *check.InstanceMetadata
	}{
		{"backend count is zero", overrideOptimalWithAll(map[string]string{"work_mem": "2097152", "active_connections": "0"}), mockMetadata()},
		{"backend count row missing", mapToVacuumSettingsRows(missingBackendRow), mockMetadata()},
		{"instance memory unknown", overrideOptimalWith("work_mem", "2097152"), &check.InstanceMetadata{InstanceClass: "db.unknown", VCPUCores: 2, MemoryGB: 0}},
		{"instance memory below one megabyte", overrideOptimalWith("work_mem", "2097152"), &check.InstanceMetadata{InstanceClass: "db.unknown", VCPUCores: 2, MemoryGB: 0.0005}},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockVacuumSettingsQueries{rows: tt.Rows}
			checker := vacuumsettings.New(queryer)

			ctx := check.ContextWithInstanceMetadata(context.Background(), tt.Meta)
			report, err := checker.Check(ctx)
			require.NoError(t, err)

			require.Nil(t, findResult(report.Results, "work_mem"))
		})
	}
}

func Test_VacuumSettings_MaintenanceWorkMemMemoryUnknown(t *testing.T) {
	t.Parallel()

	queryer := &mockVacuumSettingsQueries{rows: overrideOptimalWith("maintenance_work_mem", "1048576")}
	checker := vacuumsettings.New(queryer)

	ctx := check.ContextWithInstanceMetadata(context.Background(), &check.InstanceMetadata{EngineVersion: "17.4", EngineVersionMajor: 17, EngineVersionMinor: 4})
	report, err := checker.Check(ctx)
	require.NoError(t, err)

	require.Nil(t, findResult(report.Results, "maintenance_work_mem"))
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
	require.Equal(t, check.SeverityPass, results[0].Severity)
}
