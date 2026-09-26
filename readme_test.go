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
	severityPattern  = regexp.MustCompile(`Severity:\s+([\w.]+)`)
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

			for id, required := range emitted {
				if required {
					assert.Equal(t, 1, headings[id], "README needs exactly one \"### For `%s`\" heading", id)
				}
			}
			for id, n := range headings {
				_, ok := emitted[id]
				assert.True(t, ok, "README has a heading for %q, which the check does not emit", id)
				assert.Equal(t, 1, n, "README has more than one \"### For `%s`\" heading", id)
			}
		})
	}
}

// findingIDPattern also matches `CheckID:` and struct fields such as
// `dbFindingID:`. A selector value such as c.dbFindingID is skipped, because the
// struct literal that sets that field supplies the ID.
//
// The map value is true when the ID needs a heading: the first `Severity:`
// after some occurrence of the ID is neither PASS nor SKIP. A severity held in
// a variable counts as needing a heading.
func findingIDs(t *testing.T, src string) map[string]bool {
	t.Helper()

	m := checkIDPattern.FindStringSubmatch(src)
	require.NotNil(t, m, "no CheckID in check.go")
	checkID := m[1]

	ids := map[string]bool{}
	for _, loc := range findingIDPattern.FindAllStringSubmatchIndex(src, -1) {
		if src[loc[2]:loc[3]] == "Check" {
			continue
		}
		var id string
		expr := src[loc[4]:loc[5]]
		switch {
		case strings.HasPrefix(expr, `"`):
			id = strings.Trim(expr, `"`)
		case expr == "report.CheckID":
			id = checkID
		case strings.Contains(expr, "."):
			continue
		default:
			c := regexp.MustCompile(`\b` + expr + `\s+=\s+"([^"]+)"`).FindStringSubmatch(src)
			require.NotNil(t, c, "cannot resolve finding ID %s", expr)
			id = c[1]
		}
		sev := severityPattern.FindStringSubmatch(src[loc[1]:])
		quiet := sev != nil && (sev[1] == "check.SeverityPass" || sev[1] == "check.SeveritySkip")
		ids[id] = ids[id] || !quiet
	}
	return ids
}
