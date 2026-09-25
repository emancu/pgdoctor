package cli

import (
	"context"
	"os"
	"path/filepath"
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
		name        string
		content     string
		want        check.Config
		wantSkipped []string
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
			name:        "unknown check",
			content:     "no-such-check:\n  timeout: 1000\nsession-settings:\n  timeout: 1000\n",
			want:        check.Config{"session-settings": {"timeout": "1000"}},
			wantSkipped: []string{`config: skipping unknown check "no-such-check"`},
		},
		{
			name:        "unknown check with non-mapping value",
			content:     "no-such-check: [a, b]\nsession-settings:\n  timeout: 1000\n",
			want:        check.Config{"session-settings": {"timeout": "1000"}},
			wantSkipped: []string{`config: skipping unknown check "no-such-check"`},
		},
		{
			name:        "non-mapping check settings",
			content:     "session-settings: 1000\n",
			want:        check.Config{},
			wantSkipped: []string{"config: skipping session-settings: not a mapping"},
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
		{
			name:        "list with a nested item",
			content:     "session-settings:\n  roles: [app_ro, [app_rw]]\n  timeout: 1000\n",
			want:        check.Config{"session-settings": {"timeout": "1000"}},
			wantSkipped: []string{"config: skipping session-settings.roles: not a scalar value"},
		},
		{
			name:        "mapping value",
			content:     "session-settings:\n  roles: {app_ro: true}\n  timeout: 1000\n",
			want:        check.Config{"session-settings": {"timeout": "1000"}},
			wantSkipped: []string{"config: skipping session-settings.roles: not a scalar value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, skipped, err := loadConfig(writeConfig(t, tt.content), pgdoctor.AllChecks())

			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg)
			assert.Equal(t, tt.wantSkipped, skipped)
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	t.Parallel()

	_, _, err := loadConfig(filepath.Join(t.TempDir(), "missing.yml"), pgdoctor.AllChecks())

	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	t.Parallel()

	_, _, err := loadConfig(writeConfig(t, "session-settings: [\n"), pgdoctor.AllChecks())

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

	cfg, _, err := loadConfig(writeConfig(t, "session-settings:\n  timeout: 1000\n"), pgdoctor.AllChecks())
	require.NoError(t, err)

	report, err = sessionsettings.New(rows, cfg).Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)
}
