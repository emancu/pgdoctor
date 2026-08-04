package check

import (
	"context"
	"fmt"
)

// Extensions is the set of extensions installed in the database, keyed by
// extension name with the installed version as the value.
//
// pgdoctor.Run() discovers it once per run and publishes it on the context, so
// a check never queries pg_extension itself.
//
// A nil Extensions means availability is unknown — discovery did not run (a
// checker invoked directly, as unit tests do) or it failed. Unknown is not the
// same as empty: RequireExtension never blocks a check on unknown availability,
// it lets the check run and surface whatever the database reports.
type Extensions map[string]string

// Has reports whether the named extension is installed. It is false for an
// unknown (nil) set, so callers that want "run anyway when unknown" should use
// RequireExtension instead.
func (e Extensions) Has(name string) bool {
	_, ok := e[name]
	return ok
}

// Version returns the installed version of the named extension, or "" when it
// is not installed.
func (e Extensions) Version(name string) string {
	return e[name]
}

type extensionsKey struct{}

// ContextWithExtensions returns a new context carrying the installed extension
// set. pgdoctor.Run() calls this; a library consumer can call it first to
// override discovery.
func ContextWithExtensions(ctx context.Context, extensions Extensions) context.Context {
	return context.WithValue(ctx, extensionsKey{}, extensions)
}

// ExtensionsFromContext retrieves the installed extension set from the context.
// It returns nil when availability is unknown.
func ExtensionsFromContext(ctx context.Context) Extensions {
	extensions, _ := ctx.Value(extensionsKey{}).(Extensions)
	return extensions
}

// MissingExtensionError reports that a check needs a PostgreSQL extension the
// database does not have installed. The runner turns it into the check's SKIP,
// which is why no check ever assigns SeveritySkip itself.
type MissingExtensionError struct {
	Extension string
}

func (e *MissingExtensionError) Error() string {
	return fmt.Sprintf("extension %s is not installed", e.Extension)
}

// RequireExtension returns a *MissingExtensionError when the named extension is
// known to be absent, and nil when it is installed or when availability is
// unknown.
//
// A check calls it before the work that needs the extension:
//
//	if err := check.RequireExtension(ctx, "pg_stat_statements"); err != nil {
//	    return report, fmt.Errorf("query pattern analysis: %w", err)
//	}
//
// Returning the error is what makes the check SKIP. Return the report alongside
// it to keep findings already added from queries that did not need the
// extension; return a nil report to skip the check outright.
func RequireExtension(ctx context.Context, name string) error {
	extensions := ExtensionsFromContext(ctx)
	if extensions == nil || extensions.Has(name) {
		return nil
	}

	return &MissingExtensionError{Extension: name}
}
