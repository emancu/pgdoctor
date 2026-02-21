// Package pgdoctor implements health checks for common
// misconfiguration and issues of PostgreSQL databases.
package pgdoctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

const checkTimeout = 2 * time.Second

// Run is the entrypoint to executing pgdoctor's checks.
// Returns all check reports with their metadata and results.
// Use AllChecks() for the built-in set, or append contrib checks for custom runs.
func Run(ctx context.Context, conn db.DBTX, checks []check.Package, only, ignored []string) ([]*check.Report, error) {
	ignoredMap := map[string]struct{}{}
	for _, ignore := range ignored {
		ignoredMap[ignore] = struct{}{}
	}
	onlyMap := map[string]struct{}{}
	for _, o := range only {
		onlyMap[o] = struct{}{}
	}

	var allReports []*check.Report

	for _, pkg := range checks {
		// Instantiate the checker for this check
		checker := pkg.New(conn)
		if !shouldRunCheck(checker, onlyMap, ignoredMap) {
			continue
		}

		// Create timeout context for this check
		checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
		report, err := checker.Check(checkCtx)
		cancel() // Release resources immediately

		if err != nil {
			metadata := checker.Metadata()
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, fmt.Errorf("check %s/%s timed out after %s",
					metadata.Category, metadata.CheckID, checkTimeout)
			}
			return nil, err
		}

		allReports = append(allReports, report)
	}

	return allReports, nil
}

// ValidateFilters normalizes filter strings and validates them against available checks.
// Returns valid filters (normalized to check IDs and categories) and invalid filters.
//
// Normalization:
//   - "check-id" -> "check-id" (exact match)
//   - "check-id/subcheck-id" -> "check-id" (extracts check ID from subcheck)
//   - "category" -> "category" (exact match)
//
// Invalid filters are those that don't match any check ID or category.
func ValidateFilters(checks []check.Package, filters []string) ([]string, []string) {
	var valid, invalid []string

	// Build set of valid check IDs and categories
	validCheckIDs := map[string]struct{}{}
	validCategories := map[string]struct{}{}

	for _, pkg := range checks {
		metadata := pkg.Metadata()
		validCheckIDs[metadata.CheckID] = struct{}{}
		validCategories[string(metadata.Category)] = struct{}{}
	}

	// Track seen filters to avoid duplicates
	seen := map[string]struct{}{}

	for _, filter := range filters {
		// Normalize: extract check ID from subcheck format (check-id/subcheck-id)
		normalized := filter
		if strings.Contains(filter, "/") {
			parts := strings.SplitN(filter, "/", 2)
			normalized = parts[0]
		}

		// Check if normalized filter is valid (check ID or category)
		if _, isCheckID := validCheckIDs[normalized]; isCheckID {
			if _, alreadySeen := seen[normalized]; !alreadySeen {
				valid = append(valid, normalized)
				seen[normalized] = struct{}{}
			}
			continue
		}

		if _, isCategory := validCategories[normalized]; isCategory {
			if _, alreadySeen := seen[normalized]; !alreadySeen {
				valid = append(valid, normalized)
				seen[normalized] = struct{}{}
			}
			continue
		}

		// Invalid filter (not a check ID or category)
		invalid = append(invalid, filter)
	}

	return valid, invalid
}

// AllFilters returns all valid filter values (check IDs and categories).
func AllFilters() []string {
	checks := AllChecks()

	seen := map[string]struct{}{}
	var filters []string

	for _, pkg := range checks {
		metadata := pkg.Metadata()

		if _, ok := seen[metadata.CheckID]; !ok {
			filters = append(filters, metadata.CheckID)
			seen[metadata.CheckID] = struct{}{}
		}

		category := string(metadata.Category)
		if _, ok := seen[category]; !ok {
			filters = append(filters, category)
			seen[category] = struct{}{}
		}
	}

	return filters
}

func shouldRunCheck(checker check.Checker, only, ignored map[string]struct{}) bool {
	metadata := checker.Metadata()

	if len(only) > 0 {
		_, categoryInOnly := only[string(metadata.Category)]
		_, checkIDInOnly := only[metadata.CheckID]
		if !categoryInOnly && !checkIDInOnly {
			return false
		}
	}

	if _, ignoredCategory := ignored[string(metadata.Category)]; ignoredCategory {
		return false
	}
	if _, ignoredCheck := ignored[metadata.CheckID]; ignoredCheck {
		return false
	}

	return true
}
