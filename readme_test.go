package pgdoctor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	checkIDPattern   = regexp.MustCompile(`CheckID:\s+"([^"]+)"`)
	findingIDPattern = regexp.MustCompile(`(\w*)ID:\s+("[^"]+"|[\w.]+),`)
	headingPattern   = regexp.MustCompile("(?m)^### For `([^`]+)`")
)

// Operators grep a check README for the finding ID that the run output prints.
func TestReadmeHasHeadingForEveryFindingID(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("checks/*/check.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		dir := filepath.Dir(file)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()

			src, err := os.ReadFile(file)
			require.NoError(t, err)
			readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
			require.NoError(t, err)

			emitted := findingIDs(t, string(src))
			require.NotEmpty(t, emitted)

			headings := map[string]int{}
			for _, m := range headingPattern.FindAllStringSubmatch(string(readme), -1) {
				headings[m[1]]++
			}

			for id := range emitted {
				assert.Equal(t, 1, headings[id], "README needs exactly one \"### For `%s`\" heading", id)
			}
			for id := range headings {
				assert.True(t, emitted[id], "README has a heading for %q, which the check does not emit", id)
			}
		})
	}
}

// findingIDPattern also matches `CheckID:` and struct fields such as
// `dbFindingID:`. A selector value such as c.dbFindingID is skipped, because the
// struct literal that sets that field supplies the ID.
func findingIDs(t *testing.T, src string) map[string]bool {
	t.Helper()

	m := checkIDPattern.FindStringSubmatch(src)
	require.NotNil(t, m, "no CheckID in check.go")
	checkID := m[1]

	ids := map[string]bool{}
	for _, m := range findingIDPattern.FindAllStringSubmatch(src, -1) {
		if m[1] == "Check" {
			continue
		}
		expr := m[2]
		switch {
		case strings.HasPrefix(expr, `"`):
			ids[strings.Trim(expr, `"`)] = true
		case expr == "report.CheckID":
			ids[checkID] = true
		case strings.Contains(expr, "."):
		default:
			c := regexp.MustCompile(`\b` + expr + `\s+=\s+"([^"]+)"`).FindStringSubmatch(src)
			require.NotNil(t, c, "cannot resolve finding ID %s", expr)
			ids[c[1]] = true
		}
	}
	return ids
}
