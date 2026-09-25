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
		name    string
		content string
		want    check.Config
		wantErr string
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
			name:    "unknown check",
			content: "no-such-check:\n  timeout: 1000\n",
			wantErr: `unknown check "no-such-check"`,
		},
		{
			name:    "non-scalar value",
			content: "session-settings:\n  roles: [app_ro, app_rw]\n",
			wantErr: "session-settings.roles must be a scalar value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := loadConfig(writeConfig(t, tt.content), pgdoctor.AllChecks())

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg)
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	t.Parallel()

	_, err := loadConfig(filepath.Join(t.TempDir(), "missing.yml"), pgdoctor.AllChecks())

	require.ErrorIs(t, err, os.ErrNotExist)
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
