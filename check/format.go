package check

import (
	"fmt"

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
