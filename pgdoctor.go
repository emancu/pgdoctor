// Package pgdoctor implements health checks for common
// misconfiguration and issues of PostgreSQL databases.
package pgdoctor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultStatementTimeoutMs is the PostgreSQL statement_timeout in milliseconds.
// Callers should SET this on the connection before calling Run().
const DefaultStatementTimeoutMs = 2000

// ReportHandler is called once per check after it completes.
type ReportHandler func(*check.Report)

// Collect returns a ReportHandler that appends each report to the given slice.
func Collect(reports *[]*check.Report) ReportHandler {
	return func(r *check.Report) { *reports = append(*reports, r) }
}

// Options configures a pgdoctor run.
type Options struct {
	Checks   []check.Package
	Config   check.Config
	OnReport ReportHandler
}

// Run executes checks sequentially against the given connection.
//
// Important: callers should SET statement_timeout on the connection before calling Run()
// to prevent slow queries from blocking the database. See DefaultStatementTimeoutMs.
func Run(ctx context.Context, conn db.DBTX, opts Options) {
	onReport := opts.OnReport
	if onReport == nil {
		onReport = func(*check.Report) {}
	}

	// Discover installed extensions once, so no check has to query pg_extension
	// itself. A consumer that already put a set on the context keeps it.
	if check.ExtensionsFromContext(ctx) == nil {
		ctx = check.ContextWithExtensions(ctx, installedExtensions(ctx, conn))
	}

	for _, pkg := range opts.Checks {
		checker := pkg.New(conn, opts.Config)

		start := time.Now()
		report, err := checker.Check(ctx)
		elapsed := time.Since(start)

		if err != nil {
			report = skipped(checker, report, err)
		}

		report.Duration = elapsed
		onReport(report)
	}
}

// installedExtensions reads the installed extension set. A failure (or a nil
// conn, as check-level tests pass) leaves availability unknown rather than
// reporting every extension as absent, so checks are never skipped on a guess.
func installedExtensions(ctx context.Context, conn db.DBTX) check.Extensions {
	if conn == nil {
		return nil
	}

	rows, err := db.New(conn).InstalledExtensions(ctx)
	if err != nil {
		return nil
	}

	extensions := make(check.Extensions, len(rows))
	for _, row := range rows {
		extensions[row.Name] = row.Version
	}

	return extensions
}

// skipped builds the report for a check that returned an error.
//
// A check may return findings alongside its error, for work it completed before
// the failure — partition-usage analyzes pg_stat_user_tables before it needs
// pg_stat_statements. Those findings are kept and the SKIP is recorded as one
// more finding: SeveritySkip sorts below SeverityPass, so it documents what did
// not run without lowering what did. Only a check that produced nothing skips
// wholesale.
func skipped(checker check.Checker, report *check.Report, err error) *check.Report {
	if report == nil || len(report.Results) == 0 {
		report = check.NewReport(checker.Metadata())
		report.Severity = check.SeveritySkip
	}

	finding := check.Finding{
		ID:       "error",
		Name:     "Check Error",
		Severity: check.SeveritySkip,
		Details:  err.Error(),
	}

	switch {
	case isStatementTimeout(err):
		finding.Details = "query cancelled by statement_timeout"
	case isMissingExtension(err):
		// Uniform identity and wording for every check that needs an extension.
		finding.ID = "extension-unavailable"
		finding.Name = "Extension Unavailable"
	}

	report.AddFinding(finding)

	return report
}

// Filter returns checks matching the only/ignored filters.
// If only is non-empty, only checks matching those check IDs or categories are included.
// Checks matching ignored check IDs or categories are excluded.
func Filter(checks []check.Package, only, ignored []string) []check.Package {
	if len(only) == 0 && len(ignored) == 0 {
		return checks
	}

	onlyMap := toSet(only)
	ignoredMap := toSet(ignored)

	var filtered []check.Package
	for _, pkg := range checks {
		metadata := pkg.Metadata()
		checkID := metadata.CheckID
		category := string(metadata.Category)

		if len(onlyMap) > 0 {
			if _, ok := onlyMap[checkID]; !ok {
				if _, ok := onlyMap[category]; !ok {
					continue
				}
			}
		}

		if _, ok := ignoredMap[checkID]; ok {
			continue
		}
		if _, ok := ignoredMap[category]; ok {
			continue
		}

		filtered = append(filtered, pkg)
	}
	return filtered
}

func toSet(items []string) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, item := range items {
		m[item] = struct{}{}
	}
	return m
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
func ValidateFilters(checks []check.Package, filters []string) (valid, invalid []string) {
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

// isStatementTimeout checks if the error is a PostgreSQL statement_timeout (SQLSTATE 57014).
func isStatementTimeout(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}

// isMissingExtension checks if the error is a check reporting that an extension
// it needs is not installed.
func isMissingExtension(err error) bool {
	var missing *check.MissingExtensionError
	return errors.As(err, &missing)
}
