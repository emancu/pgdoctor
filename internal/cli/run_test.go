package cli

import (
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/stretchr/testify/require"
)

func TestRunExitError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		maxSeverity check.Severity
		wantExit    int // 0 means a nil error (exit 0)
	}{
		{"text fail exits 1", "text", check.SeverityFail, 1},
		{"text warn exits 0", "text", check.SeverityWarn, 0},
		{"text ok exits 0", "text", check.SeverityOK, 0},
		// JSON always exits 0 after a successful encode, even on failing checks —
		// consumers read pass/fail from the document. Regression guard for the
		// pre-renderer behavior.
		{"json fail exits 0", "json", check.SeverityFail, 0},
		{"json ok exits 0", "json", check.SeverityOK, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := runExitError(tt.output, tt.maxSeverity)
			if tt.wantExit == 0 {
				require.NoError(t, err)
				return
			}

			se, ok := err.(*SilentError)
			require.True(t, ok, "expected *SilentError, got %T", err)
			require.Equal(t, tt.wantExit, se.ExitCode)
		})
	}
}
