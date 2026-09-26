package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		{"unparsable url", "postgres://u:secret@db.example.com:notaport/app", "unknown"},
		{"unparsable keyword", "host=db.example.com password='secret", "unknown"},
	}

	for _, tt := range tests {
		got := parseDSNLabel(tt.dsn)
		assert.Equal(t, tt.want, got, tt.name)
		assert.NotContains(t, got, "secret", tt.name)
	}
}

func TestParseDSNLabel_DatabaseFromEnvironment(t *testing.T) {
	t.Setenv("PGDATABASE", "envdb")

	assert.Equal(t, "db.example.com/envdb", parseDSNLabel("postgres://u:secret@db.example.com"))
}
