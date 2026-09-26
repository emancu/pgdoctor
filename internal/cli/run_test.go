package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDSNLabel(t *testing.T) {
	t.Setenv("PGHOST", "")
	t.Setenv("PGDATABASE", "")

	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{"url with password", "postgres://u:secret@db.example.com:5432/app", "db.example.com/app"},
		{"url without database", "postgres://u:secret@db.example.com", "db.example.com"},
		{"keyword with password", "host=db.example.com dbname=app user=u password=secret", "db.example.com/app"},
		{"keyword without database", "host=db.example.com user=u password=secret", "db.example.com"},
		{"unix socket host", "host=/var/run/postgresql dbname=app user=u password=secret", "/var/run/postgresql/app"},
		{"multi-host", "host=a.example.com,b.example.com dbname=app user=u password=secret", "a.example.com/app"},
	}

	for _, tt := range tests {
		cfg, err := parseDSN(tt.dsn)
		require.NoError(t, err, tt.name)
		got := dsnLabel(cfg)
		assert.Equal(t, tt.want, got, tt.name)
		assert.NotContains(t, got, "secret", tt.name)
	}
}

func TestParseDSN_Unparsable(t *testing.T) {
	t.Parallel()

	for _, dsn := range []string{
		"postgres://u:secret@db.example.com:notaport/app",
		"host=db.example.com password='secret",
	} {
		_, err := parseDSN(dsn)
		require.Error(t, err, dsn)
		assert.NotContains(t, err.Error(), "secret", dsn)
	}
}

func TestParseDSN_ConnectDefaults(t *testing.T) {
	t.Setenv("PGCONNECT_TIMEOUT", "")
	t.Setenv("PGAPPNAME", "")

	tests := []struct {
		name        string
		dsn         string
		wantTimeout time.Duration
		wantAppName string
	}{
		{"url defaults", "postgres://u@db.example.com/app", 10 * time.Second, "pgdoctor"},
		{"keyword defaults", "host=db.example.com dbname=app", 10 * time.Second, "pgdoctor"},
		{"url overrides", "postgres://u@db.example.com/app?connect_timeout=3&application_name=ops", 3 * time.Second, "ops"},
		{"keyword overrides", "host=db.example.com connect_timeout=3 application_name=ops", 3 * time.Second, "ops"},
		{"explicit zero timeout", "postgres://u@db.example.com/app?connect_timeout=0", 0, "pgdoctor"},
	}

	for _, tt := range tests {
		cfg, err := parseDSN(tt.dsn)
		require.NoError(t, err, tt.name)
		assert.Equal(t, tt.wantTimeout, cfg.ConnectTimeout, tt.name)
		assert.Equal(t, tt.wantAppName, cfg.RuntimeParams["application_name"], tt.name)
	}
}

func TestParseDSNLabel_DatabaseFromEnvironment(t *testing.T) {
	t.Setenv("PGDATABASE", "envdb")

	cfg, err := parseDSN("postgres://u:secret@db.example.com")
	require.NoError(t, err)
	assert.Equal(t, "db.example.com/envdb", dsnLabel(cfg))
}
