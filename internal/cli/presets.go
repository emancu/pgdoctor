package cli

import (
	"fmt"
	"io"
)

const (
	presetAll    = "all"
	presetTriage = "triage"
)

var triageChecks = []string{
	"connection-health",
	"connection-efficiency",
	"replication-lag",
	"replication-slots",
	"table-bloat",
	"table-vacuum-health",
	"freeze-age",
	"invalid-indexes",
	"temp-usage",
	"cache-efficiency",
}

func applyPreset(w io.Writer, preset string, only []string) []string {
	switch preset {
	case presetAll:
		return only
	case presetTriage:
		if len(only) > 0 {
			fmt.Fprintf(w, "Warning: --preset %s ignores --only\n", preset)
		}
		return triageChecks
	default:
		fmt.Fprintf(w, "Warning: ignoring unknown preset %q (valid presets: %s, %s)\n", preset, presetAll, presetTriage)
		return only
	}
}
