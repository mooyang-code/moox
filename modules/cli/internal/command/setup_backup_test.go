package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupbackup "github.com/mooyang-code/moox/modules/cli/internal/setup/backup"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBackupGateway struct {
	exportPassphrase string
	importPassphrase string
	importBundle     string
	importOverwrite  bool
	importCalled     bool

	bundle       string
	secretCount  int32
	channels     []*monitorpb.ExportedNotificationChannel
	importRsp    *adminpb.ImportSecretsRsp
	exportErr    error
	channelErr   error
	importErr    error
	closed       bool
}

func (g *fakeBackupGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	switch service + "/" + method {
	case secretMgrSvc + "/ExportSecrets":
		if g.exportErr != nil {
			return g.exportErr
		}
		request := req.(*adminpb.ExportSecretsReq)
		g.exportPassphrase = request.GetPassphrase()
		response := rsp.(*adminpb.ExportSecretsRsp)
		response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		response.Bundle = g.bundle
		response.Count = g.secretCount
		return nil
	case monitorService + "/ExportNotificationChannels":
		if g.channelErr != nil {
			return g.channelErr
		}
		response := rsp.(*monitorpb.ExportNotificationChannelsRsp)
		response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		response.Channels = g.channels
		return nil
	case secretMgrSvc + "/ImportSecrets":
		if g.importErr != nil {
			return g.importErr
		}
		request := req.(*adminpb.ImportSecretsReq)
		g.importCalled = true
		g.importPassphrase = request.GetPassphrase()
		g.importBundle = request.GetBundle()
		g.importOverwrite = request.GetOverwrite()
		response := rsp.(*adminpb.ImportSecretsRsp)
		if g.importRsp != nil {
			response.RetInfo = g.importRsp.GetRetInfo()
			response.Created = g.importRsp.GetCreated()
			response.Updated = g.importRsp.GetUpdated()
			response.Skipped = append([]string(nil), g.importRsp.GetSkipped()...)
			response.Failed = append([]string(nil), g.importRsp.GetFailed()...)
		} else {
			response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		}
		return nil
	default:
		return errors.New("unexpected rpc " + service + "/" + method)
	}
}

func (g *fakeBackupGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, errors.New("not used")
}

func (g *fakeBackupGateway) Close() error { g.closed = true; return nil }

func writeTestSetupManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "moox.toml")
	raw := `[paths]
deploy_root = "/data/moox"

[hosts.control]
address = "198.51.100.10"
region = "ap-shanghai"
ssh = { username = "ubuntu", private_key_file = "~/.ssh/moox_ed25519" }

[deployments]
control = ["admin", "monitor", "eventbus"]
`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	return path
}

func runSetupBackupCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSetupBackupCommand()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

func TestSetupBackupWritesEncryptedFileAndPrintsCountsOnly(t *testing.T) {
	restoreKeys := configcrypt.SetDefaultForTest(&configcrypt.MemoryStore{})
	t.Cleanup(restoreKeys)

	manifest := writeTestSetupManifest(t)
	gateway := &fakeBackupGateway{
		bundle:      `{"format":"moox.secrets/v1","ciphertext":"SECRET-BUNDLE-CIPHER"}`,
		secretCount: 3,
		channels: []*monitorpb.ExportedNotificationChannel{
			{ChannelId: "c1", ChannelType: "feishu", WebhookUrl: "https://open.feishu.cn/open-apis/bot/v2/hook/top-secret-hook", Secret: "top-secret-sign"},
		},
	}
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })

	stdout, err := runSetupBackupCommand(t, "--file", manifest)
	require.NoError(t, err)
	assert.True(t, gateway.closed)
	assert.False(t, gateway.importCalled)
	assert.GreaterOrEqual(t, len(gateway.exportPassphrase), 64)

	// 标准输出只有路径、密钥条数、渠道条数，不含口令、迁移包、webhook 与签名密钥。
	assert.NotContains(t, stdout, gateway.exportPassphrase)
	assert.NotContains(t, stdout, "SECRET-BUNDLE-CIPHER")
	assert.NotContains(t, stdout, "top-secret-hook")
	assert.NotContains(t, stdout, "top-secret-sign")

	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	wantPath := filepath.Join(filepath.Dir(manifest), topology.BackupFileName)
	assert.Equal(t, map[string]any{
		"path":          wantPath,
		"secret_count":  float64(3),
		"channel_count": float64(1),
	}, out)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(wantPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	raw, err := os.ReadFile(wantPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "top-secret-hook")
	assert.NotContains(t, string(raw), "top-secret-sign")

	// 确认写出的文件能用本机密钥解开，且内容与导出一致。
	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), false)
	require.NoError(t, err)
	defer creds.Clear()
	opened, err := setupbackup.Open(raw, creds)
	require.NoError(t, err)
	assert.Equal(t, gateway.bundle, opened.File.Secrets.Bundle)
	require.Len(t, opened.Channels, 1)
	assert.Equal(t, "top-secret-sign", opened.Channels[0].Secret)
}

func TestSetupBackupVerifyReadsBackAndCallsImportSecretsWithoutOverwrite(t *testing.T) {
	restoreKeys := configcrypt.SetDefaultForTest(&configcrypt.MemoryStore{})
	t.Cleanup(restoreKeys)

	manifest := writeTestSetupManifest(t)
	gateway := &fakeBackupGateway{
		bundle:      `{"format":"moox.secrets/v1","ciphertext":"CIPHER-TEXT"}`,
		secretCount: 2,
		channels: []*monitorpb.ExportedNotificationChannel{
			{ChannelId: "c1", ChannelType: "dingtalk", WebhookUrl: "https://oapi.dingtalk.com/robot/send?access_token=ding-token", Secret: "ding-secret"},
			{ChannelId: "c2", ChannelType: "wecom", WebhookUrl: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=wecom-token"},
		},
		importRsp: &adminpb.ImportSecretsRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Created: 0,
			Updated: 0,
			Skipped: []string{"Binance 实盘密钥", "腾讯云密钥"},
			Failed:  nil,
		},
	}
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })

	stdout, err := runSetupBackupCommand(t, "--file", manifest, "--verify")
	require.NoError(t, err)
	assert.True(t, gateway.importCalled)
	assert.False(t, gateway.importOverwrite, "验收导入必须 overwrite=false")
	assert.Equal(t, gateway.exportPassphrase, gateway.importPassphrase, "验收导入必须使用同一派生口令")
	assert.Equal(t, gateway.bundle, gateway.importBundle)

	// 输出只含条数，不含口令、渠道密钥，也不打印跳过列表里的具体密钥名称。
	assert.NotContains(t, stdout, gateway.exportPassphrase)
	assert.NotContains(t, stdout, "ding-secret")
	assert.NotContains(t, stdout, "ding-token")
	assert.NotContains(t, stdout, "Binance 实盘密钥")

	var out setupBackupOutput
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	assert.Equal(t, 2, out.SecretCount)
	assert.Equal(t, 2, out.ChannelCount)
	require.NotNil(t, out.Verify)
	assert.Equal(t, &setupBackupVerifyOutput{ChannelCount: 2, Created: 0, Updated: 0, Skipped: 2, Failed: 0}, out.Verify)

	// 验收返回失败条数时命令应报错，且不打印失败密钥详情。
	gateway.importRsp = &adminpb.ImportSecretsRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Skipped: []string{"腾讯云密钥"},
		Failed:  []string{"秘密密钥名称：分类无效"},
	}
	stdout, err = runSetupBackupCommand(t, "--file", manifest, "--verify")
	require.ErrorContains(t, err, "1 条失败")
	assert.NotContains(t, stdout, "秘密密钥名称")
	assert.NotContains(t, err.Error(), "秘密密钥名称")
}
