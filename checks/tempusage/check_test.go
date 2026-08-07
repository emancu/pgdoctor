package tempusage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tempusage"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockQueryer struct {
	row        db.TempUsageRow
	err        error
	pgssOK     bool
	statements []db.TempUsageByStatementRow
}

func (m *mockQueryer) TempUsage(ctx context.Context) (db.TempUsageRow, error) {
	return m.row, m.err
}

func (m *mockQueryer) HasPgStatStatements(context.Context) (pgtype.Bool, error) {
	return pgtype.Bool{Bool: m.pgssOK, Valid: true}, nil
}

func (m *mockQueryer) TempUsageByStatement(context.Context) ([]db.TempUsageByStatementRow, error) {
	return m.statements, nil
}

func findFinding(t *testing.T, report *check.Report, id string) check.Finding {
	t.Helper()

	for _, result := range report.Results {
		if result.ID == id {
			return result
		}
	}

	t.Fatalf("finding %q not found", id)

	return check.Finding{}
}

func oneStatement() []db.TempUsageByStatementRow {
	return []db.TempUsageByStatementRow{{
		Queryid:          pgtype.Int8{Int64: 1, Valid: true},
		Calls:            pgtype.Int8{Int64: 10, Valid: true},
		TempBytesWritten: pgtype.Int8{Int64: 1024 * 1024, Valid: true},
		QueryText:        pgtype.Text{String: "select 1", Valid: true},
	}}
}

func makeTempUsageRow(
	tempFiles, tempBytes int64,
	secondsSinceReset float64,
	tempFilesPerHour, tempBytesPerHour float64,
	statsReset *time.Time,
) db.TempUsageRow {
	var secondsNumeric, filesPerHourNumeric, bytesPerHourNumeric pgtype.Numeric
	_ = secondsNumeric.Scan(fmt.Sprintf("%.2f", secondsSinceReset))
	_ = filesPerHourNumeric.Scan(fmt.Sprintf("%.2f", tempFilesPerHour))
	_ = bytesPerHourNumeric.Scan(fmt.Sprintf("%.2f", tempBytesPerHour))

	row := db.TempUsageRow{
		DatabaseName:      pgtype.Text{String: "test_db", Valid: true},
		TempFiles:         pgtype.Int8{Int64: tempFiles, Valid: true},
		TempBytes:         pgtype.Int8{Int64: tempBytes, Valid: true},
		SecondsSinceReset: secondsNumeric,
		TempFilesPerHour:  filesPerHourNumeric,
		TempBytesPerHour:  bytesPerHourNumeric,
	}

	row.WindowIsLowerBound = pgtype.Bool{Bool: statsReset == nil, Valid: true}
	if statsReset != nil {
		row.StatsReset = pgtype.Timestamptz{Time: *statsReset, Valid: true}
	}

	return row
}

func TestTempUsage_StatsResetTooRecent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		secondsSinceReset float64
	}{
		{
			name:              "stats reset 30 minutes ago",
			secondsSinceReset: 1800, // 30 minutes
		},
		{
			name:              "stats reset 1 minute ago",
			secondsSinceReset: 60,
		},
		{
			name:              "stats reset just now",
			secondsSinceReset: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := makeTempUsageRow(100, 1000000, tt.secondsSinceReset, 10.0, 100000.0, nil)
			queryer := &mockQueryer{row: row}
			checker := tempusage.New(queryer)

			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, check.SeveritySkip, report.Severity)
			assert.Len(t, report.Results, 1)
			assert.Equal(t, "temp-usage", report.Results[0].ID)
			assert.Equal(t, check.SeveritySkip, report.Results[0].Severity)
			assert.Contains(t, report.Results[0].Details, "need at least 1h of data")
		})
	}
}

// A NULL stats_reset carries no window at all, which is a different state from a
// window that is merely short: the counters may have been zeroed by an unclean
// shutdown or belong to a fresh replica. It must not read as a passing check.
func TestTempUsage_UnknownWindow(t *testing.T) {
	t.Parallel()

	row := makeTempUsageRow(100, 1000000, 0, 10.0, 100000.0, nil)
	row.SecondsSinceReset = pgtype.Numeric{Valid: false}

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)

	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeveritySkip, report.Severity)
	require.Len(t, report.Results, 1)
	assert.Equal(t, check.SeveritySkip, report.Results[0].Severity)
	assert.Contains(t, report.Results[0].Details, "Counter window unknown")
}

func TestTempUsage_AllHealthy(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	// Low temp file rate and low volume
	row := makeTempUsageRow(
		100,                 // 100 temp files total
		50*1024*1024,        // 50MB total
		oneHourInSeconds*24, // 24 hours since reset
		4.2,                 // 4.2 files/hour (< 5)
		2*1024*1024,         // 2MB/hour (< 1GB)
		&statsReset,
	)

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityPass, report.Severity)
	assert.Len(t, report.Results, 2)

	// Both subchecks should be OK
	assert.Equal(t, check.SeverityPass, report.Results[0].Severity)
	assert.Contains(t, report.Results[0].Name, "/hour")

	assert.Equal(t, check.SeverityPass, report.Results[1].Severity)
	assert.Contains(t, report.Results[1].Name, "/hour")
}

func TestTempUsage_HighFileRate_Warning(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	row := makeTempUsageRow(
		2400,                // temp files
		100*1024*1024,       // 100MB
		oneHourInSeconds*24, // 24 hours
		10.0,                // 10 files/hour (>= 5, < 20) -> WARN
		4*1024*1024,         // 4MB/hour
		&statsReset,
	)

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	assert.Equal(t, check.SeverityInfo, findFinding(t, report, "temp-file-rate").Severity)
	assert.Equal(t, check.SeverityWarn, findFinding(t, report, "temp-file-sources").Severity)
	assert.Contains(t, findFinding(t, report, "temp-file-rate").Name, "10.0 files/hour")
}

func TestTempUsage_HighFileRate_Critical(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	row := makeTempUsageRow(
		12000,               // temp files
		500*1024*1024,       // 500MB
		oneHourInSeconds*24, // 24 hours
		50.0,                // 50 files/hour (>= 20) -> FAIL
		20*1024*1024,        // 20MB/hour
		&statsReset,
	)

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	assert.Equal(t, check.SeverityInfo, findFinding(t, report, "temp-file-rate").Severity)
	assert.Equal(t, check.SeverityFail, findFinding(t, report, "temp-file-sources").Severity,
		"the graded severity rides on the actionable finding")
	assert.Contains(t, findFinding(t, report, "temp-file-rate").Name, "50.0 files/hour")
}

func TestTempUsage_HighVolumeRate_Warning(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	const oneGB = 1024 * 1024 * 1024
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	row := makeTempUsageRow(
		100,                 // low file count
		50*oneGB,            // 50GB total
		oneHourInSeconds*24, // 24 hours
		4.2,                 // 4.2 files/hour
		2*oneGB,             // 2GB/hour (>= 1GB, < 5GB) -> WARN
		&statsReset,
	)

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	volumeFinding := findFinding(t, report, "temp-volume-rate")
	assert.Equal(t, check.SeverityInfo, volumeFinding.Severity)
	assert.Contains(t, volumeFinding.Name, "2.0GiB/hour")
}

func TestTempUsage_HighVolumeRate_Critical(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	const oneGB = 1024 * 1024 * 1024
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	row := makeTempUsageRow(
		100,                 // low file count
		200*oneGB,           // 200GB total
		oneHourInSeconds*24, // 24 hours
		4.2,                 // 4.2 files/hour
		8*oneGB,             // 8GB/hour (>= 5GB) -> FAIL
		&statsReset,
	)

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	volumeFinding := findFinding(t, report, "temp-volume-rate")
	assert.Equal(t, check.SeverityInfo, volumeFinding.Severity)
	assert.Contains(t, volumeFinding.Name, "8.0GiB/hour")
	assert.Equal(t, check.SeverityFail, findFinding(t, report, "temp-file-sources").Severity)
}

func TestTempUsage_BothHighRates(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	const oneGB = 1024 * 1024 * 1024
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	row := makeTempUsageRow(
		20000,               // many temp files
		150*oneGB,           // 150GB total
		oneHourInSeconds*24, // 24 hours
		833.0,               // 833 files/hour (>= 20) -> FAIL
		6.25*oneGB,          // 6.25GB/hour (>= 5GB) -> FAIL
		&statsReset,
	)

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Len(t, report.Results, 3, "two rate findings plus the attribution finding")

	// The rates are context; the attribution finding carries the graded severity.
	assert.Equal(t, check.SeverityInfo, findFinding(t, report, "temp-file-rate").Severity)
	assert.Equal(t, check.SeverityInfo, findFinding(t, report, "temp-volume-rate").Severity)
	assert.Equal(t, check.SeverityFail, findFinding(t, report, "temp-file-sources").Severity)
}

func TestTempUsage_EdgeCases_ExactThresholds(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	const oneGB = 1024 * 1024 * 1024
	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	tests := []struct {
		name                       string
		filesPerHour               float64
		bytesPerHour               float64
		expectedFileRateSeverity   check.Severity
		expectedVolumeRateSeverity check.Severity
	}{
		{
			name:                       "exactly 5 files/hour - warning threshold",
			filesPerHour:               5.0,
			bytesPerHour:               100 * 1024 * 1024, // 100MB
			expectedFileRateSeverity:   check.SeverityWarn,
			expectedVolumeRateSeverity: check.SeverityPass,
		},
		{
			name:                       "exactly 20 files/hour - critical threshold",
			filesPerHour:               20.0,
			bytesPerHour:               100 * 1024 * 1024,
			expectedFileRateSeverity:   check.SeverityFail,
			expectedVolumeRateSeverity: check.SeverityPass,
		},
		{
			name:                       "exactly 1GB/hour - warning threshold",
			filesPerHour:               4.0,
			bytesPerHour:               float64(oneGB),
			expectedFileRateSeverity:   check.SeverityPass,
			expectedVolumeRateSeverity: check.SeverityWarn,
		},
		{
			name:                       "exactly 5GB/hour - critical threshold",
			filesPerHour:               4.0,
			bytesPerHour:               float64(5 * oneGB),
			expectedFileRateSeverity:   check.SeverityPass,
			expectedVolumeRateSeverity: check.SeverityFail,
		},
		{
			name:                       "just below 5 files/hour - OK",
			filesPerHour:               4.99,
			bytesPerHour:               100 * 1024 * 1024,
			expectedFileRateSeverity:   check.SeverityPass,
			expectedVolumeRateSeverity: check.SeverityPass,
		},
		{
			name:                       "just below 1GB/hour - OK",
			filesPerHour:               4.0,
			bytesPerHour:               float64(oneGB) - 1,
			expectedFileRateSeverity:   check.SeverityPass,
			expectedVolumeRateSeverity: check.SeverityPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			row := makeTempUsageRow(
				1000,
				int64(tt.bytesPerHour*24), // Scale up to 24 hours
				oneHourInSeconds*24,
				tt.filesPerHour,
				tt.bytesPerHour,
				&statsReset,
			)

			queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
			checker := tempusage.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)

			// The thresholds still decide the outcome, they just land on the report
			// via the attribution finding rather than on the rate findings.
			want := tt.expectedFileRateSeverity
			if tt.expectedVolumeRateSeverity > want {
				want = tt.expectedVolumeRateSeverity
			}
			assert.Equal(t, want, report.Severity, "threshold outcome")
		})
	}
}

func TestTempUsage_InvalidNumeric(t *testing.T) {
	t.Parallel()

	now := time.Now()
	statsReset := now.Add(-24 * time.Hour)

	// Row with invalid Numeric values
	row := db.TempUsageRow{
		DatabaseName:      pgtype.Text{String: "test_db", Valid: true},
		TempFiles:         pgtype.Int8{Int64: 100, Valid: true},
		TempBytes:         pgtype.Int8{Int64: 1000000, Valid: true},
		SecondsSinceReset: pgtype.Numeric{Valid: false}, // Invalid
		TempFilesPerHour:  pgtype.Numeric{Valid: false}, // Invalid
		TempBytesPerHour:  pgtype.Numeric{Valid: false}, // Invalid
		StatsReset:        pgtype.Timestamptz{Time: statsReset, Valid: true},
	}

	queryer := &mockQueryer{row: row, pgssOK: true, statements: oneStatement()}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	// An unreadable seconds_since_reset leaves the window unknown, same as NULL.
	assert.Equal(t, check.SeveritySkip, report.Severity)
	assert.Len(t, report.Results, 1)
	assert.Contains(t, report.Results[0].Details, "Counter window unknown")
}

func TestTempUsage_Metadata(t *testing.T) {
	t.Parallel()

	queryer := &mockQueryer{}
	checker := tempusage.New(queryer)

	metadata := checker.Metadata()
	assert.Equal(t, "temp-usage", metadata.CheckID)
	assert.Equal(t, "Temporary File Usage", metadata.Name)
	assert.Equal(t, check.CategoryConfigs, metadata.Category)
	assert.NotEmpty(t, metadata.SQL)
	assert.NotEmpty(t, metadata.Readme)
	assert.NotEmpty(t, metadata.Description)
}

// With no recorded reset the window is anchored to server uptime, which is only a
// lower bound, so the rates are upper bounds. A rate over the threshold may just be
// a long history divided by a short uptime, so it must not reach FAIL - but a rate
// under it is still conclusive and must still PASS.
func TestTempUsage_LowerBoundWindowCapsSeverity(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0

	tests := []struct {
		name             string
		filesPerHour     float64
		bytesPerHour     float64
		expectedSeverity check.Severity
	}{
		{"rate in FAIL band is capped to WARN", 50.0, 20 * 1024 * 1024, check.SeverityWarn},
		{"rate in WARN band stays WARN", 10.0, 20 * 1024 * 1024, check.SeverityWarn},
		{"rate below thresholds still passes", 1.0, 1024, check.SeverityPass},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// nil statsReset => window_is_lower_bound
			row := makeTempUsageRow(12000, 500*1024*1024, oneHourInSeconds*24, tt.filesPerHour, tt.bytesPerHour, nil)

			report, err := tempusage.New(&mockQueryer{row: row, pgssOK: true, statements: oneStatement()}).Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSeverity, report.Severity)
			assert.GreaterOrEqual(t, len(report.Results), 2, "both rate subchecks must run")
		})
	}
}

// The same rate with a recorded reset is an exact measurement, so FAIL is available.
func TestTempUsage_ExactWindowAllowsFail(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0

	statsReset := time.Now().Add(-24 * time.Hour)
	row := makeTempUsageRow(12000, 500*1024*1024, oneHourInSeconds*24, 50.0, 20*1024*1024, &statsReset)

	report, err := tempusage.New(&mockQueryer{row: row, pgssOK: true, statements: oneStatement()}).Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)
}

// Attribution runs only when a rate finding fired: reading pg_stat_statements
// materialises the whole query-text corpus, so a healthy database must not pay for it.
func TestTempUsage_AttributionOnlyWhenRatesWarn(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	statsReset := time.Now().Add(-24 * time.Hour)

	healthy := makeTempUsageRow(10, 1024, oneHourInSeconds*24, 1.0, 1024, &statsReset)
	report, err := tempusage.New(&mockQueryer{row: healthy, pgssOK: true}).Check(context.Background())
	require.NoError(t, err)
	require.Len(t, report.Results, 2, "no attribution finding on a healthy database")
}

func TestTempUsage_AttributionTable(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	statsReset := time.Now().Add(-24 * time.Hour)
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	row := makeTempUsageRow(12000, 500*1024*1024, oneHourInSeconds*24, 50.0, 20*1024*1024, &statsReset)
	queryer := &mockQueryer{
		row:    row,
		pgssOK: true,
		statements: []db.TempUsageByStatementRow{
			{
				Queryid:          pgtype.Int8{Int64: 42, Valid: true},
				Calls:            pgtype.Int8{Int64: 1200, Valid: true},
				TempBytesWritten: pgtype.Int8{Int64: 4 * 1024 * 1024 * 1024, Valid: true},
				EntrySince:       pgtype.Timestamptz{Time: since, Valid: true},
				QueryText:        pgtype.Text{String: "select * from bookings order by created_at", Valid: true},
			},
			{
				// PG15/16 have no stats_since column.
				Queryid:          pgtype.Int8{Int64: 7, Valid: true},
				Calls:            pgtype.Int8{Int64: 90, Valid: true},
				TempBytesWritten: pgtype.Int8{Int64: 1024 * 1024, Valid: true},
				EntrySince:       pgtype.Timestamptz{Valid: false},
				QueryText:        pgtype.Text{String: "select 1", Valid: true},
			},
		},
	}

	report, err := tempusage.New(queryer).Check(context.Background())
	require.NoError(t, err)

	var sources *check.Finding
	for i := range report.Results {
		if report.Results[i].ID == "temp-file-sources" {
			sources = &report.Results[i]
		}
	}

	require.NotNil(t, sources, "attribution finding must be present")
	require.Equal(t, check.SeverityFail, sources.Severity, "the actionable finding carries the graded severity")
	require.NotNil(t, sources.Table)
	require.Len(t, sources.Table.Rows, 2)
	require.Equal(t, []string{"Temp Written", "Calls", "Tracked Since", "Query ID", "Query"}, sources.Table.Headers)
	require.Equal(t, "4.0GiB", sources.Table.Rows[0].Cells[0])
	require.Equal(t, "2026-05-01", sources.Table.Rows[0].Cells[2])
	require.Equal(t, "-", sources.Table.Rows[1].Cells[2], "missing stats_since renders as a dash")
	// The report severity still comes from the rate findings, not from this one.
	require.Equal(t, check.SeverityFail, report.Severity)
}

// With nothing attributable the check says nothing: the rate findings have already
// reported the problem, and a line explaining that pg_stat_statements cannot name the
// offender reads as a denial that any temp file was written.
func TestTempUsage_NoAttributionFindingWhenNothingToShow(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	statsReset := time.Now().Add(-24 * time.Hour)
	row := makeTempUsageRow(12000, 500*1024*1024, oneHourInSeconds*24, 50.0, 20*1024*1024, &statsReset)

	for _, tc := range []struct {
		name    string
		queryer *mockQueryer
	}{
		{"pg_stat_statements unavailable", &mockQueryer{row: row, pgssOK: false}},
		{"no statement wrote temp files", &mockQueryer{row: row, pgssOK: true, statements: nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			report, err := tempusage.New(tc.queryer).Check(context.Background())
			require.NoError(t, err)

			for _, result := range report.Results {
				require.NotEqual(t, "temp-file-sources", result.ID, "no attribution finding expected")
			}
		})
	}
}

// An unattributable spill is not a quiet one. pg_stat_statements records at
// ExecutorEnd, so a statement killed by statement_timeout never appears there while
// its temp file is still counted - which makes "cannot attribute" a signal that the
// offender was expensive, not that nothing is wrong. The rate keeps the severity.
func TestTempUsage_UnattributableSpillKeepsTheSeverity(t *testing.T) {
	t.Parallel()

	const oneHourInSeconds = 3600.0
	statsReset := time.Now().Add(-24 * time.Hour)
	row := makeTempUsageRow(12000, 500*1024*1024, oneHourInSeconds*24, 1.0, 2*1024*1024*1024, &statsReset)

	for _, tc := range []struct {
		name    string
		queryer *mockQueryer
	}{
		{"pg_stat_statements unavailable", &mockQueryer{row: row, pgssOK: false}},
		{"no statement recorded any temp", &mockQueryer{row: row, pgssOK: true, statements: nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			report, err := tempusage.New(tc.queryer).Check(context.Background())
			require.NoError(t, err)

			volume := findFinding(t, report, "temp-volume-rate")
			require.Equal(t, check.SeverityWarn, volume.Severity, "the signal must survive")
			require.Equal(t, check.SeverityWarn, report.Severity)
			require.Contains(t, volume.Details, "log_temp_files")

			for _, result := range report.Results {
				require.NotEqual(t, "temp-file-sources", result.ID)
			}
		})
	}
}
