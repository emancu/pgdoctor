package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/emancu/pgdoctor"
	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/partitioning"
	"github.com/emancu/pgdoctor/checks/pktypes"
	"github.com/emancu/pgdoctor/checks/replicationlag"
	"github.com/emancu/pgdoctor/checks/sequencehealth"
	"github.com/emancu/pgdoctor/checks/sessionsettings"
	"github.com/emancu/pgdoctor/checks/tablevacuumhealth"
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
			content: "session-settings:\n  timeout: 1000\n  ignore_roles: [dba_ro, migrations]\n",
			want:    check.Config{"session-settings": sessionsettings.Config{Timeout: 1000, IgnoreRoles: []string{"dba_ro", "migrations"}}},
		},
		{
			name:    "empty",
			content: "",
			want:    check.Config{},
		},
		{
			name:    "one document with markers",
			content: "---\nsession-settings:\n  timeout: 1000\n...\n",
			want:    check.Config{"session-settings": sessionsettings.Config{Timeout: 1000}},
		},
		{
			name:    "check without settings",
			content: "pg-version: {}\n",
			want:    check.Config{},
		},
		{
			name:    "partition row floor",
			content: "partitioning:\n  inefficient_partitions_min_rows: 25000000\n",
			want: check.Config{"partitioning": partitioning.Config{
				InefficientPartitionsMinRows:  25_000_000,
				LargeUnpartitionedMinRows:     50_000_000,
				TransientUnpartitionedMinRows: 10_000_000,
			}},
		},
		{
			name:    "role timeout and table prefixes",
			content: "session-settings:\n  timeout_by_role:\n    dba_ro: 300000\ntable-vacuum-health:\n  ignore_tables:\n    - public.outbox\n",
			want: check.Config{
				"session-settings":    sessionsettings.Config{Timeout: 5000, TimeoutByRole: map[string]int64{"dba_ro": 300000}},
				"table-vacuum-health": tablevacuumhealth.Config{IgnoreTables: []string{"public.outbox"}},
			},
		},
		{
			name:    "replica lag by application",
			content: "replication-lag:\n  physical_lag_by_application:\n    delayed:\n      warn_seconds: 305\n      fail_seconds: 360\n",
			want: check.Config{"replication-lag": replicationlag.Config{
				PhysicalLagWarnSeconds:   5,
				PhysicalLagFailSeconds:   60,
				PhysicalLagByApplication: map[string]replicationlag.LagThresholds{"delayed": {WarnSeconds: 305, FailSeconds: 360}},
			}},
		},
		{
			name:    "alias across sections",
			content: "pk-types: &limits\n  usage_warn_percent: 40\nsequence-health: *limits\n",
			want: check.Config{
				"pk-types":        pktypes.Config{UsageWarnPercent: 40, UsageFailPercent: 90},
				"sequence-health": sequencehealth.Config{UsageWarnPercent: 40, UsageFailPercent: 90},
			},
		},
		{
			name:    "alias inside one section",
			content: "replication-lag:\n  physical_lag_by_application:\n    a: &lag {warn_seconds: 305, fail_seconds: 360}\n    b: *lag\n",
			want: check.Config{"replication-lag": replicationlag.Config{
				PhysicalLagWarnSeconds: 5,
				PhysicalLagFailSeconds: 60,
				PhysicalLagByApplication: map[string]replicationlag.LagThresholds{
					"a": {WarnSeconds: 305, FailSeconds: 360},
					"b": {WarnSeconds: 305, FailSeconds: 360},
				},
			}},
		},
		{
			name:    "merge key with an explicit key that wins",
			content: "pk-types: &limits\n  usage_warn_percent: 40\n  usage_fail_percent: 80\nsequence-health:\n  usage_warn_percent: 30\n  <<: *limits\n",
			want: check.Config{
				"pk-types":        pktypes.Config{UsageWarnPercent: 40, UsageFailPercent: 80},
				"sequence-health": sequencehealth.Config{UsageWarnPercent: 30, UsageFailPercent: 80},
			},
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
			want:    []string{"session-settings: cannot unmarshal !!int `1000` into sessionsettings.Config"},
		},
		{
			name:    "comma string for a list",
			content: "session-settings:\n  ignore_roles: dba_ro,migrations\n",
			want:    []string{"session-settings: cannot unmarshal !!str `dba_ro,...` into []string"},
		},
		{
			name:    "unknown key",
			content: "session-settings:\n  timeuot: 1000\n",
			want:    []string{"session-settings: field timeuot not found in type sessionsettings.Config"},
		},
		{
			name:    "unknown nested key",
			content: "replication-lag:\n  physical_lag_by_application:\n    delayed:\n      fial_seconds: 360\n",
			want:    []string{"replication-lag: field fial_seconds not found in type replicationlag.LagThresholds"},
		},
		{
			name:    "key on a check without settings",
			content: "pg-version:\n  minimum: 16\n",
			want:    []string{"pg-version: the check accepts no settings"},
		},
		{
			name:    "timeout that is not an integer",
			content: "session-settings:\n  timeout: 5s\n",
			want:    []string{"session-settings: cannot unmarshal !!str `5s` into int64"},
		},
		{
			name:    "role timeout that is not positive",
			content: "session-settings:\n  timeout_by_role:\n    dba_ro: 0\n",
			want:    []string{"session-settings: timeout_by_role.dba_ro: 0 is not a positive integer"},
		},
		{
			name:    "partition row floor that is not positive",
			content: "partitioning:\n  inefficient_partitions_min_rows: 0\n",
			want:    []string{"partitioning: row thresholds must be positive integers"},
		},
		{
			name:    "warn not below the default fail",
			content: "pk-types:\n  usage_warn_percent: 95\n",
			want:    []string{"pk-types: usage_warn_percent 95 must be lower than usage_fail_percent 90"},
		},
		{
			name:    "every problem is reported",
			content: "no-such-check: {}\nsession-settings:\n  timeout: abc\n  timeuot: 1\npk-types:\n  usage_fail_percent: 40\n",
			want: []string{
				"pk-types: usage_warn_percent 50 must be lower than usage_fail_percent 40",
				"session-settings: cannot unmarshal !!str `abc` into int64",
				"session-settings: field timeuot not found in type sessionsettings.Config",
				`unknown check "no-such-check"`,
			},
		},
		{
			name:    "unknown key through an alias",
			content: "pk-types: &limits\n  usage_warn_percnt: 40\nsequence-health: *limits\n",
			want: []string{
				"pk-types: field usage_warn_percnt not found in type pktypes.Config",
				"sequence-health: field usage_warn_percnt not found in type sequencehealth.Config",
			},
		},
		{
			name:    "unknown key through a merge key",
			content: "replication-lag:\n  physical_lag_by_application:\n    a: &lag {warn_seconds: 305, fial_seconds: 360}\n    b:\n      <<: *lag\n",
			want: []string{
				"replication-lag: field fial_seconds not found in type replicationlag.LagThresholds",
				"replication-lag: field fial_seconds not found in type replicationlag.LagThresholds",
			},
		},
		{
			name:    "anchor that contains itself",
			content: "pk-types: &limits\n  usage_warn_percent: *limits\n",
			want:    []string{"pk-types: yaml: anchor 'limits' value contains itself"},
		},
		{
			name:    "every invalid role timeout, in order",
			content: "session-settings: {timeout_by_role: {b: -1, a: 0}}\n",
			want: []string{
				"session-settings: timeout_by_role.a: 0 is not a positive integer",
				"session-settings: timeout_by_role.b: -1 is not a positive integer",
			},
		},
		{
			name:    "alias cycle inside an overridden merge key",
			content: "session-settings: {timeout: 1000, <<: &d {timeout: *d}}\n",
			want:    []string{"session-settings: YAML aliases expand too far (an alias cycle or too many aliases)"},
		},
		{
			name:    "alias bomb inside an overridden merge key",
			content: "session-settings: {timeout: 1000, <<: {timeout: [&a [x, x, x, x, x, x, x, x, x, x], &b [*a, *a, *a, *a, *a, *a, *a, *a, *a, *a], &c [*b, *b, *b, *b, *b, *b, *b, *b, *b, *b], &d [*c, *c, *c, *c, *c, *c, *c, *c, *c, *c], [*d, *d, *d, *d, *d, *d, *d, *d, *d, *d]]}}\n",
			want:    []string{"session-settings: YAML aliases expand too far (an alias cycle or too many aliases)"},
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

func TestLoadConfigMultipleDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "two documents", content: "session-settings:\n  timeout: 1000\n---\npk-types:\n  usage_warn_percent: 40\n"},
		{name: "unknown key in the second document", content: "session-settings:\n  timeout: 1000\n---\nsession-settings:\n  timeuot: 1\n"},
		{name: "empty second document", content: "session-settings:\n  timeout: 1000\n---\n"},
		{name: "invalid second document", content: "session-settings:\n  timeout: 1000\n---\nsession-settings: [\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := loadConfig(writeConfig(t, tt.content), pgdoctor.AllChecks())

			require.ErrorContains(t, err, "expected one YAML document")
			assert.Nil(t, cfg)
		})
	}
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

	report, err := sessionsettings.New(rows, sessionsettings.DefaultConfig()).Check(context.Background())
	require.NoError(t, err)
	require.Equal(t, check.SeverityPass, report.Severity)

	cfg, err := loadConfig(writeConfig(t, "session-settings:\n  timeout: 1000\n"), pgdoctor.AllChecks())
	require.NoError(t, err)

	report, err = sessionsettings.New(rows, cfg["session-settings"].(sessionsettings.Config)).Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, check.SeverityWarn, report.Severity)
}

func TestLoadConfigLongListWithoutAliases(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	b.WriteString("table-vacuum-health:\n  ignore_tables:\n")
	for i := range 12000 {
		b.WriteString("    - public.t" + strconv.Itoa(i) + "\n")
	}

	_, err := loadConfig(writeConfig(t, b.String()), pgdoctor.AllChecks())

	require.NoError(t, err)
}

func TestResolveAliasesLeavesTheSharedTreeUnchanged(t *testing.T) {
	t.Parallel()

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("a: {x: 1, <<: &d {x: *d}}\nb: {y: *d}\n"), &doc))
	root := doc.Content[0]
	sectionA, sectionB := root.Content[1], root.Content[3]
	shared := sectionB.Content[1]
	require.Equal(t, yaml.AliasNode, shared.Kind)

	expanded := 0
	_, err := resolveAliases(sectionA, false, &expanded)
	require.ErrorContains(t, err, "YAML aliases expand too far")
	assert.Equal(t, yaml.AliasNode, shared.Kind)
	assert.Equal(t, yaml.AliasNode, shared.Alias.Content[1].Kind)

	expanded = 0
	_, err = resolveAliases(sectionB, false, &expanded)
	require.ErrorContains(t, err, "YAML aliases expand too far")
}

func FuzzLoadConfig(f *testing.F) {
	for _, seed := range []string{
		"session-settings:\n  timeout: 1000\n  ignore_roles: [a, b]\n",
		"pk-types: &l\n  usage_warn_percent: 40\nsequence-health: *l\n",
		"session-settings: {timeout: 1000, <<: &d {timeout: *d}}\npk-types: {usage_warn_percent: *d}\n",
		"replication-lag:\n  physical_lag_by_application:\n    r1: {warn_seconds: 5, fail_seconds: 60}\n",
		"a: 1\n---\nb: 2\n",
	} {
		f.Add(seed)
	}
	checks := pgdoctor.AllChecks()
	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "pgdoctor.yml")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		_, _ = loadConfig(path, checks)
	})
}

func TestLoadConfigEmptyAliasForCheckWithoutSettings(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		"pg-version: &empty {}\ncache-efficiency: *empty\n",
		"pg-version: {<<: {}}\n",
		"pg-version:\n",
	} {
		_, err := loadConfig(writeConfig(t, content), pgdoctor.AllChecks())
		require.NoError(t, err, content)
	}
}

func TestDecodeConfigRejectsTrailingDocuments(t *testing.T) {
	t.Parallel()

	for _, pkg := range pgdoctor.AllChecks() {
		if pkg.DecodeConfig == nil {
			continue
		}
		for _, content := range []string{"{}\n---\nunknown_setting: 1\n", "{}\n---\n[\n"} {
			_, err := pkg.DecodeConfig([]byte(content))
			require.Error(t, err, "%s %q", pkg.Metadata().CheckID, content)
		}
	}
}
