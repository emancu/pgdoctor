package check

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// Byte size constants (IEC binary units).
const (
	KiB = 1024
	MiB = KiB * 1024
	GiB = MiB * 1024
)

// FormatBytes formats a byte count as a human-readable string (e.g., "1.5GiB").
// Supports from bytes up to exbibytes (EiB) for large database objects.
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatNumber formats a large number as a human-readable string (e.g., "1.5M").
func FormatNumber(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// FormatDurationMs formats milliseconds as a human-readable duration (e.g., "1.5h").
func FormatDurationMs(ms float64) string {
	if ms >= 3600000 {
		return fmt.Sprintf("%.1fh", ms/3600000)
	}
	if ms >= 60000 {
		return fmt.Sprintf("%.1fm", ms/60000)
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

// timeUnitFactors maps a PostgreSQL GUC time-unit suffix to its millisecond
// multiplier. PostgreSQL recognises only these lowercase, case-sensitive
// suffixes; there is no bare "m" time unit ("min" is the only minute spelling).
var timeUnitFactors = map[string]float64{
	"us":  1.0 / 1000,
	"ms":  1,
	"s":   1000,
	"min": 60000,
	"h":   3600000,
	"d":   86400000,
}

// ParseDurationMs parses a PostgreSQL GUC time value into integer milliseconds —
// the inverse of FormatDurationMs. Unlike pg_settings.setting (always normalised
// to the base unit), raw ALTER ROLE / pg_db_role_setting values are stored
// verbatim, so they may carry a unit suffix ("2000ms", "1.5s", "2000 ms") or be a
// bare number interpreted in baseUnit. The sign is preserved so sentinel values
// like -1 (disabled) survive.
func ParseDurationMs(value, baseUnit string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty value")
	}

	// Match the longest trailing alphabetic suffix that is a known time unit.
	numeric, factor := value, durationUnitFactor(baseUnit)
	for i := len(value) - 1; i >= 0; i-- {
		if !isASCIILetter(value[i]) {
			suffix := value[i+1:]
			if f, ok := timeUnitFactors[suffix]; ok {
				numeric = strings.TrimSpace(value[:i+1])
				factor = f
			}
			break
		}
	}

	n, err := strconv.ParseFloat(numeric, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing numeric part %q: %w", numeric, err)
	}

	return int64(math.Round(n * factor)), nil
}

// durationUnitFactor returns the ms multiplier for a value's base unit,
// defaulting to milliseconds when the unit is empty or not a known time unit.
func durationUnitFactor(unit string) float64 {
	if f, ok := timeUnitFactors[unit]; ok {
		return f
	}
	return 1
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// FormatDurationSec formats seconds as a human-readable duration (e.g., "2h" or "1d").
func FormatDurationSec(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	return fmt.Sprintf("%dd", seconds/86400)
}

// NumericToFloat64 converts pgtype.Numeric to float64, returning 0 if invalid.
func NumericToFloat64(n pgtype.Numeric) float64 {
	if !n.Valid {
		return 0
	}
	f, _ := n.Float64Value()
	return f.Float64
}

// NumericToInt64 converts pgtype.Numeric to int64, returning 0 if invalid.
func NumericToInt64(n pgtype.Numeric) int64 {
	return int64(NumericToFloat64(n))
}

// Float8ToFloat64 converts pgtype.Float8 to float64, returning 0 if invalid.
func Float8ToFloat64(f pgtype.Float8) float64 {
	if !f.Valid {
		return 0
	}
	return f.Float64
}

// Int8ToInt64 converts pgtype.Int8 to int64, returning 0 if invalid.
func Int8ToInt64(i pgtype.Int8) int64 {
	if !i.Valid {
		return 0
	}
	return i.Int64
}
