package cli

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

func getPresetChecks(preset string) []string {
	switch preset {
	case presetTriage:
		return triageChecks
	default:
		return nil
	}
}

func intersect(a, b []string) []string {
	bMap := make(map[string]struct{}, len(b))
	for _, item := range b {
		bMap[item] = struct{}{}
	}

	var result []string
	for _, item := range a {
		if _, exists := bMap[item]; exists {
			result = append(result, item)
		}
	}
	return result
}
