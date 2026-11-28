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
	"time"

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

	importChannelsCalled bool
	importedChannels     []*monitorpb.ImportNotificationChannel
	importChannelsRsp    *monitorpb.ImportNotificationChannelsRsp
	importChannelsErr    error

	listSecrets  []*adminpb.Secret
	listChannels []*monitorpb.NotificationChannel
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
	case secretMgrSvc + "/ListSecrets":
		request := req.(*adminpb.ListSecretsReq)
		response := rsp.(*adminpb.ListSecretsRsp)
		response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		if request.GetPage().GetPage() <= 1 {
			response.Secrets = g.listSecrets
		}
		response.PageResult = &commonpb.PageResult{Page: request.GetPage().GetPage(), Size: request.GetPage().GetSize(), Total: uint32(len(g.listSecrets)), HasMore: false}
		return nil
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
	case monitorService + "/ListNotificationChannels":
		if g.channelErr != nil {
			return g.channelErr
		}
		response := rsp.(*monitorpb.ListNotificationChannelsRsp)
		response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		response.Channels = g.listChannels
		return nil
	case monitorService + "/ExportNotificationChannels":
		if g.channelErr != nil {
			return g.channelErr
		}
		response := rsp.(*monitorpb.ExportNotificationChannelsRsp)
		response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		response.Channels = g.channels
		return nil
	case monitorService + "/ImportNotificationChannels":
		if g.importChannelsErr != nil {
			return g.importChannelsErr
		}
		request := req.(*monitorpb.ImportNotificationChannelsReq)
		g.importChannelsCalled = true
		g.importedChannels = request.GetChannels()
		response := rsp.(*monitorpb.ImportNotificationChannelsRsp)
		if g.importChannelsRsp != nil {
			response.RetInfo = g.importChannelsRsp.GetRetInfo()
			response.Created = g.importChannelsRsp.GetCreated()
			response.Updated = g.importChannelsRsp.GetUpdated()
		} else {
			response.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		}
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

func TestSetupBackupRefusesOverwriteWhenPendingImport(t *testing.T) {
	restoreKeys := configcrypt.SetDefaultForTest(&configcrypt.MemoryStore{})
	t.Cleanup(restoreKeys)

	manifest := writeTestSetupManifest(t)
	backupPath := topology.BackupPathFor(manifest)
	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), true)
	require.NoError(t, err)
	defer creds.Clear()

	pendingRaw, err := setupbackup.SealPending(`{"format":"moox.secrets/v1","ciphertext":"GOOD"}`, 3, []setupbackup.Channel{
		{ChannelType: "feishu", WebhookURL: "https://open.feishu.cn/hook/good", Secret: "s"},
	}, creds, time.Date(2026, 11, 28, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, setupbackup.WriteAtomic(backupPath, pendingRaw))

	called := false
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) {
		called = true
		return &fakeBackupGateway{}, nil
	}
	t.Cleanup(func() { openCommandGateway = previous })

	_, err = runSetupBackupCommand(t, "--file", manifest)
	require.ErrorIs(t, err, setupbackup.ErrPendingImport)
	assert.False(t, called, "已有待导入备份时手动 backup 应直接拒绝、不连接网关")

	after, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	assert.Equal(t, pendingRaw, after, "原待导入备份必须毫发无损")
}

func TestCountBackupItemsExcludesEnvironmentBoundSecrets(t *testing.T) {
	gateway := &fakeBackupGateway{
		listSecrets: []*adminpb.Secret{
			{SecretId: "tencent-default", Category: "cloud", Provider: "tencent"},
			{SecretId: "exchange-binance-production-main", Category: "exchange", Provider: "binance"},
			{SecretId: "moox:ssh-host:control", Category: "ssh", Provider: "host"},
			{SecretId: "eb-token-1", Category: "eventbus", Provider: "moox_eventbus"},
		},
		listChannels: []*monitorpb.NotificationChannel{
			{ChannelId: "c1", ChannelType: "wecom", WebhookUrl: "https://example.com/1"},
		},
	}
	secrets, channels, err := countBackupItems(context.Background(), gateway)
	require.NoError(t, err)
	assert.Equal(t, 2, secrets)
	assert.Equal(t, 1, channels)
}

func TestImportBackupFileOverwritesSecretsAndImportsChannelsThenClearsPendingFlag(t *testing.T) {
	restoreKeys := configcrypt.SetDefaultForTest(&configcrypt.MemoryStore{})
	t.Cleanup(restoreKeys)

	manifest := writeTestSetupManifest(t)
	backupPath := topology.BackupPathFor(manifest)
	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), true)
	require.NoError(t, err)
	defer creds.Clear()

	pendingRaw, err := setupbackup.SealPending(`{"format":"moox.secrets/v1","ciphertext":"RESTORE-ME"}`, 2, []setupbackup.Channel{
		{ChannelID: "old-id", ChannelType: "feishu", WebhookURL: "https://open.feishu.cn/hook/abc", Secret: "sec-abc"},
		{ChannelID: "old-id-2", ChannelType: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/hook/def"},
	}, creds, time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, setupbackup.WriteAtomic(backupPath, pendingRaw))

	// 先模拟渠道导入失败：此时不能清掉 pending_import，以便重跑继续恢复。
	failingGateway := &fakeBackupGateway{
		importRsp:         &adminpb.ImportSecretsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Created: 1, Updated: 1},
		importChannelsErr: errors.New("monitor unreachable"),
	}
	_, err = importBackupFile(context.Background(), failingGateway, backupPath)
	require.ErrorContains(t, err, "导入告警渠道备份失败")
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	require.NoError(t, err)
	require.True(t, exists)
	assert.True(t, meta.PendingImport, "导入未完全成功时必须保留 pending_import 标记")

	// 重试成功：密钥按 overwrite=true 导入，渠道按 ImportNotificationChannels 导入，完成后清掉 pending_import。
	gateway := &fakeBackupGateway{
		importRsp: &adminpb.ImportSecretsRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Created: 1,
			Updated: 1,
		},
		importChannelsRsp: &monitorpb.ImportNotificationChannelsRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Created: 1,
			Updated: 1,
		},
	}
	summary, err := importBackupFile(context.Background(), gateway, backupPath)
	require.NoError(t, err)
	assert.True(t, gateway.importCalled)
	assert.True(t, gateway.importOverwrite, "重装自动导入必须覆盖已有初始密钥（以管理台为准）")
	assert.Equal(t, creds.Passphrase(), gateway.importPassphrase)
	assert.True(t, gateway.importChannelsCalled)
	require.Len(t, gateway.importedChannels, 2)
	assert.Equal(t, "feishu", gateway.importedChannels[0].GetChannelType())
	assert.Equal(t, "https://open.feishu.cn/hook/abc", gateway.importedChannels[0].GetWebhookUrl())
	assert.Equal(t, "sec-abc", gateway.importedChannels[0].GetSecret())
	assert.Equal(t, "imported", summary.Status)
	assert.EqualValues(t, 1, summary.SecretsCreated)
	assert.EqualValues(t, 1, summary.SecretsUpdated)
	assert.EqualValues(t, 1, summary.ChannelsCreated)
	assert.EqualValues(t, 1, summary.ChannelsUpdated)
	assert.NotEmpty(t, summary.ImportedAt)

	meta, exists, err = setupbackup.ReadMetadata(backupPath)
	require.NoError(t, err)
	require.True(t, exists)
	assert.False(t, meta.PendingImport, "导入成功后应清除 pending_import 标记")
	assert.NotEmpty(t, meta.ImportedAt)
}
