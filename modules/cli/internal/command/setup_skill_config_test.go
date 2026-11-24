package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestWriteSkillConfigAtomicFailurePreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-access.yaml")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	err := writeSkillConfigAtomic0600(path, []byte("new"), func(string, string) error {
		return errors.New("rename failed")
	})
	require.ErrorContains(t, err, "rename failed")
	raw, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "old", string(raw))
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(path), ".data-access.yaml.tmp-*"))
	require.NoError(t, globErr)
	require.Empty(t, matches)
}

func testSkillDataAccessConfig() dataAccessConfig {
	return dataAccessConfig{
		GatewayClient: &gatewayclient.ExternalFileConfig{Address: "storage.example:11004", Host: "storage", Caller: "moox-skill", KeyFile: ".moox-skill-keys/moox-skill.key"},
		Version:       1,
		DataTypes: map[string]dataTypeConfig{
			"crypto": {
				DefaultExchange: "binance",
				Exchanges: map[string]exchangeConfig{
					"binance": {SpaceID: "crypto", SeriesTag: "venue:binance|market:spot|source:spot_http", KlineDatasets: map[string]string{"1m": "dataset_binance_kline_1m"}},
				},
			},
			"stockcn": {
				DefaultExchange: "stockcn",
				Exchanges: map[string]exchangeConfig{
					"stockcn": {SpaceID: "stockcn", SeriesTag: "default", KlineDatasets: map[string]string{"1m": "dataset_stockcn_equity_kline_1m"}},
				},
			},
		},
	}
}

// identityHarness 记下登记公钥的调用，不连任何主机。
type identityHarness struct {
	deps       setupDeps
	registered [][2]string
}

func newIdentityHarness(t *testing.T) *identityHarness {
	t.Helper()
	h := &identityHarness{}
	snapshot := fourHostSnapshot(t)
	h.deps = setupDeps{
		load: func(string) (*setupSnapshot, error) { return snapshot, nil },
		exportSkillConfig: func(context.Context, *setupSnapshot, string) (dataAccessConfig, error) {
			return testSkillDataAccessConfig(), nil
		},
		registerIdentity: func(_ context.Context, _ *setupSnapshot, identity, publicKey string) error {
			h.registered = append(h.registered, [2]string{identity, publicKey})
			return nil
		},
	}
	return h
}

func (h *identityHarness) run(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	cmd := newSetupCommand(h.deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	var result map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &result), out.String())
	return result, nil
}

// moox-skill 的私钥只在这台电脑上生成、存在操作员身份目录里，登记的是它的公钥；导出时抄一份到输出文件旁边。
// 反复导出（包括导出到别的目录、重新打包 Skill）用的是同一把，不会让已经发出去的 Skill 包失效。
func TestExportSkillConfigGeneratesKeyLocallyAndRegistersPublicKey(t *testing.T) {
	h := newIdentityHarness(t)
	operator := filepath.Join(t.TempDir(), "moox")
	output := filepath.Join(t.TempDir(), "data-access.yaml")
	result, err := h.run(t, "export-skill-config", "--space-id", "crypto", "--output", output, "--operator-dir", operator)
	require.NoError(t, err)
	require.Equal(t, "exported", result["status"])
	master, err := gatewayauth.ReadSigningKey(filepath.Join(operator, "moox-skill.key"))
	require.NoError(t, err)
	keyFile := filepath.Join(filepath.Dir(output), ".moox-skill-keys", "moox-skill.key")
	key, err := gatewayauth.ReadSigningKey(keyFile)
	require.NoError(t, err)
	require.True(t, master.Equal(key))
	directory, err := os.Stat(filepath.Dir(keyFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), directory.Mode().Perm())
	require.Equal(t, [][2]string{{"moox-skill", identitybundle.PublicKey(key)}}, h.registered)
	raw, err := os.ReadFile(output)
	require.NoError(t, err)
	require.NotContains(t, string(raw), gatewayauth.EncodeSigningKey(key), "配置文件里只有私钥的路径")
	var config dataAccessConfig
	require.NoError(t, yaml.Unmarshal(raw, &config))
	require.Equal(t, ".moox-skill-keys/moox-skill.key", filepath.ToSlash(config.GatewayClient.KeyFile))
	require.Equal(t, "moox-skill", config.GatewayClient.Caller)

	// 导出到另一个目录：还是同一把私钥。
	other := filepath.Join(t.TempDir(), "data-access.yaml")
	_, err = h.run(t, "export-skill-config", "--space-id", "crypto", "--output", other, "--operator-dir", operator)
	require.NoError(t, err)
	require.Len(t, h.registered, 2)
	require.Equal(t, h.registered[0], h.registered[1])
	again, err := gatewayauth.ReadSigningKey(filepath.Join(filepath.Dir(other), ".moox-skill-keys", "moox-skill.key"))
	require.NoError(t, err)
	require.True(t, key.Equal(again))

	// 登记失败时不留下指向没登记的私钥的配置。
	h.deps.registerIdentity = func(context.Context, *setupSnapshot, string, string) error { return errors.New("control unreachable") }
	failed := filepath.Join(t.TempDir(), "data-access.yaml")
	_, err = h.run(t, "export-skill-config", "--space-id", "crypto", "--output", failed, "--operator-dir", operator)
	require.ErrorContains(t, err, "control unreachable")
	require.NoFileExists(t, failed)
}

// setup operator：本机已有私钥就复用；--register 把这台电脑的公钥登记成 moox-cli 的公钥（换电脑）。
func TestSetupOperatorReusesKeyAndRegistersOnRequest(t *testing.T) {
	h := newIdentityHarness(t)
	directory := filepath.Join(t.TempDir(), "moox")
	first, err := h.run(t, "operator", "--operator-dir", directory)
	require.NoError(t, err)
	require.Equal(t, true, first["created"])
	require.Equal(t, false, first["registered"])
	require.Empty(t, h.registered, "不带 --register 不连任何主机")
	key, err := identitybundle.ReadOperatorKey(directory)
	require.NoError(t, err)
	require.Equal(t, identitybundle.PublicKey(key), first["public_key"])

	second, err := h.run(t, "operator", "--operator-dir", directory, "--register")
	require.NoError(t, err)
	require.Equal(t, false, second["created"])
	require.Equal(t, true, second["registered"])
	require.Equal(t, first["public_key"], second["public_key"])
	require.Equal(t, [][2]string{{"moox-cli", identitybundle.PublicKey(key)}}, h.registered)
}
