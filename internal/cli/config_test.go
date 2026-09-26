package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/emancu/pgdoctor"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/sessionsettings"
	"github.com/emancu/pgdoctor/db"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pgdoctor.yml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    check.Config
	}{
		{
			name:    "valid",
			content: "session-settings:\n  timeout: 1000\n  roles: app_ro,app_rw\n",
			want:    check.Config{"session-settings": {"timeout": "1000", "roles": "app_ro,app_rw"}},
		},
		{
			name:    "empty",
			content: "",
			want:    check.Config{},
		},
		{
			name:    "check without settings",
			content: "pg-version: {}\n",
			want:    check.Config{"pg-version": {}},
		},
		{
			name:    "role timeout and table prefixes",
			content: "session-settings:\n  timeout.dba_ro: 300000\ntable-vacuum-health:\n  autovacuum_disabled_exclude: [public.outbox]\n",
			want: check.Config{
				"session-settings":    {"timeout.dba_ro": "300000"},
				"table-vacuum-health": {"autovacuum_disabled_exclude": "public.outbox"},
			},
		},
		{
			name:    "list of scalars",
			content: "session-settings:\n  roles:\n    - app_ro\n    - app_rw\n",
			want:    check.Config{"session-settings": {"roles": "app_ro,app_rw"}},
		},
		{
			name:    "flow list of scalars",
			content: "session-settings:\n  roles: [app_ro, 42]\n",
			want:    check.Config{"session-settings": {"roles": "app_ro,42"}},
		},
		{
			name:    "empty list",
			content: "session-settings:\n  roles: []\n",
			want:    check.Config{"session-settings": {"roles": ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := loadConfig(writeConfig(t, tt.content), pgdoctor.AllChecks())

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg)
		})
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "unknown check",
			content: "no-such-check:\n  timeout: 1000\nsession-settings:\n  timeout: 1000\n",
			want:    []string{`unknown check "no-such-check"`},
		},
		{
			name:    "unknown check with non-mapping value",
			content: "no-such-check: [a, b]\n",
			want:    []string{`unknown check "no-such-check"`},
		},
		{
			name:    "non-mapping check settings",
			content: "session-settings: 1000\n",
			want:    []string{"session-settings: not a mapping"},
		},
		{
			name:    "list with a nested item",
			content: "session-settings:\n  roles: [app_ro, [app_rw]]\n  timeout: 1000\n",
			want:    []string{"session-settings.roles: not a scalar value"},
		},
		{
			name:    "mapping value",
			content: "session-settings:\n  roles: {app_ro: true}\n",
			want:    []string{"session-settings.roles: not a scalar value"},
		},
		{
			name:    "unknown key",
			content: "session-settings:\n  timeuot: 1000\n",
			want:    []string{`session-settings: unknown key "timeuot"`},
		},
		{
			name:    "key on a check without settings",
			content: "pg-version:\n  minimum: 16\n",
			want:    []string{`pg-version: unknown key "minimum"`},
		},
		{
			name:    "timeout that is not an integer",
			content: "session-settings:\n  timeout: 5s\n",
			want:    []string{`session-settings: timeout: "5s" is not an integer`},
		},
		{
			name:    "role timeout that is not an integer",
			content: "session-settings:\n  timeout.dba_ro: 5m\n",
			want:    []string{`session-settings: timeout.dba_ro: "5m" is not an integer`},
		},
		{
			name:    "role timeout without a role",
			content: "session-settings:\n  timeout.: 1000\n",
			want:    []string{`session-settings: unknown key "timeout."`},
		},
		{
			name:    "unknown table-vacuum-health key",
			content: "table-vacuum-health:\n  exclude: public.outbox\n",
			want:    []string{`table-vacuum-health: unknown key "exclude"`},
		},
		{
			name:    "every problem is reported",
			content: "no-such-check: {}\nsession-settings:\n  timeout: abc\n  timeuot: 1\n",
			want: []string{
				`session-settings: timeout: "abc" is not an integer`,
				`session-settings: unknown key "timeuot"`,
				`unknown check "no-such-check"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := loadConfig(writeConfig(t, tt.content), pgdoctor.AllChecks())

			require.Error(t, err)
			assert.Nil(t, cfg)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
			assert.Len(t, strings.Split(err.Error(), "\n"), len(tt.want)+1)
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	t.Parallel()

	_, err := loadConfig(filepath.Join(t.TempDir(), "missing.yml"), pgdoctor.AllChecks())

	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	t.Parallel()

	_, err := loadConfig(writeConfig(t, "session-settings: [\n"), pgdoctor.AllChecks())

	require.ErrorContains(t, err, "parsing config")
}

type sessionSettingsQueryer []db.SessionSettingsRow

func (q sessionSettingsQueryer) SessionSettings(context.Context) ([]db.SessionSettingsRow, error) {
	return q, nil
}

func TestLoadConfigReachesCheck(t *testing.T) {
	t.Parallel()

	var rows sessionSettingsQueryer
	for name, value := range map[string]string{
		"statement_timeout":                   "3000",
		"idle_in_transaction_session_timeout": "60000",
		"transaction_timeout":                 "3000",
		"log_min_duration_statement":          "2000",
	} {
		rows = append(rows, db.SessionSettingsRow{
			RoleName:     pgtype.Text{String: "app", Valid: true},
			SettingName:  pgtype.Text{String: name, Valid: true},
			SettingValue: pgtype.Text{String: value, Valid: true},
		})
	}

	report, err := sessionsettings.New(rows).Check(context.Background())
	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)

	cfg, err := loadConfig(writeConfig(t, "session-settings:\n  timeout: 1000\n"), pgdoctor.AllChecks())
	require.NoError(t, err)

	report, err = sessionsettings.New(rows, cfg).Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)
}
