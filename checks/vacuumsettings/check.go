// Package vacuumsettings implements a check for validating autovacuum, maintenance memory, and vacuum cost settings.
package vacuumsettings

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type dbVacuumSettings []db.VacuumSettingsRow

type VacuumSettingsQueries interface {
	VacuumSettings(context.Context) ([]db.VacuumSettingsRow, error)
}

type checker struct {
	queryer VacuumSettingsQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryVacuum,
		CheckID:     "vacuum-settings",
		Name:        "PostgreSQL Vacuum & Maintenance Configs",
		Description: "Validates autovacuum, maintenance memory, and vacuum cost settings",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queryer VacuumSettingsQueries) check.Checker {
	return &checker{
		queryer: queryer,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	meta := check.InstanceMetadataFromContext(ctx)
	if meta == nil {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityWarn,
			Details:  "Instance metadata not available - skipping RAM-aware vacuum settings checks",
		})
		return report, nil
	}

	settings, err := c.queryer.VacuumSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryVacuum, report.CheckID, err)
	}

	dbSettings := dbVacuumSettings(settings)

	checkAutovacuumScaleFactors(dbSettings, report)
	checkAutovacuumWorkers(dbSettings, report, meta)
	checkMaintenanceWorkMem(dbSettings, report, meta)
	checkVacuumCostSettings(dbSettings, report)
	checkWorkMem(dbSettings, report, meta)

	if len(report.Results) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
		})
	}

	return report, nil
}

func checkAutovacuumScaleFactors(s dbVacuumSettings, report *check.Report) {
	// Check autovacuum_analyze_scale_factor
	analyzeScaleStr, fetchErr := s.fetch("autovacuum_analyze_scale_factor")
	analyzeScale := 0.1 // PostgreSQL default
	if fetchErr == nil {
		parsed, err := strconv.ParseFloat(analyzeScaleStr, 64)
		if err == nil {
			analyzeScale = parsed
		}
	}

	if analyzeScale > 0.1 {
		report.AddFinding(check.Finding{Name: "Default autovacuum_analyze_scale_factor",
			ID: "autovacuum_analyze_scale_factor", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("autovacuum_analyze_scale_factor too high: %.2f (recommend 0.05-0.1)", analyzeScale),
		})
	} else if analyzeScale < 0.01 {
		report.AddFinding(check.Finding{Name: "Default autovacuum_analyze_scale_factor",
			ID: "autovacuum_analyze_scale_factor", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("autovacuum_analyze_scale_factor too low: %.2f (may cause excessive analyze)", analyzeScale),
		})
	}

	// Check autovacuum_vacuum_scale_factor
	vacuumScaleStr, fetchErr := s.fetch("autovacuum_vacuum_scale_factor")
	vacuumScale := 0.2 // PostgreSQL default
	if fetchErr == nil {
		parsed, err := strconv.ParseFloat(vacuumScaleStr, 64)
		if err == nil {
			vacuumScale = parsed
		}
	}

	if vacuumScale > 0.2 {
		report.AddFinding(check.Finding{Name: "Default autovacuum_vacuum_scale_factor",
			ID: "autovacuum_vacuum_scale_factor", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("autovacuum_vacuum_scale_factor too high: %.2f (recommend 0.1-0.2)", vacuumScale),
		})
	} else if vacuumScale < 0.02 {
		report.AddFinding(check.Finding{Name: "Default autovacuum_vacuum_scale_factor",
			ID: "autovacuum_vacuum_scale_factor", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("autovacuum_vacuum_scale_factor too low: %.2f (may cause excessive vacuum)", vacuumScale),
		})
	}
}

func checkAutovacuumWorkers(s dbVacuumSettings, report *check.Report, meta *check.InstanceMetadata) {
	workers := s.fetchInt64("autovacuum_max_workers", 3) // PostgreSQL default: 3

	// Critical misconfigurations (no metadata needed)
	if workers == 0 {
		report.AddFinding(check.Finding{
			Name:     "Autovacuum workers disabled",
			ID:       "autovacuum_max_workers",
			Severity: check.SeverityFail,
			Details: "autovacuum_max_workers is 0 (autovacuum disabled)\n\n" +
				"This will cause table bloat and transaction ID wraparound.",
		})
		return
	}

	if workers == 1 {
		report.AddFinding(check.Finding{
			Name:     "Very low autovacuum workers",
			ID:       "autovacuum_max_workers",
			Severity: check.SeverityWarn,
			Details: "autovacuum_max_workers is 1 (critically low)\n\n" +
				"Single worker cannot keep up with multiple busy tables.",
		})
		return
	}

	if workers > 10 {
		report.AddFinding(check.Finding{
			Name:     "Excessive autovacuum workers",
			ID:       "autovacuum_max_workers",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("autovacuum_max_workers is %d (unusually high)\n\n"+
				"Too many workers cause I/O contention and waste resources.",
				workers),
		})
		return
	}

	// Context-aware check for very large instances only
	// Only flag when workers = 3 might genuinely be a bottleneck (≥32 vCPU)
	if meta.VCPUCores >= 32 && workers == 3 {
		recommended := 6 // Conservative: fixed recommendation for very large instances

		report.AddFinding(check.Finding{
			Name:     "Low autovacuum workers for large instance",
			ID:       "autovacuum_max_workers",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("autovacuum_max_workers is 3 on very large instance %s (%d vCPU)\n\n"+
				"With %d vCPU cores and likely many concurrent tables, 3 workers may be a bottleneck.\n"+
				"Consider %d workers for better parallelism on this instance size.",
				meta.InstanceClass, meta.VCPUCores, meta.VCPUCores, recommended),
		})
	}
}

func checkMaintenanceWorkMem(s dbVacuumSettings, report *check.Report, meta *check.InstanceMetadata) {
	maintenanceMemKB := s.fetchInt64("maintenance_work_mem", 65536) // PostgreSQL default: 64MB
	maintenanceMemMB := maintenanceMemKB / 1024

	autovacuumMaxWorkers := s.fetchInt64("autovacuum_max_workers", 3)

	// Critical misconfigurations (no metadata needed)
	if maintenanceMemMB < 32 {
		report.AddFinding(check.Finding{
			Name:     "Very low maintenance_work_mem",
			ID:       "maintenance_work_mem",
			Severity: check.SeverityWarn,
			Details:  fmt.Sprintf("maintenance_work_mem is %dMB (below half the PostgreSQL default)\n\nMay cause slow VACUUM operations requiring multiple passes.", maintenanceMemMB),
		})
		return
	}

	if maintenanceMemMB > 4096 {
		report.AddFinding(check.Finding{
			Name:     "Excessive maintenance_work_mem",
			ID:       "maintenance_work_mem",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("maintenance_work_mem is %dMB (unusually high)\n\n"+
				"Values above 2GB show diminishing returns for VACUUM performance.\n"+
				"PostgreSQL has a 1GB limit for tracking dead tuples (though >1GB helps CREATE INDEX).",
				maintenanceMemMB),
		})
		return
	}

	// RAM-aware check: Total budget calculation
	// CRITICAL: maintenance_work_mem × autovacuum_max_workers = total RAM used
	availableRAMMB := int64(meta.MemoryGB * 1024)
	totalBudgetMB := maintenanceMemMB * autovacuumMaxWorkers
	budgetPercent := (float64(totalBudgetMB) / float64(availableRAMMB)) * 100

	// Flag dangerous total budgets
	if budgetPercent > 25 {
		report.AddFinding(check.Finding{
			Name:     "Dangerous maintenance_work_mem total budget",
			ID:       "maintenance_work_mem",
			Severity: check.SeverityFail,
			Details: fmt.Sprintf("maintenance_work_mem is %dMB on %s (%.0fGB RAM) with autovacuum_max_workers=%d\n\n"+
				"Total autovacuum RAM budget: %dMB (%.1f%% of available RAM)\n\n"+
				"CRITICAL: When autovacuum runs, it allocates memory per worker:\n"+
				"  Total RAM = maintenance_work_mem × autovacuum_max_workers\n"+
				"  Your config: %dMB × %d workers = %dMB\n\n"+
				"This can cause memory pressure. Keep total under 25%% RAM.\n"+
				"Manual VACUUM and CREATE INDEX operations also use this memory.",
				maintenanceMemMB, meta.InstanceClass, meta.MemoryGB, autovacuumMaxWorkers,
				totalBudgetMB, budgetPercent,
				maintenanceMemMB, autovacuumMaxWorkers, totalBudgetMB),
		})
		return
	}

	if budgetPercent > 12.5 {
		report.AddFinding(check.Finding{
			Name:     "High maintenance_work_mem total budget",
			ID:       "maintenance_work_mem",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("maintenance_work_mem is %dMB on %s (%.0fGB RAM) with autovacuum_max_workers=%d\n\n"+
				"Total autovacuum RAM budget: %dMB (%.1f%% of available RAM)\n\n"+
				"Total RAM = maintenance_work_mem × autovacuum_max_workers\n"+
				"Your config: %dMB × %d workers = %dMB\n\n"+
				"While not critical, consider keeping total under 12.5%% RAM (1/8 of total).",
				maintenanceMemMB, meta.InstanceClass, meta.MemoryGB, autovacuumMaxWorkers,
				totalBudgetMB, budgetPercent,
				maintenanceMemMB, autovacuumMaxWorkers, totalBudgetMB),
		})
		return
	}

	// Context-aware: Large instance with default 64MB
	if meta.MemoryGB >= 64 && maintenanceMemMB == 64 {
		recommendedMB := int64(1024) // 1GB for very large instances
		newTotalBudgetMB := recommendedMB * autovacuumMaxWorkers
		newBudgetPercent := (float64(newTotalBudgetMB) / float64(availableRAMMB)) * 100

		report.AddFinding(check.Finding{
			Name:     "Low maintenance_work_mem for large instance",
			ID:       "maintenance_work_mem",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("maintenance_work_mem is 64MB on very large instance %s (%.0fGB RAM)\n\n"+
				"Large instances typically have large tables with more dead tuples.\n"+
				"64MB can track only ~400K dead tuples (may require multiple VACUUM passes).\n"+
				"Consider %dMB (can track ~6M dead tuples in one pass).\n\n"+
				"Current total budget: 64MB × %d workers = %dMB (%.1f%% RAM)\n"+
				"Recommended total: %dMB × %d workers = %dMB (%.1f%% RAM)",
				meta.InstanceClass, meta.MemoryGB, recommendedMB,
				autovacuumMaxWorkers, 64*autovacuumMaxWorkers, (float64(64*autovacuumMaxWorkers)/float64(availableRAMMB))*100,
				recommendedMB, autovacuumMaxWorkers, newTotalBudgetMB, newBudgetPercent),
		})
	}
}

func checkVacuumCostSettings(s dbVacuumSettings, report *check.Report) {
	// Check vacuum_cost_delay
	costDelay := s.fetchInt64("vacuum_cost_delay", 2) // PostgreSQL default: 2ms

	if costDelay > 20 {
		report.AddFinding(check.Finding{Name: "Default vacuum_cost_delay",
			ID: "vacuum_cost_delay", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("vacuum_cost_delay too high: %dms (may slow vacuum, recommend 0-10ms)", costDelay),
		})
	}

	// Check vacuum_cost_limit
	costLimit := s.fetchInt64("vacuum_cost_limit", 200) // PostgreSQL default: 200

	if costLimit < 200 {
		report.AddFinding(check.Finding{Name: "Default vacuum_cost_limit",
			ID: "vacuum_cost_limit", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("vacuum_cost_limit too low: %d (may slow vacuum, default 200)", costLimit),
		})
	} else if costLimit > 10000 {
		report.AddFinding(check.Finding{Name: "Default vacuum_cost_limit",
			ID: "vacuum_cost_limit", Severity: check.SeverityWarn,
			Details: fmt.Sprintf("vacuum_cost_limit very high: %d (may cause I/O spikes)", costLimit),
		})
	}
}

func checkWorkMem(s dbVacuumSettings, report *check.Report, meta *check.InstanceMetadata) {
	workMemKB := s.fetchInt64("work_mem", 4096) // PostgreSQL default: 4MB
	workMemMB := workMemKB / 1024

	maxConnections := s.fetchInt64("max_connections", 100) // PostgreSQL default
	activeConnections := s.fetchInt64("active_connections", 0)

	if workMemMB < 4 {
		report.AddFinding(check.Finding{
			Name:     "Very low work_mem",
			ID:       "work_mem",
			Severity: check.SeverityFail,
			Details: fmt.Sprintf("work_mem is %dMB (critically low)\n\n"+
				"Will cause excessive temporary file usage for sorts and hash operations.",
				workMemMB),
		})
		return
	}

	availableRAMMB := int64(meta.MemoryGB * 1024)

	// Calculate worst-case: all connections use work_mem
	worstCaseRAMMB := workMemMB * maxConnections
	worstCasePercent := (float64(worstCaseRAMMB) / float64(availableRAMMB)) * 100

	// Calculate typical: active connections use work_mem
	typicalRAMMB := workMemMB * activeConnections
	typicalPercent := (float64(typicalRAMMB) / float64(availableRAMMB)) * 100

	// Flag dangerous configurations
	if worstCasePercent > 80 {
		report.AddFinding(check.Finding{
			Name:     "Dangerous work_mem configuration",
			ID:       "work_mem",
			Severity: check.SeverityFail,
			Details: fmt.Sprintf("work_mem is %dMB on %s (%.0fGB RAM) with max_connections=%d\n\n"+
				"Worst-case RAM usage: %dMB (%.1f%% of available RAM)\n"+
				"Current active connections: %d using ~%dMB (%.1f%%)\n\n"+
				"This configuration can cause out-of-memory errors when connections spike.\n"+
				"Note: Each query operation (sort/hash) can use work_mem multiple times.",
				workMemMB, meta.InstanceClass, meta.MemoryGB, maxConnections,
				worstCaseRAMMB, worstCasePercent,
				activeConnections, typicalRAMMB, typicalPercent),
		})
		return
	}

	if worstCasePercent > 50 {
		report.AddFinding(check.Finding{
			Name:     "Risky work_mem configuration",
			ID:       "work_mem",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("work_mem is %dMB on %s (%.0fGB RAM) with max_connections=%d\n\n"+
				"Worst-case RAM usage: %dMB (%.1f%% of available RAM)\n"+
				"Current active connections: %d using ~%dMB (%.1f%%)\n\n"+
				"While currently safe, connection spikes could cause memory pressure.",
				workMemMB, meta.InstanceClass, meta.MemoryGB, maxConnections,
				worstCaseRAMMB, worstCasePercent,
				activeConnections, typicalRAMMB, typicalPercent),
		})
		return
	}

	if activeConnections > 0 && typicalPercent > 40 {
		report.AddFinding(check.Finding{
			Name:     "High current work_mem usage",
			ID:       "work_mem",
			Severity: check.SeverityWarn,
			Details: fmt.Sprintf("work_mem is %dMB with %d active connections on %s (%.0fGB RAM)\n\n"+
				"Current RAM usage: ~%dMB (%.1f%% of available RAM)\n"+
				"Worst-case with max connections (%d): %dMB (%.1f%%)\n\n"+
				"High memory usage from current connections. Monitor for memory pressure.",
				workMemMB, activeConnections, meta.InstanceClass, meta.MemoryGB,
				typicalRAMMB, typicalPercent,
				maxConnections, worstCaseRAMMB, worstCasePercent),
		})
		return
	}
}

// Type functions

func (s dbVacuumSettings) fetch(name string) (string, error) {
	for _, n := range s {
		if n.Name.Valid && n.Name.String == name && n.Setting.Valid {
			return n.Setting.String, nil
		}
	}

	return "", fmt.Errorf("setting %s not found", name)
}

func (s dbVacuumSettings) fetchInt64(name string, defaultValue int64) int64 {
	str, err := s.fetch(name)
	if err != nil {
		return defaultValue
	}

	parsed, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return defaultValue
	}

	return parsed
}
