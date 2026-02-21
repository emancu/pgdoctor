// Package check defines the core types and interfaces for pgdoctor health checks.
package check

import (
	"context"

	"github.com/emancu/pgdoctor/db"
)

// DBTX re-exports the database connection interface for use by external consumers.
// This allows contrib checks from other packages to reference the same interface
// without importing the db package directly.
type DBTX = db.DBTX

type Severity int

const (
	SeverityOK Severity = iota
	SeverityWarn
	SeverityFail
)

func (s Severity) String() string {
	switch s {
	case SeverityOK:
		return "ok"
	case SeverityWarn:
		return "warn"
	case SeverityFail:
		return "fail"
	default:
		return "unknown"
	}
}

type Category string

const (
	CategoryIndexes     Category = "indexes"
	CategoryConfigs     Category = "configs"
	CategoryVacuum      Category = "vacuum"
	CategorySchema      Category = "schema"
	CategoryPerformance Category = "performance"
)

type Checker interface {
	Metadata() Metadata
	Check(context.Context) (*Report, error)
}

// Package holds references to a check's exported functions.
// This allows the generator to create a simple list that consumers
// can use to either get metadata or instantiate checkers.
type Package struct {
	Metadata func() Metadata
	New      func(DBTX) Checker
}

type Metadata struct {
	CheckID     string
	Name        string
	Category    Category
	Description string
	Readme      string
	SQL         string // SQL query used by this check
}

// Report holds check-level metadata and all subcheck findings for a single check.
// The check's overall severity is the maximum severity across all findings.
type Report struct {
	Metadata // Embedded, promotes CheckID, Name, Category, Description, SQL
	Severity Severity
	Results  []Finding
}

func NewReport(metadata Metadata) *Report {
	return &Report{
		Metadata: metadata,
		Severity:      SeverityOK, // Start at OK, will be updated as results are added
		Results:       []Finding{},
	}
}

func (r *Report) AddFinding(res Finding) {
	r.Results = append(r.Results, res)

	if res.Severity > r.Severity {
		r.Severity = res.Severity
	}
}

// Finding is something to log during the check.
// Keep multiple findings in one check when they're closely related and often
// examined together. For example, a connection check might have findings
// about both connection count and idle connections.
type Finding struct {
	// ID is the unique identifier for this specific subcheck (expected to be kebab-cased).
	// Used for display and documentation (e.g., "partitioning", "single-table").
	ID       string
	Name     string
	Severity Severity
	Details  string
	// Table contains optional structured tabular data.
	// If set, the CLI will render this as a formatted table.
	Table *Table
	// Debug contains debug information like SQL queries, timing info, etc.
	// Only shown when --debug flag is used.
	Debug string
}

type Table struct {
	Headers []string
	Rows    []TableRow
}

type TableRow struct {
	Cells    []string
	Severity Severity
}

// InstanceMetadata contains database instance specifications and configuration.
// This metadata is fetched once per pgdoctor run and made available to all checks
// via context for enhanced recommendations and validation.
// All fields are optional - checks gracefully degrade when metadata is absent.
type InstanceMetadata struct {
	// Instance identification
	InstanceID    string            // Instance identifier (e.g., RDS instance ID, Cloud SQL name, hostname)
	InstanceClass string            // Size descriptor (e.g., "db.r6g.xlarge", "n2-standard-4")
	Tags          map[string]string // Instance tags/labels

	// Compute resources
	VCPUCores int     // Number of vCPU cores
	MemoryGB  float64 // RAM in gigabytes

	// Storage configuration
	StorageType string // Storage type (e.g., "gp3", "io2", "ssd", "standard")
	StorageGB   int    // Allocated storage in GB
	StorageIOPS int    // Provisioned IOPS (0 if not applicable)

	// Engine version (parsed at creation time)
	EngineVersion      string // PostgreSQL version string (e.g., "15.4")
	EngineVersionMajor int    // Major version (e.g., 15)
	EngineVersionMinor int    // Minor version (e.g., 4)
}

type instanceMetadataKey struct{}

// ContextWithInstanceMetadata returns a new context with instance metadata attached.
// This is typically called in the CLI layer after fetching instance information.
func ContextWithInstanceMetadata(ctx context.Context, metadata *InstanceMetadata) context.Context {
	return context.WithValue(ctx, instanceMetadataKey{}, metadata)
}

// InstanceMetadataFromContext retrieves instance metadata from the context.
// Returns nil if no metadata is present in the context.
// Checks can use this to access instance specifications for enhanced recommendations.
func InstanceMetadataFromContext(ctx context.Context) *InstanceMetadata {
	if metadata, ok := ctx.Value(instanceMetadataKey{}).(*InstanceMetadata); ok {
		return metadata
	}
	return nil
}
