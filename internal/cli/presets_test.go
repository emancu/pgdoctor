package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestApplyPreset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		preset      string
		only        []string
		want        []string
		wantWarning string
	}{
		{"all keeps only", presetAll, []string{"indexes"}, []string{"indexes"}, ""},
		{"all without only", presetAll, nil, nil, ""},
		{"triage without only", presetTriage, nil, triageChecks, ""},
		{"triage ignores only", presetTriage, []string{"indexes"}, triageChecks, "Warning: --preset triage ignores --only\n"},
		{"unknown preset keeps only", "bogus", []string{"indexes"}, []string{"indexes"}, "Warning: ignoring unknown preset \"bogus\" (valid presets: all, triage)\n"},
		{"unknown preset runs all", "bogus", nil, nil, "Warning: ignoring unknown preset \"bogus\" (valid presets: all, triage)\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			got := applyPreset(&stderr, tt.preset, tt.only)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarning, stderr.String())
		})
	}
}
