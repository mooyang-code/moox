package configinit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

func TestGetConfigMasksSecretsUnlessRevealed(t *testing.T) {
	path, _, keys := migrateFixture(t, sampleText(t))
	const secret = "Adm1n-P@ss"

	admin, err := GetConfig(path, "admin", false, keys)
	require.NoError(t, err)
	require.Equal(t, MaskedValue, admin.(map[string]any)["password"])
	require.NotEmpty(t, admin.(map[string]any)["username"])

	pw, err := GetConfig(path, "admin.password", false, keys)
	require.NoError(t, err)
	require.Equal(t, MaskedValue, pw)

	pw, err = GetConfig(path, "admin.password", true, keys)
	require.NoError(t, err)
	require.Equal(t, secret, pw)

	whole, err := GetConfig(path, "", false, keys)
	require.NoError(t, err)
	encoded, err := json.Marshal(whole)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.NotContains(t, string(encoded), "pw-storage", "内嵌表里的密码也要打码")
}

func TestGetConfigRejectsUnknownPathAndIndexesArrays(t *testing.T) {
	path, _, keys := migrateFixture(t, sampleText(t))
	_, err := GetConfig(path, "nope.x", false, keys)
	require.ErrorContains(t, err, "没有 nope.x")
	_, err = GetConfig(path, "hosts.storage.label.x", false, keys)
	require.ErrorContains(t, err, "单个值")
	task, err := GetConfig(path, "factors.tasks.0", false, keys)
	require.NoError(t, err)
	require.NotEmpty(t, task.(map[string]any)["id"])
	_, err = GetConfig(path, "factors.tasks.99", false, keys)
	require.ErrorContains(t, err, "序号")
}

func TestSetConfigEditsOneLineAndKeepsEverythingElse(t *testing.T) {
	path, _, keys := migrateFixture(t, sampleText(t))
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	before, _ := os.ReadFile(path)

	report, err := SetConfig(path, "hosts.storage.label", "存储主机", false, keys, now)
	require.NoError(t, err)
	require.True(t, report.Changed && report.Valid && !report.Written)
	after, _ := os.ReadFile(path)
	require.Equal(t, before, after, "预览不写回")

	report, err = SetConfig(path, "hosts.storage.label", "存储主机", true, keys, now)
	require.NoError(t, err)
	require.True(t, report.Written)
	require.FileExists(t, report.Backup)

	raw, _ := os.ReadFile(path)
	plain, err := configcrypt.Decrypt(raw, keys)
	require.NoError(t, err)
	oldPlain, _ := configcrypt.Decrypt(before, keys)
	require.Equal(t,
		strings.Replace(string(oldPlain), `label = "存储机"`, `label = "存储主机"`, 1), string(plain),
		"只有那一行变化")

	got, err := GetConfig(path, "hosts.storage.label", false, keys)
	require.NoError(t, err)
	require.Equal(t, "存储主机", got)
}

func TestSetConfigTypesAndRefusals(t *testing.T) {
	path, _, keys := migrateFixture(t, sampleText(t))
	_, err := SetConfig(path, "factors.enabled", "false", false, keys, nil)
	require.NoError(t, err)
	_, err = SetConfig(path, "factors.nope", "1", false, keys, nil)
	require.ErrorContains(t, err, "只改已有的键")
	_, err = SetConfig(path, "nope.x", "1", false, keys, nil)
	require.ErrorContains(t, err, "只改已有的表")
	_, err = SetConfig(path, "factors.tasks.task_id", "x", false, keys, nil)
	require.ErrorContains(t, err, "不能改")
	_, err = SetConfig(path, "hosts.storage.ssh", "x", false, keys, nil)
	require.ErrorContains(t, err, "不能改")

	require.Equal(t, "true", tomlScalar("true"))
	require.Equal(t, "12", tomlScalar("12"))
	require.Equal(t, "1.5", tomlScalar("1.5"))
	require.Equal(t, `"240d"`, tomlScalar("240d"))
	require.Equal(t, `  # 说明`, trailingComment(`"a"  # 说明`))
	require.Equal(t, "", trailingComment(`"a#b"`))
}
