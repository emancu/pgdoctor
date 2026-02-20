package freezeage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/freezeage"
	"github.com/emancu/pgdoctor/db"
)

const (
	findingIDDatabaseFreezeAge = "database-freeze-age"
	findingIDTableFreezeAge    = "table-freeze-age"
)

type mockQueryer struct {
	dbRows    []db.DatabaseFreezeAgeRow
	tableRows []db.TableFreezeAgeRow
	dbErr     error
	tableErr  error
}

func (m *mockQueryer) DatabaseFreezeAge(context.Context) ([]db.DatabaseFreezeAgeRow, error) {
	if m.dbErr != nil {
		return nil, m.dbErr
	}
	return m.dbRows, nil
}

func (m *mockQueryer) TableFreezeAge(context.Context) ([]db.TableFreezeAgeRow, error) {
	if m.tableErr != nil {
		return nil, m.tableErr
	}
	return m.tableRows, nil
}

func makeDatabaseRow(dbName string, freezeAge int32, freezeMaxAge int64) db.DatabaseFreezeAgeRow {
	return db.DatabaseFreezeAgeRow{
		DatabaseName: pgtype.Text{String: dbName, Valid: true},
		FrozenXid:    pgtype.Text{String: "1000", Valid: true},
		FreezeAge:    pgtype.Int4{Int32: freezeAge, Valid: true},
		FreezeMaxAge: pgtype.Int8{Int64: freezeMaxAge, Valid: true},
	}
}

func makeTableRow(
	schemaName, tableName string,
	freezeAge int32,
	tableSizeBytes int64,
	lastAutovacuum, lastVacuum *time.Time,
	autovacuumCount, vacuumCount int64,
) db.TableFreezeAgeRow {
	fullTableName := schemaName + "." + tableName
	row := db.TableFreezeAgeRow{
		TableName:       pgtype.Text{String: fullTableName, Valid: true},
		FrozenXid:       pgtype.Text{String: "1000", Valid: true},
		FreezeAge:       pgtype.Int4{Int32: freezeAge, Valid: true},
		TableSizeBytes:  pgtype.Int8{Int64: tableSizeBytes, Valid: true},
		AutovacuumCount: pgtype.Int8{Int64: autovacuumCount, Valid: true},
		VacuumCount:     pgtype.Int8{Int64: vacuumCount, Valid: true},
	}

	if lastAutovacuum != nil {
		row.LastAutovacuum = pgtype.Timestamptz{Time: *lastAutovacuum, Valid: true}
	}
	if lastVacuum != nil {
		row.LastVacuum = pgtype.Timestamptz{Time: *lastVacuum, Valid: true}
	}

	return row
}

func TestFreezeAge_AllHealthy(t *testing.T) {
	t.Parallel()

	lastVacuum := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 100_000_000, 200_000_000),
			makeDatabaseRow("myapp", 50_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "users", 100_000_000, 1024*1024*100, &lastVacuum, nil, 10, 2),
			makeTableRow("public", "orders", 50_000_000, 1024*1024*50, &lastVacuum, nil, 5, 1),
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityOK, report.Severity)
	require.Equal(t, 2, len(report.Results))

	for _, finding := range report.Results {
		require.Equal(t, check.SeverityOK, finding.Severity)
	}
}

func TestFreezeAge_DatabaseWarning(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 600_000_000, 200_000_000), // Above 500M warning
		},
		tableRows: []db.TableFreezeAgeRow{},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	var dbFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDDatabaseFreezeAge {
			dbFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, dbFinding)
	require.Equal(t, check.SeverityWarn, dbFinding.Severity)
	require.Contains(t, dbFinding.Details, "1 database(s)")
	require.NotNil(t, dbFinding.Table)
	require.Equal(t, 1, len(dbFinding.Table.Rows))
	require.Equal(t, check.SeverityWarn, dbFinding.Table.Rows[0].Severity)
}

func TestFreezeAge_DatabaseCritical(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 1_100_000_000, 200_000_000), // Above 1B critical
		},
		tableRows: []db.TableFreezeAgeRow{},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	var dbFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDDatabaseFreezeAge {
			dbFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, dbFinding)
	require.Equal(t, check.SeverityFail, dbFinding.Severity)
	require.Contains(t, dbFinding.Details, "1 database(s)")
	require.NotNil(t, dbFinding.Table)
	require.Equal(t, 1, len(dbFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, dbFinding.Table.Rows[0].Severity)
}

func TestFreezeAge_DatabaseEmergency(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 1_600_000_000, 200_000_000), // Above 1.5B emergency
		},
		tableRows: []db.TableFreezeAgeRow{},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	var dbFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDDatabaseFreezeAge {
			dbFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, dbFinding)
	require.Equal(t, check.SeverityFail, dbFinding.Severity)
}

func TestFreezeAge_TableWarning(t *testing.T) {
	t.Parallel()

	lastVacuum := time.Now().Add(-24 * time.Hour)
	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 100_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "large_table", 500_000_000, 1024*1024*1024, &lastVacuum, nil, 10, 2), // Above 400M warning
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityWarn, report.Severity)

	var tableFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTableFreezeAge {
			tableFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, tableFinding)
	require.Equal(t, check.SeverityWarn, tableFinding.Severity)
	require.Contains(t, tableFinding.Details, "1 table(s)")
	require.NotNil(t, tableFinding.Table)
	require.Equal(t, 1, len(tableFinding.Table.Rows))
	require.Equal(t, check.SeverityWarn, tableFinding.Table.Rows[0].Severity)
}

func TestFreezeAge_TableCritical(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 100_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "old_table", 900_000_000, 1024*1024*500, nil, nil, 0, 0), // Above 800M critical
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	var tableFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTableFreezeAge {
			tableFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, tableFinding)
	require.Equal(t, check.SeverityFail, tableFinding.Severity)
	require.NotNil(t, tableFinding.Table)
	require.Equal(t, 1, len(tableFinding.Table.Rows))
	require.Equal(t, check.SeverityFail, tableFinding.Table.Rows[0].Severity)
}

func TestFreezeAge_MixedSeverity(t *testing.T) {
	t.Parallel()

	lastVacuum := time.Now().Add(-1 * time.Hour)
	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("critical_db", 1_100_000_000, 200_000_000), // Critical
			makeDatabaseRow("warning_db", 600_000_000, 200_000_000),    // Warning
			makeDatabaseRow("healthy_db", 100_000_000, 200_000_000),    // OK
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "critical_table", 900_000_000, 1024*1024*1024, nil, nil, 0, 0),        // Critical
			makeTableRow("public", "warning_table", 500_000_000, 1024*1024*500, &lastVacuum, nil, 5, 1),  // Warning
			makeTableRow("public", "healthy_table", 100_000_000, 1024*1024*100, &lastVacuum, nil, 10, 2), // OK
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	require.Equal(t, check.SeverityFail, report.Severity)

	var dbFinding *check.Finding
	var tableFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDDatabaseFreezeAge {
			dbFinding = &report.Results[i]
		}
		if report.Results[i].ID == findingIDTableFreezeAge {
			tableFinding = &report.Results[i]
		}
	}

	require.NotNil(t, dbFinding)
	require.Equal(t, check.SeverityFail, dbFinding.Severity)
	require.NotNil(t, dbFinding.Table)
	require.Equal(t, 2, len(dbFinding.Table.Rows)) // Critical + Warning

	require.NotNil(t, tableFinding)
	require.Equal(t, check.SeverityFail, tableFinding.Severity)
	require.NotNil(t, tableFinding.Table)
	require.Equal(t, 2, len(tableFinding.Table.Rows)) // Critical + Warning
}

func TestFreezeAge_EdgeCases_ExactThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		freezeAge        int32
		expectedSeverity check.Severity
		checkType        string // "database" or "table"
	}{
		{
			name:             "database exactly 500M - warning threshold",
			freezeAge:        500_000_000,
			expectedSeverity: check.SeverityWarn,
			checkType:        "database",
		},
		{
			name:             "database exactly 1B - critical threshold",
			freezeAge:        1_000_000_000,
			expectedSeverity: check.SeverityFail,
			checkType:        "database",
		},
		{
			name:             "database just below 500M",
			freezeAge:        499_999_999,
			expectedSeverity: check.SeverityOK,
			checkType:        "database",
		},
		{
			name:             "table exactly 400M - warning threshold",
			freezeAge:        400_000_000,
			expectedSeverity: check.SeverityWarn,
			checkType:        "table",
		},
		{
			name:             "table exactly 800M - critical threshold",
			freezeAge:        800_000_000,
			expectedSeverity: check.SeverityFail,
			checkType:        "table",
		},
		{
			name:             "table just below 400M",
			freezeAge:        399_999_999,
			expectedSeverity: check.SeverityOK,
			checkType:        "table",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{}

			if tt.checkType == "database" {
				queryer.dbRows = []db.DatabaseFreezeAgeRow{
					makeDatabaseRow("test_db", tt.freezeAge, 200_000_000),
				}
				queryer.tableRows = []db.TableFreezeAgeRow{}
			} else {
				queryer.dbRows = []db.DatabaseFreezeAgeRow{
					makeDatabaseRow("test_db", 100_000_000, 200_000_000),
				}
				lastVacuum := time.Now()
				queryer.tableRows = []db.TableFreezeAgeRow{
					makeTableRow("public", "test_table", tt.freezeAge, 1024*1024*100, &lastVacuum, nil, 5, 1),
				}
			}

			checker := freezeage.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)

			findingID := findingIDDatabaseFreezeAge
			if tt.checkType == "table" {
				findingID = findingIDTableFreezeAge
			}

			var targetFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingID {
					targetFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, targetFinding)
			assert.Equal(t, tt.expectedSeverity, targetFinding.Severity,
				"age=%d should be %s", tt.freezeAge, tt.expectedSeverity)
		})
	}
}

func TestFreezeAge_TableFormatting_Database(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("myapp", 600_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var dbFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDDatabaseFreezeAge {
			dbFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, dbFinding)
	require.NotNil(t, dbFinding.Table)

	table := dbFinding.Table
	require.Equal(t, []string{"Database", "Age", "% to Limit", "Freeze Max Age"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))
	require.Equal(t, "myapp", table.Rows[0].Cells[0])
	require.Contains(t, table.Rows[0].Cells[1], "M") // Formatted age
	require.Contains(t, table.Rows[0].Cells[2], "%") // Percentage
}

func TestFreezeAge_TableFormatting_Table(t *testing.T) {
	t.Parallel()

	lastVacuum := time.Date(2024, 12, 1, 10, 30, 0, 0, time.UTC)
	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 100_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "users", 500_000_000, 1024*1024*1024, &lastVacuum, nil, 15, 3),
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var tableFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTableFreezeAge {
			tableFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, tableFinding)
	require.NotNil(t, tableFinding.Table)

	table := tableFinding.Table
	require.Equal(t, []string{"Table", "Age", "Size", "Last Vacuum", "Vacuum Count"}, table.Headers)
	require.Equal(t, 1, len(table.Rows))
	require.Equal(t, "public.users", table.Rows[0].Cells[0])
	require.Contains(t, table.Rows[0].Cells[1], "M")       // Formatted age
	require.Contains(t, table.Rows[0].Cells[2], "iB")      // Formatted size
	require.Contains(t, table.Rows[0].Cells[3], "2024-12") // Formatted timestamp
	require.Equal(t, "18", table.Rows[0].Cells[4])         // Vacuum count
}

func TestFreezeAge_TableNeverVacuumed(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 100_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "old_table", 500_000_000, 1024*1024*500, nil, nil, 0, 0),
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var tableFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTableFreezeAge {
			tableFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, tableFinding)
	require.NotNil(t, tableFinding.Table)
	require.Equal(t, 1, len(tableFinding.Table.Rows))
	require.Equal(t, "never", tableFinding.Table.Rows[0].Cells[3]) // Last Vacuum
	require.Equal(t, "0", tableFinding.Table.Rows[0].Cells[4])     // Vacuum Count
}

func TestFreezeAge_TableManualVacuum(t *testing.T) {
	t.Parallel()

	lastManualVacuum := time.Date(2024, 12, 1, 10, 30, 0, 0, time.UTC)
	queryer := &mockQueryer{
		dbRows: []db.DatabaseFreezeAgeRow{
			makeDatabaseRow("postgres", 100_000_000, 200_000_000),
		},
		tableRows: []db.TableFreezeAgeRow{
			makeTableRow("public", "manual_table", 500_000_000, 1024*1024*100, nil, &lastManualVacuum, 0, 5),
		},
	}

	checker := freezeage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)

	var tableFinding *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == findingIDTableFreezeAge {
			tableFinding = &report.Results[i]
			break
		}
	}

	require.NotNil(t, tableFinding)
	require.NotNil(t, tableFinding.Table)
	require.Equal(t, 1, len(tableFinding.Table.Rows))
	require.Contains(t, tableFinding.Table.Rows[0].Cells[3], "manual") // Shows (manual)
}

func TestFreezeAge_DatabaseQueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("database connection error")
	queryer := &mockQueryer{
		dbErr: expectedErr,
	}

	checker := freezeage.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "database")
}

func TestFreezeAge_TableQueryError(t *testing.T) {
	t.Parallel()

	expectedErr := fmt.Errorf("table query error")
	queryer := &mockQueryer{
		dbRows:   []db.DatabaseFreezeAgeRow{makeDatabaseRow("postgres", 100_000_000, 200_000_000)},
		tableErr: expectedErr,
	}

	checker := freezeage.New(queryer)
	_, err := checker.Check(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "tables")
}

func TestFreezeAge_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{
		dbRows:    []db.DatabaseFreezeAgeRow{},
		tableRows: []db.TableFreezeAgeRow{},
	}

	checker := freezeage.New(queryer)
	metadata := checker.Metadata()

	require.Equal(t, "freeze-age", metadata.CheckID)
	require.Equal(t, "Transaction ID Freeze Age", metadata.Name)
	require.Equal(t, check.CategoryVacuum, metadata.Category)
	require.NotEmpty(t, metadata.Description)
	require.NotEmpty(t, metadata.SQL)
	require.NotEmpty(t, metadata.Readme)
	require.Contains(t, metadata.Description, "transaction ID")
	require.Contains(t, metadata.Description, "wraparound")
}

func TestFreezeAge_AgeFormatting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		freezeAge    int32
		expectedText string
	}{
		{
			name:         "small age in K",
			freezeAge:    10_000,
			expectedText: "10.0K",
		},
		{
			name:         "medium age in M",
			freezeAge:    100_000_000,
			expectedText: "100.0M",
		},
		{
			name:         "large age in B",
			freezeAge:    1_500_000_000,
			expectedText: "1.50B",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			queryer := &mockQueryer{
				dbRows: []db.DatabaseFreezeAgeRow{
					makeDatabaseRow("test_db", tt.freezeAge, 200_000_000),
				},
				tableRows: []db.TableFreezeAgeRow{},
			}

			checker := freezeage.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)

			var dbFinding *check.Finding
			for i := range report.Results {
				if report.Results[i].ID == findingIDDatabaseFreezeAge {
					dbFinding = &report.Results[i]
					break
				}
			}

			require.NotNil(t, dbFinding)
			if tt.freezeAge >= 500_000_000 {
				// Only check table formatting if we have a WARN/FAIL finding
				require.NotNil(t, dbFinding.Table)
				require.Contains(t, dbFinding.Table.Rows[0].Cells[1], tt.expectedText)
			} else {
				// For OK findings, age is in details text
				require.Contains(t, dbFinding.Details, tt.expectedText)
			}
		})
	}
}
