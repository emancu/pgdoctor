package check_test

import (
	"context"
	"testing"

	"github.com/emancu/pgdoctor/check"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtensions(t *testing.T) {
	t.Parallel()

	installed := check.Extensions{"pg_stat_statements": "1.11", "pgcrypto": "1.3"}

	tests := []struct {
		name        string
		extensions  check.Extensions
		lookup      string
		wantHas     bool
		wantVersion string
	}{
		{
			name:        "installed extension",
			extensions:  installed,
			lookup:      "pg_stat_statements",
			wantHas:     true,
			wantVersion: "1.11",
		},
		{
			name:       "absent from a discovered set",
			extensions: installed,
			lookup:     "pg_buffercache",
		},
		{
			name:       "empty discovered set",
			extensions: check.Extensions{},
			lookup:     "pg_stat_statements",
		},
		{
			name:       "unknown set",
			extensions: nil,
			lookup:     "pg_stat_statements",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantHas, tt.extensions.Has(tt.lookup))
			assert.Equal(t, tt.wantVersion, tt.extensions.Version(tt.lookup))
		})
	}
}

func TestRequireExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// ctx is built per case so the nil set ("no discovery ran") is
		// distinguishable from an empty one ("discovered, nothing installed").
		ctx     func() context.Context
		wantErr bool
	}{
		{
			name: "installed",
			ctx: func() context.Context {
				return check.ContextWithExtensions(context.Background(),
					check.Extensions{"pg_stat_statements": "1.11"})
			},
		},
		{
			name: "discovered but absent",
			ctx: func() context.Context {
				return check.ContextWithExtensions(context.Background(), check.Extensions{})
			},
			wantErr: true,
		},
		{
			name: "availability unknown does not block the check",
			ctx:  context.Background,
		},
		{
			name: "explicit nil set is unknown too",
			ctx: func() context.Context {
				return check.ContextWithExtensions(context.Background(), nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := check.RequireExtension(tt.ctx(), "pg_stat_statements")

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}

			var missing *check.MissingExtensionError
			require.ErrorAs(t, err, &missing)
			assert.Equal(t, "pg_stat_statements", missing.Extension)
			assert.Equal(t, "extension pg_stat_statements is not installed", err.Error())
		})
	}
}

func TestExtensionsFromContext_RoundTrip(t *testing.T) {
	t.Parallel()

	assert.Nil(t, check.ExtensionsFromContext(context.Background()))

	extensions := check.Extensions{"pgcrypto": "1.3"}
	ctx := check.ContextWithExtensions(context.Background(), extensions)

	assert.Equal(t, extensions, check.ExtensionsFromContext(ctx))
}
