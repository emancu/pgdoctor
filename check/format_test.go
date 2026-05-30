package check_test

import (
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/stretchr/testify/require"
)

func TestParseDurationMs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		baseUnit string
		expect   int64
	}{
		{name: "ms suffix", value: "2000ms", expect: 2000},
		{name: "seconds suffix", value: "2s", expect: 2000},
		{name: "minutes suffix", value: "1min", expect: 60000},
		{name: "bare number defaults to ms", value: "2000", expect: 2000},
		{name: "zero", value: "0", expect: 0},
		{name: "disabled sentinel keeps sign", value: "-1", expect: -1},
		{name: "fractional seconds", value: "1.5s", expect: 1500},
		{name: "space before unit", value: "2000 ms", expect: 2000},
		{name: "bare number with ms base unit", value: "2000", baseUnit: "ms", expect: 2000},
		{name: "microseconds round to nearest ms", value: "1500us", expect: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := check.ParseDurationMs(tt.value, tt.baseUnit)
			require.NoError(t, err)
			require.Equal(t, tt.expect, got)
		})
	}
}

func TestParseDurationMs_Invalid(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "  ", "abc", "12x3"} {
		_, err := check.ParseDurationMs(value, "ms")
		require.Error(t, err, "expected error for %q", value)
	}
}
