// Package extensionversions inventories installed PostgreSQL extensions and flags versions no longer supported upstream.
package extensionversions

import (
	"context"
	_ "embed"
	"fmt"
	"sort"

	goversion "github.com/hashicorp/go-version"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/db"
)

//go:embed query.sql
var querySQL string

//go:embed README.md
var readme string

type ExtensionQueries interface {
	InstalledExtensions(context.Context) ([]db.InstalledExtensionsRow, error)
}

type checker struct {
	queries  ExtensionQueries
	policies map[string]ExtensionPolicy
}

func Metadata() check.Metadata {
	return check.Metadata{
		Category:    check.CategoryConfigs,
		CheckID:     "extension-versions",
		Name:        "PostgreSQL Extension Versions",
		Description: "Inventories installed extensions and flags versions no longer supported upstream",
		Readme:      readme,
		SQL:         querySQL,
	}
}

func New(queries ExtensionQueries, _ ...check.Config) check.Checker {
	policies := make(map[string]ExtensionPolicy, len(ExtensionPolicies))
	for _, p := range ExtensionPolicies {
		policies[p.Name] = p
	}
	return &checker{
		queries:  queries,
		policies: policies,
	}
}

func (c *checker) Metadata() check.Metadata {
	return Metadata()
}

func (c *checker) Check(ctx context.Context) (*check.Report, error) {
	report := check.NewReport(Metadata())

	rows, err := c.queries.InstalledExtensions(ctx)
	if err != nil {
		return nil, fmt.Errorf("running %s/%s: %w", report.Category, report.CheckID, err)
	}

	report.AddFinding(c.supportFinding(rows))
	report.AddFinding(c.pendingUpdateFinding(rows))

	return report, nil
}

func (c *checker) supportFinding(rows []db.InstalledExtensionsRow) check.Finding {
	if len(rows) == 0 {
		return check.Finding{
			ID:       "version-support",
			Name:     "Supported Versions",
			Severity: check.SeverityPass,
			Details:  "No extensions installed",
		}
	}

	var flagged []check.TableRow
	maxSeverity := check.SeverityPass
	unsupported := 0
	deprecated := 0

	for _, row := range rows {
		sev, required, status := c.classify(row.ExtensionName, row.InstalledVersion)
		switch status {
		case "unsupported":
			unsupported++
		case "deprecated":
			deprecated++
		}
		if sev > maxSeverity {
			maxSeverity = sev
		}
		// Only extensions needing attention go in the table; a clean run is just the PASS line.
		if sev != check.SeverityPass {
			flagged = append(flagged, check.TableRow{
				Cells:    []string{row.ExtensionName, row.InstalledVersion, required},
				Severity: sev,
			})
		}
	}

	finding := check.Finding{
		ID:       "version-support",
		Name:     "Supported Versions",
		Severity: maxSeverity,
		Details:  summary(len(rows), unsupported, deprecated),
	}

	if len(flagged) > 0 {
		sort.SliceStable(flagged, func(i, j int) bool {
			if flagged[i].Severity != flagged[j].Severity {
				return flagged[i].Severity > flagged[j].Severity
			}
			return flagged[i].Cells[0] < flagged[j].Cells[0]
		})
		finding.Table = &check.Table{
			Headers: []string{"Extension", "Installed", "Required"},
			Rows:    flagged,
		}
	}

	return finding
}

func (c *checker) pendingUpdateFinding(rows []db.InstalledExtensionsRow) check.Finding {
	tableRows := make([]check.TableRow, 0, len(rows))

	for _, row := range rows {
		// default_version is NULL when a managed provider does not expose the control file: the bundled ceiling is unknown, so we can't compare.
		if !row.DefaultVersion.Valid {
			continue
		}
		if _, ok := classifyPendingUpdate(row.InstalledVersion, row.DefaultVersion.String); !ok {
			continue
		}
		tableRows = append(tableRows, check.TableRow{
			Cells:    []string{row.ExtensionName, row.InstalledVersion, row.DefaultVersion.String},
			Severity: check.SeverityInfo,
		})
	}

	if len(tableRows) == 0 {
		return check.Finding{
			ID:       "pending-update",
			Name:     "Pending Updates",
			Severity: check.SeverityPass,
			Details:  "All extensions are at the bundled default version",
		}
	}

	sort.SliceStable(tableRows, func(i, j int) bool {
		return tableRows[i].Cells[0] < tableRows[j].Cells[0]
	})

	pgMajor := 0
	if len(rows) > 0 {
		pgMajor = int(rows[0].ServerVersionNum) / 10000
	}

	return check.Finding{
		ID:       "pending-update",
		Name:     "Pending Updates",
		Severity: check.SeverityInfo,
		Details:  fmt.Sprintf("%d extension(s) behind the version bundled with PostgreSQL %d", len(tableRows), pgMajor),
		Table: &check.Table{
			Headers: []string{"Extension", "Installed", "Available"},
			Rows:    tableRows,
		},
	}
}

func (c *checker) classify(name, installed string) (sev check.Severity, required, status string) {
	policy, ok := c.policies[name]
	if !ok {
		return check.SeverityPass, "—", "no version policy"
	}

	required = "≥ " + policy.requiredVersion()

	iv, err := goversion.NewVersion(installed)
	if err != nil {
		return check.SeverityWarn, required, "installed version unparseable — manual review"
	}

	if policy.FailBelow != "" {
		if fv, ferr := goversion.NewVersion(policy.FailBelow); ferr == nil && iv.LessThan(fv) {
			return check.SeverityWarn, required, "unsupported"
		}
	}

	if policy.WarnBelow != "" {
		if wv, werr := goversion.NewVersion(policy.WarnBelow); werr == nil && iv.LessThan(wv) {
			return check.SeverityWarn, required, "deprecated"
		}
	}

	return check.SeverityPass, required, "supported"
}

// classifyPendingUpdate reports whether an extension trails its bundled default; ok is false when the row should be skipped (installed >= default, e.g. managed-provider bundle skew).
func classifyPendingUpdate(installed, defaultVer string) (status string, ok bool) {
	iv, ierr := goversion.NewVersion(installed)
	dv, derr := goversion.NewVersion(defaultVer)
	if ierr != nil || derr != nil {
		return "version unparseable — manual review", true
	}

	if iv.LessThan(dv) {
		return "behind bundled default", true
	}

	return "", false
}

func summary(total, unsupported, deprecated int) string {
	if unsupported == 0 && deprecated == 0 {
		return fmt.Sprintf("%d extension(s) installed; all supported", total)
	}
	return fmt.Sprintf("%d extension(s) installed; %d unsupported, %d deprecated", total, unsupported, deprecated)
}
