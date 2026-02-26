// Package replicationslots implements a check for PostgreSQL replication slot health.
package replicationslots

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	lagWarnThreshold = 1 * check.GiB
	lagFailThreshold = 5 * check.GiB
)

type ReplicationSlotsQueries interface {
	ReplicationSlots(context.Context) ([]db.ReplicationSlotsRow, error)
	ReplicationSlotsPG15(context.Context) ([]db.ReplicationSlotsPG15Row, error)
}

type checker struct {
	queryer ReplicationSlotsQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "replication-slots",
		Name:        "Replication Slots",
		Description: "Validates replication slot configuration and health status",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queryer ReplicationSlotsQueries, _ ...check.Config) check.Checker {
	return &checker{queryer: queryer}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	slots, err := c.fetchSlots(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	var invalidSlots []db.ReplicationSlotsRow
	var inactiveSlots []db.ReplicationSlotsRow
	var conflictingSlots []db.ReplicationSlotsRow
	var lostWALSlots []db.ReplicationSlotsRow
	var criticalLagSlots []db.ReplicationSlotsRow
	var highLagSlots []db.ReplicationSlotsRow

	for _, slot := range slots {
		// Check for invalidated slots (PG17+ only, will be NULL on PG15/16)
		if slot.InvalidationReason.Valid && slot.InvalidationReason.String != "" {
			invalidSlots = append(invalidSlots, slot)
			continue
		}

		// Check for conflicting slots (PG17+ only, will be NULL on PG15/16)
		if slot.Conflicting.Valid && slot.Conflicting.Bool {
			conflictingSlots = append(conflictingSlots, slot)
			continue
		}

		if slot.WalStatus.Valid && (slot.WalStatus.String == "lost" || slot.WalStatus.String == "unreserved") {
			lostWALSlots = append(lostWALSlots, slot)
			continue
		}

		if !slot.Active.Bool {
			inactiveSlots = append(inactiveSlots, slot)
			continue
		}

		if slot.RestartLsnLagBytes.Valid {
			lag := slot.RestartLsnLagBytes.Int64
			if lag >= lagFailThreshold {
				criticalLagSlots = append(criticalLagSlots, slot)
			} else if lag >= lagWarnThreshold {
				highLagSlots = append(highLagSlots, slot)
			}
		}
	}

	reportInvalidSlots(report, invalidSlots)
	reportLostWALSlots(report, lostWALSlots)
	reportConflictingSlots(report, conflictingSlots)
	reportInactiveSlots(report, inactiveSlots)
	reportCriticalLagSlots(report, criticalLagSlots)
	reportHighLagSlots(report, highLagSlots)

	// If no issues found
	if len(report.Results) == 0 {
		report.AddFinding(check.Finding{
			ID:       report.CheckID,
			Name:     report.Name,
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All %d replication slot(s) are healthy", len(slots)),
		})
	}

	return report, nil
}

// Retrieves replication slots using the appropriate query for the PG version.
// PG17+ query has inactive_since, conflicting, invalidation_reason.
// Falls back to PG15 query for older versions (returns NULLs for those columns).
func (c *checker) fetchSlots(ctx context.Context) ([]db.ReplicationSlotsRow, error) {
	meta := check.InstanceMetadataFromContext(ctx)

	if meta == nil || meta.EngineVersionMajor < 17 {
		pg15Slots, err := c.queryer.ReplicationSlotsPG15(ctx)
		if err != nil {
			return nil, err
		}
		slots := make([]db.ReplicationSlotsRow, len(pg15Slots))
		for i, s := range pg15Slots {
			slots[i] = db.ReplicationSlotsRow(s)
		}
		return slots, nil
	}

	return c.queryer.ReplicationSlots(ctx)
}

func reportInvalidSlots(report *check.Report, slots []db.ReplicationSlotsRow) {
	if len(slots) == 0 {
		return
	}

	lines := make([]string, 0, len(slots))
	for _, slot := range slots {
		lines = append(lines, fmt.Sprintf("  %s (reason: %s)", slot.SlotName.String, slot.InvalidationReason.String))
	}

	report.AddFinding(check.Finding{
		ID:       "invalid-slots",
		Name:     "Invalid Replication Slots",
		Severity: check.SeverityFail,
		Details:  fmt.Sprintf("Found %d invalid replication slot(s):\n%s", len(slots), strings.Join(lines, "\n")),
	})
}

func reportLostWALSlots(report *check.Report, slots []db.ReplicationSlotsRow) {
	if len(slots) == 0 {
		return
	}

	lines := make([]string, 0, len(slots))
	for _, slot := range slots {
		lines = append(lines, fmt.Sprintf("  %s (wal_status: %s)", slot.SlotName.String, slot.WalStatus.String))
	}

	report.AddFinding(check.Finding{
		ID:       "lost-wal-slots",
		Name:     "Slots with Lost WAL",
		Severity: check.SeverityFail,
		Details:  fmt.Sprintf("Found %d slot(s) with lost/unreserved WAL:\n%s", len(slots), strings.Join(lines, "\n")),
	})
}

func reportConflictingSlots(report *check.Report, slots []db.ReplicationSlotsRow) {
	if len(slots) == 0 {
		return
	}

	lines := make([]string, 0, len(slots))
	for _, slot := range slots {
		lines = append(lines, fmt.Sprintf("  %s", slot.SlotName.String))
	}

	report.AddFinding(check.Finding{
		ID:       "conflicting-slots",
		Name:     "Conflicting Replication Slots",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d slot(s) in conflicting state:\n%s", len(slots), strings.Join(lines, "\n")),
	})
}

func reportInactiveSlots(report *check.Report, slots []db.ReplicationSlotsRow) {
	if len(slots) == 0 {
		return
	}

	lines := make([]string, 0, len(slots))
	for _, slot := range slots {
		inactiveFor := "unknown"
		if slot.InactiveSeconds.Valid {
			inactiveFor = check.FormatDurationSec(slot.InactiveSeconds.Int64)
		}
		lagBytes := "unknown"
		if slot.RestartLsnLagBytes.Valid {
			lagBytes = check.FormatBytes(slot.RestartLsnLagBytes.Int64)
		}
		lines = append(lines, fmt.Sprintf("  %s (inactive: %s, lag: %s)", slot.SlotName.String, inactiveFor, lagBytes))
	}

	report.AddFinding(check.Finding{
		ID:       "inactive-slots",
		Name:     "Inactive Replication Slots",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d inactive slot(s):\n%s\n\nInactive slots prevent WAL cleanup and can fill disk.", len(slots), strings.Join(lines, "\n")),
	})
}

func reportCriticalLagSlots(report *check.Report, slots []db.ReplicationSlotsRow) {
	if len(slots) == 0 {
		return
	}

	lines := make([]string, 0, len(slots))
	for _, slot := range slots {
		lines = append(lines, fmt.Sprintf("  %s (lag: %s)", slot.SlotName.String, check.FormatBytes(slot.RestartLsnLagBytes.Int64)))
	}

	report.AddFinding(check.Finding{
		ID:       "critical-lag",
		Name:     "Critical Replication Lag",
		Severity: check.SeverityFail,
		Details:  fmt.Sprintf("Found %d slot(s) with critical lag (>= 5GB):\n%s\n\nConsumers are severely behind and may never catch up.", len(slots), strings.Join(lines, "\n")),
	})
}

func reportHighLagSlots(report *check.Report, slots []db.ReplicationSlotsRow) {
	if len(slots) == 0 {
		return
	}

	lines := make([]string, 0, len(slots))
	for _, slot := range slots {
		lines = append(lines, fmt.Sprintf("  %s (lag: %s)", slot.SlotName.String, check.FormatBytes(slot.RestartLsnLagBytes.Int64)))
	}

	report.AddFinding(check.Finding{
		ID:       "high-lag",
		Name:     "High Replication Lag",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("Found %d slot(s) with high lag (>= 1GB):\n%s\n\nConsumers are falling behind.", len(slots), strings.Join(lines, "\n")),
	})
}
