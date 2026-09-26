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

	ctx = withServerVersion(ctx, conn)

	for _, pkg := range opts.Checks {
		checker := pkg.New(conn, opts.Config)

		start := time.Now()
		report, err := checker.Check(ctx)
		elapsed := time.Since(start)

		if err != nil {
			metadata := checker.Metadata()
			report = check.NewReport(metadata)
			report.Severity = check.SeveritySkip

			detail := err.Error()
			if isStatementTimeout(err) {
				detail = "query cancelled by statement_timeout"
			}

			report.AddFinding(check.Finding{
				ID:       "error",
				Name:     "Check Error",
				Severity: check.SeveritySkip,
				Details:  detail,
			})
		}

		report.Duration = elapsed
		onReport(report)
	}
}

// withServerVersion fills the engine version from the server when the
// caller's instance metadata does not carry one.
func withServerVersion(ctx context.Context, conn db.DBTX) context.Context {
	meta := check.InstanceMetadataFromContext(ctx)
	if meta != nil && meta.EngineVersionMajor != 0 {
		return ctx
	}

	version, err := db.New(conn).PGVersion(ctx)
	if err != nil {
		return ctx
	}

	var filled check.InstanceMetadata
	if meta != nil {
		filled = *meta
	}
	filled.EngineVersionMajor = int(version.Major)
	filled.EngineVersionMinor = int(version.Minor)
	filled.EngineVersion = fmt.Sprintf("%d.%d", filled.EngineVersionMajor, filled.EngineVersionMinor)

	return check.ContextWithInstanceMetadata(ctx, &filled)
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
