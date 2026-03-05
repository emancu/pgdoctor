// Package replicationlag implements checks for replication lag monitoring.
package replicationlag

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

const (
	// Physical replication thresholds (streaming replication to standbys).
	// Strict thresholds because physical standbys should be near-synchronous.
	physicalWarnSeconds = 0.25 // 250ms
	physicalFailSeconds = 1.0  // 1 second

	// Logical replication thresholds (CDC/Debezium, selective replication).
	// Debezium can wait up to 30 seconds to acknowledge WAL consumption during batch processing.
	// These thresholds accommodate Debezium's normal operation while detecting genuine issues.
	logicalWarnSeconds = 20.0 // 20 seconds - investigation threshold
	logicalFailSeconds = 35.0 // 35 seconds - exceeds Debezium max ack window

	// WAL retention status values.
	walStatusLost       = "lost"
	walStatusUnreserved = "unreserved"
	walStatusExtended   = "extended"
)

type ReplicationLagQueries interface {
	ReplicationLag(context.Context) ([]db.ReplicationLagRow, error)
}

type checker struct {
	queries ReplicationLagQueries
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryPerformance,
		CheckID:     "replication-lag",
		Name:        "Replication Lag",
		Description: "Monitors active replication streams for lag issues",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries ReplicationLagQueries, _ ...check.Config) check.Checker {
	return &checker{
		queries: queries,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.ReplicationLag(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", check.CategoryPerformance, report.CheckID, err)
	}

	if len(rows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "no-replication",
			Name:     "No Replication Configured",
			Severity: check.SeverityOK,
			Details:  "No active replication streams found",
		})
		return report, nil
	}

	// Check replication state and WAL retention for all streams
	checkReplicationState(rows, report)
	checkWALRetention(rows, report)

	// Check lag by replication type
	var physicalRows, logicalRows []db.ReplicationLagRow
	for _, row := range rows {
		switch row.ReplicationType.String {
		case "physical":
			physicalRows = append(physicalRows, row)
		case "logical":
			logicalRows = append(logicalRows, row)
		}
	}

	if len(physicalRows) > 0 {
		checkPhysicalReplicationLag(physicalRows, report)
	}

	if len(logicalRows) > 0 {
		checkLogicalReplicationLag(logicalRows, report)
	}

	return report, nil
}

func checkPhysicalReplicationLag(rows []db.ReplicationLagRow, report *check.Report) {
	var laggingRows []db.ReplicationLagRow
	maxSeverity := check.SeverityOK

	for _, row := range rows {
		// COALESCE in query ensures these are always valid
		lagSeconds := row.ReplayLagSeconds.Float64
		if lagSeconds >= physicalWarnSeconds {
			laggingRows = append(laggingRows, row)
			if lagSeconds >= physicalFailSeconds {
				maxSeverity = check.SeverityFail
			} else if maxSeverity != check.SeverityFail {
				maxSeverity = check.SeverityWarn
			}
		}
	}

	if len(laggingRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "physical-replication-lag",
			Name:     "Physical Replication Lag",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All %d physical replication stream(s) are healthy", len(rows)),
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range laggingRows {
		// COALESCE in query ensures these are always valid
		lagSeconds := row.ReplayLagSeconds.Float64
		severity := check.SeverityWarn
		if lagSeconds >= physicalFailSeconds {
			severity = check.SeverityFail
		}

		slotName := row.SlotName.String
		if slotName == "" {
			slotName = "[no slot]"
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.ApplicationName.String,
				row.State.String,
				fmt.Sprintf("%.2fs", lagSeconds),
				check.FormatBytes(row.ReplayLagBytes.Int64),
				slotName,
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "physical-replication-lag",
		Name:     "Physical Replication Lag",
		Severity: maxSeverity,
		Details:  fmt.Sprintf("%d of %d physical replication stream(s) are lagging", len(laggingRows), len(rows)),
		Table: &check.Table{
			Headers: []string{"Application", "State", "Replay Lag", "Lag Bytes", "Slot"},
			Rows:    tableRows,
		},
	})
}

func checkLogicalReplicationLag(rows []db.ReplicationLagRow, report *check.Report) {
	var laggingRows []db.ReplicationLagRow
	maxSeverity := check.SeverityOK

	for _, row := range rows {
		// COALESCE in query ensures these are always valid
		lagSeconds := row.ReplayLagSeconds.Float64
		if lagSeconds >= logicalWarnSeconds {
			laggingRows = append(laggingRows, row)
			if lagSeconds >= logicalFailSeconds {
				maxSeverity = check.SeverityFail
			} else if maxSeverity != check.SeverityFail {
				maxSeverity = check.SeverityWarn
			}
		}
	}

	if len(laggingRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "logical-replication-lag",
			Name:     "Logical Replication Lag",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All %d logical replication stream(s) are healthy", len(rows)),
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range laggingRows {
		// COALESCE in query ensures these are always valid
		lagSeconds := row.ReplayLagSeconds.Float64
		severity := check.SeverityWarn
		if lagSeconds >= logicalFailSeconds {
			severity = check.SeverityFail
		}

		slotName := row.SlotName.String
		if slotName == "" {
			slotName = "[no slot]"
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.ApplicationName.String,
				row.State.String,
				fmt.Sprintf("%.2fs", lagSeconds),
				check.FormatBytes(row.ReplayLagBytes.Int64),
				slotName,
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "logical-replication-lag",
		Name:     "Logical Replication Lag",
		Severity: maxSeverity,
		Details:  fmt.Sprintf("%d of %d logical replication stream(s) are lagging", len(laggingRows), len(rows)),
		Table: &check.Table{
			Headers: []string{"Application", "State", "Replay Lag", "Lag Bytes", "Slot"},
			Rows:    tableRows,
		},
	})
}

func checkReplicationState(rows []db.ReplicationLagRow, report *check.Report) {
	var problematicRows []db.ReplicationLagRow

	for _, row := range rows {
		if row.State.String != "streaming" {
			problematicRows = append(problematicRows, row)
		}
	}

	if len(problematicRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "replication-state",
			Name:     "Replication State",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All %d replication stream(s) are in 'streaming' state", len(rows)),
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range problematicRows {
		severity := check.SeverityWarn
		if row.State.String == "backup" || row.State.String == "stopping" {
			severity = check.SeverityFail
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.ApplicationName.String,
				row.ReplicationType.String,
				row.State.String,
				row.SlotName.String,
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "replication-state",
		Name:     "Replication State",
		Severity: check.SeverityWarn,
		Details:  fmt.Sprintf("%d of %d replication stream(s) are not in 'streaming' state", len(problematicRows), len(rows)),
		Table: &check.Table{
			Headers: []string{"Application", "Type", "State", "Slot"},
			Rows:    tableRows,
		},
	})
}

func checkWALRetention(rows []db.ReplicationLagRow, report *check.Report) {
	var problematicRows []db.ReplicationLagRow
	maxSeverity := check.SeverityOK

	for _, row := range rows {
		walStatus := row.WalStatus.String
		if walStatus == walStatusLost || walStatus == walStatusUnreserved || walStatus == walStatusExtended {
			problematicRows = append(problematicRows, row)
			if walStatus == walStatusLost {
				maxSeverity = check.SeverityFail
			} else if maxSeverity != check.SeverityFail && walStatus == walStatusUnreserved {
				maxSeverity = check.SeverityFail
			} else if maxSeverity == check.SeverityOK {
				maxSeverity = check.SeverityWarn
			}
		}
	}

	if len(problematicRows) == 0 {
		report.AddFinding(check.Finding{
			ID:       "wal-retention",
			Name:     "WAL Retention",
			Severity: check.SeverityOK,
			Details:  fmt.Sprintf("All %d replication slot(s) have healthy WAL retention", len(rows)),
		})
		return
	}

	var tableRows []check.TableRow
	for _, row := range problematicRows {
		walStatus := row.WalStatus.String
		severity := check.SeverityWarn
		if walStatus == walStatusLost || walStatus == walStatusUnreserved {
			severity = check.SeverityFail
		}

		tableRows = append(tableRows, check.TableRow{
			Cells: []string{
				row.ApplicationName.String,
				row.ReplicationType.String,
				row.SlotName.String,
				walStatus,
			},
			Severity: severity,
		})
	}

	report.AddFinding(check.Finding{
		ID:       "wal-retention",
		Name:     "WAL Retention",
		Severity: maxSeverity,
		Details:  fmt.Sprintf("%d of %d replication slot(s) have WAL retention issues", len(problematicRows), len(rows)),
		Table: &check.Table{
			Headers: []string{"Application", "Type", "Slot", "WAL Status"},
			Rows:    tableRows,
		},
	})
}
