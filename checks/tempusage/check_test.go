package tempusage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/tempusage"
	"github.com/emancu/pgdoctor/db"
)

type mockQueryer struct {
	row db.TempUsageRow
	err error
}

func (m *mockQueryer) TempUsage(ctx context.Context) (db.TempUsageRow, error) {
	return m.row, m.err
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
			assert.Equal(t, check.SeverityOK, report.Severity)
			assert.Len(t, report.Results, 1)
			assert.Equal(t, "temp-usage", report.Results[0].ID)
			assert.Contains(t, report.Results[0].Details, "Statistics reset too recently")
			assert.Contains(t, report.Results[0].Details, "Need at least 1 hour of data")
		})
	}
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 2)

	// Both subchecks should be OK
	assert.Equal(t, "temp-file-rate", report.Results[0].ID)
	assert.Equal(t, check.SeverityOK, report.Results[0].Severity)
	assert.Contains(t, report.Results[0].Details, "acceptable")

	assert.Equal(t, "temp-volume-rate", report.Results[1].ID)
	assert.Equal(t, check.SeverityOK, report.Results[1].Severity)
	assert.Contains(t, report.Results[1].Details, "acceptable")
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	fileRateFinding := report.Results[0]
	assert.Equal(t, "temp-file-rate", fileRateFinding.ID)
	assert.Equal(t, check.SeverityWarn, fileRateFinding.Severity)
	assert.Contains(t, fileRateFinding.Details, "High temp file creation rate")
	assert.Contains(t, fileRateFinding.Details, "10.0 files/hour")
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	fileRateFinding := report.Results[0]
	assert.Equal(t, "temp-file-rate", fileRateFinding.ID)
	assert.Equal(t, check.SeverityFail, fileRateFinding.Severity)
	assert.Contains(t, fileRateFinding.Details, "High temp file creation rate")
	assert.Contains(t, fileRateFinding.Details, "50.0 files/hour")
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)

	volumeFinding := report.Results[1]
	assert.Equal(t, "temp-volume-rate", volumeFinding.ID)
	assert.Equal(t, check.SeverityWarn, volumeFinding.Severity)
	assert.Contains(t, volumeFinding.Details, "High temp data volume")
	assert.Contains(t, volumeFinding.Details, "2.0GiB/hour")
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)

	volumeFinding := report.Results[1]
	assert.Equal(t, "temp-volume-rate", volumeFinding.ID)
	assert.Equal(t, check.SeverityFail, volumeFinding.Severity)
	assert.Contains(t, volumeFinding.Details, "High temp data volume")
	assert.Contains(t, volumeFinding.Details, "8.0GiB/hour")
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	assert.Equal(t, check.SeverityFail, report.Severity)
	assert.Len(t, report.Results, 2)

	// Both should be FAIL
	assert.Equal(t, check.SeverityFail, report.Results[0].Severity)
	assert.Equal(t, check.SeverityFail, report.Results[1].Severity)
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
			expectedVolumeRateSeverity: check.SeverityOK,
		},
		{
			name:                       "exactly 20 files/hour - critical threshold",
			filesPerHour:               20.0,
			bytesPerHour:               100 * 1024 * 1024,
			expectedFileRateSeverity:   check.SeverityFail,
			expectedVolumeRateSeverity: check.SeverityOK,
		},
		{
			name:                       "exactly 1GB/hour - warning threshold",
			filesPerHour:               4.0,
			bytesPerHour:               float64(oneGB),
			expectedFileRateSeverity:   check.SeverityOK,
			expectedVolumeRateSeverity: check.SeverityWarn,
		},
		{
			name:                       "exactly 5GB/hour - critical threshold",
			filesPerHour:               4.0,
			bytesPerHour:               float64(5 * oneGB),
			expectedFileRateSeverity:   check.SeverityOK,
			expectedVolumeRateSeverity: check.SeverityFail,
		},
		{
			name:                       "just below 5 files/hour - OK",
			filesPerHour:               4.99,
			bytesPerHour:               100 * 1024 * 1024,
			expectedFileRateSeverity:   check.SeverityOK,
			expectedVolumeRateSeverity: check.SeverityOK,
		},
		{
			name:                       "just below 1GB/hour - OK",
			filesPerHour:               4.0,
			bytesPerHour:               float64(oneGB) - 1,
			expectedFileRateSeverity:   check.SeverityOK,
			expectedVolumeRateSeverity: check.SeverityOK,
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

			queryer := &mockQueryer{row: row}
			checker := tempusage.New(queryer)
			report, err := checker.Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.expectedFileRateSeverity, report.Results[0].Severity, "file rate severity")
			assert.Equal(t, tt.expectedVolumeRateSeverity, report.Results[1].Severity, "volume rate severity")
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

	queryer := &mockQueryer{row: row}
	checker := tempusage.New(queryer)
	report, err := checker.Check(context.Background())

	require.NoError(t, err)
	// Should treat invalid numerics as 0 and report stats too recent
	assert.Equal(t, check.SeverityOK, report.Severity)
	assert.Len(t, report.Results, 1)
	assert.Contains(t, report.Results[0].Details, "Statistics reset too recently")
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
