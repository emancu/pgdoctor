package pgversion_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/pgversion"
	"github.com/emancu/pgdoctor/db"
)

func newStaticVersioner(row db.PGVersionRow) pgversion.VersionQueries {
	return pgVersioner(func() db.PGVersionRow {
		return row
	})
}

type pgVersioner func() db.PGVersionRow

func (f pgVersioner) PGVersion(context.Context) (db.PGVersionRow, error) {
	return f(), nil
}

func Test_Version(t *testing.T) {
	t.Parallel()

	type testCase struct {
		Name             string
		Row              db.PGVersionRow
		ExpectedSeverity check.Severity
	}

	testCases := []testCase{
		{
			Name:             "PG 13 - end of life",
			Row:              db.PGVersionRow{Major: 13, Minor: 0},
			ExpectedSeverity: check.SeverityFail,
		},
		{
			Name:             "PG 14 - approaching EOL",
			Row:              db.PGVersionRow{Major: 14, Minor: 0},
			ExpectedSeverity: check.SeverityWarn,
		},
		{
			Name:             "PG 15 - supported",
			Row:              db.PGVersionRow{Major: 15, Minor: 0},
			ExpectedSeverity: check.SeverityOK,
		},
		{
			Name:             "PG 16 - supported",
			Row:              db.PGVersionRow{Major: 16, Minor: 0},
			ExpectedSeverity: check.SeverityOK,
		},
		{
			Name:             "PG 17 - current",
			Row:              db.PGVersionRow{Major: 17, Minor: 0},
			ExpectedSeverity: check.SeverityOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			versioner := newStaticVersioner(tc.Row)
			checker := pgversion.New(versioner)

			report, err := checker.Check(context.Background())
			require.NoError(t, err)

			results := report.Results
			require.Equal(t, 1, len(results))
			require.Equal(t, tc.ExpectedSeverity, results[0].Severity)
		})
	}
}
