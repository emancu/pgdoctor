package check_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/emancu/pgdoctor/check"
)

// Severity values are part of the public API: consumers compare and persist
// them, so renumbering is a breaking change even if names stay the same.
func TestSeverity_Values(t *testing.T) {
	t.Parallel()

	require.Equal(t, check.Severity(-2), check.SeverityInfo)
	require.Equal(t, check.Severity(-1), check.SeveritySkip)
	require.Equal(t, check.Severity(1), check.SeverityPass)
	require.Equal(t, check.Severity(2), check.SeverityWarn)
	require.Equal(t, check.Severity(3), check.SeverityFail)
}

func TestSeverity_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		severity check.Severity
		expect   string
	}{
		{name: "info", severity: check.SeverityInfo, expect: "info"},
		{name: "skip", severity: check.SeveritySkip, expect: "skip"},
		{name: "pass", severity: check.SeverityPass, expect: "pass"},
		{name: "warn", severity: check.SeverityWarn, expect: "warn"},
		{name: "fail", severity: check.SeverityFail, expect: "fail"},
		{name: "zero value is unset", severity: check.Severity(0), expect: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expect, tt.severity.String())
		})
	}
}
