// Package main generates the docs/ directory for pgdoctor's GitHub Pages landing page.
// It reads check metadata from the Go runtime (via AllChecks()) and produces:
//   - docs/checks.json — a JSON manifest of all checks
//   - docs/checks/*.md — individual README files per check
//   - docs/logo.png — copied from repo root
//   - docs/index.html — copied from this package's template
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/emancu/pgdoctor"
)

type checkEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

type checksManifest struct {
	Checks []checkEntry `json:"checks"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gendocs: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return fmt.Errorf("finding repo root: %w", err)
	}

	docsDir := filepath.Join(repoRoot, "docs")
	checksDir := filepath.Join(docsDir, "checks")

	// Create output directories
	if err := os.MkdirAll(checksDir, 0o755); err != nil {
		return fmt.Errorf("creating docs/checks: %w", err)
	}

	// Gather check metadata from the Go runtime
	allChecks := pgdoctor.AllChecks()
	manifest := checksManifest{Checks: make([]checkEntry, 0, len(allChecks))}
	generated := make(map[string]bool, len(allChecks))

	for _, pkg := range allChecks {
		meta := pkg.Metadata()

		manifest.Checks = append(manifest.Checks, checkEntry{
			ID:          meta.CheckID,
			Name:        meta.Name,
			Category:    string(meta.Category),
			Description: meta.Description,
		})

		// Write individual README markdown
		mdName := meta.CheckID + ".md"
		generated[mdName] = true

		mdPath := filepath.Join(checksDir, mdName)
		if err := os.WriteFile(mdPath, []byte(meta.Readme), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", mdPath, err)
		}
	}

	if err := pruneStaleDocs(checksDir, generated); err != nil {
		return fmt.Errorf("pruning stale check docs: %w", err)
	}

	// Write checks.json manifest
	jsonData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling checks.json: %w", err)
	}
	jsonData = append(jsonData, '\n')

	jsonPath := filepath.Join(docsDir, "checks.json")
	if err := os.WriteFile(jsonPath, jsonData, 0o644); err != nil {
		return fmt.Errorf("writing checks.json: %w", err)
	}

	// Copy logo.png from repo root to docs/
	if err := copyFile(filepath.Join(repoRoot, "logo.png"), filepath.Join(docsDir, "logo.png")); err != nil {
		return fmt.Errorf("copying logo.png: %w", err)
	}

	// Copy index.html template from this package to docs/
	genDocsDir := filepath.Join(repoRoot, "internal", "gendocs")
	if err := copyFile(filepath.Join(genDocsDir, "index.html"), filepath.Join(docsDir, "index.html")); err != nil {
		return fmt.Errorf("copying index.html: %w", err)
	}

	fmt.Fprintf(os.Stdout, "✓ Generated docs/ with %d checks\n", len(allChecks))
	return nil
}

// pruneStaleDocs removes docs/checks/*.md files that no current check produced.
//
// Generation used to only ever write, so deleting or renaming a check left its
// markdown behind indefinitely. docs/index.html fetches checks/<id>.md by name, so
// an orphan stays reachable and serves documentation for a check that no longer
// exists — which is how docs/checks/dev-indexes.md outlived its removal.
func pruneStaleDocs(checksDir string, generated map[string]bool) error {
	entries, err := os.ReadDir(checksDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".md" || generated[name] {
			continue
		}

		if err := os.Remove(filepath.Join(checksDir, name)); err != nil {
			return err
		}

		fmt.Fprintf(os.Stdout, "  removed stale docs/checks/%s\n", name)
	}

	return nil
}

// findRepoRoot finds the repository root by looking for go.mod.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find go.mod in any parent directory")
		}
		dir = parent
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
