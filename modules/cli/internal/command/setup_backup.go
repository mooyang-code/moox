package command

import (
	"context"
	"fmt"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupbackup "github.com/mooyang-code/moox/modules/cli/internal/setup/backup"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/spf13/cobra"
)

type setupBackupOutput struct {
	Path         string                   `json:"path"`
	SecretCount  int                      `json:"secret_count"`
	ChannelCount int                      `json:"channel_count"`
	Verify       *setupBackupVerifyOutput `json:"verify,omitempty"`
}

type setupBackupVerifyOutput struct {
	ChannelCount int   `json:"channel_count"`
	Created      int32 `json:"created"`
	Updated      int32 `json:"updated"`
	Skipped      int   `json:"skipped"`
	Failed       int   `json:"failed"`
}

func newSetupBackupCommand() *cobra.Command {
	var file string
	var verify bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "把全部非系统密钥与告警渠道加密备份到 moox.toml 同目录的 moox.backup",
		Long: "调用 SecretMgr.ExportSecrets 与 MonitorMgr.ExportNotificationChannels，用本机钥匙串密钥在进程内派生口令与加密密钥，" +
			"把加密迁移包与加密后的告警渠道合并原子写入 moox.toml 同目录的 moox.backup（权限 0600）。" +
			"不接受口令参数，不输出任何密钥内容或口令；--verify 在写入后读回备份并校验渠道解密与不覆盖导入。",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetupBackup(cmd, file, verify)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "部署配置文件；用于定位备份目录并解析操作员 SSH 网关")
	cmd.Flags().BoolVar(&verify, "verify", false, "写入后读回备份，核对渠道解密条数并以 overwrite=false 调用 ImportSecrets 验收")
	return cmd
}

func runSetupBackup(cmd *cobra.Command, file string, verify bool) error {
	cfg, err := topology.Load(defaultFlag(file, defaultSetupFile))
	if err != nil {
		return err
	}
	backupPath := topology.BackupPathFor(cfg.Path())

	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), true)
	if err != nil {
		return fmt.Errorf("派生备份密钥失败：%w", err)
	}
	defer creds.Clear()

	gateway, err := openCommandGateway(cmd.Context(), file, cfg)
	if err != nil {
		return err
	}
	defer gateway.Close()

	secretRsp := &adminpb.ExportSecretsRsp{}
	if err := invokeSecretMgr(cmd.Context(), gateway, "ExportSecrets", &adminpb.ExportSecretsReq{Passphrase: creds.Passphrase()}, secretRsp, secretRsp.GetRetInfo); err != nil {
		return err
	}
	channelRsp := &monitorpb.ExportNotificationChannelsRsp{}
	if err := invokeMonitor(cmd.Context(), gateway, "ExportNotificationChannels", &monitorpb.ExportNotificationChannelsReq{}, channelRsp, channelRsp.GetRetInfo); err != nil {
		return err
	}

	channels := make([]setupbackup.Channel, 0, len(channelRsp.GetChannels()))
	for _, item := range channelRsp.GetChannels() {
		channels = append(channels, setupbackup.Channel{
			ChannelID:   item.GetChannelId(),
			ChannelType: item.GetChannelType(),
			WebhookURL:  item.GetWebhookUrl(),
			Secret:      item.GetSecret(),
			CreatedAt:   item.GetCreatedAt(),
		})
	}
	sealed, err := setupbackup.Seal(secretRsp.GetBundle(), int(secretRsp.GetCount()), channels, creds, time.Now())
	if err != nil {
		return err
	}
	if err := setupbackup.WriteAtomic(backupPath, sealed); err != nil {
		return err
	}

	output := setupBackupOutput{
		Path:         backupPath,
		SecretCount:  int(secretRsp.GetCount()),
		ChannelCount: len(channels),
	}
	if !verify {
		return writeSetupJSON(cmd, output)
	}

	verifyResult, err := verifyBackupFile(cmd.Context(), gateway, backupPath, len(channels))
	if err != nil {
		return err
	}
	output.Verify = verifyResult
	if err := writeSetupJSON(cmd, output); err != nil {
		return err
	}
	if verifyResult.Failed > 0 {
		return fmt.Errorf("备份验收失败：ImportSecrets 有 %d 条失败", verifyResult.Failed)
	}
	return nil
}

func verifyBackupFile(ctx context.Context, gateway gatewayclient.Invoker, backupPath string, expectedChannels int) (*setupBackupVerifyOutput, error) {
	raw, err := setupbackup.ReadFile(backupPath)
	if err != nil {
		return nil, err
	}
	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), false)
	if err != nil {
		return nil, fmt.Errorf("校验备份时读取本机密钥失败：%w", err)
	}
	defer creds.Clear()

	opened, err := setupbackup.Open(raw, creds)
	if err != nil {
		return nil, err
	}
	if len(opened.Channels) != expectedChannels {
		return nil, fmt.Errorf("备份校验失败：读回渠道条数为 %d，与刚导出的 %d 不一致", len(opened.Channels), expectedChannels)
	}
	importRsp := &adminpb.ImportSecretsRsp{}
	req := &adminpb.ImportSecretsReq{
		Bundle:     opened.File.Secrets.Bundle,
		Passphrase: creds.Passphrase(),
		Overwrite:  false,
	}
	if err := invokeSecretMgr(ctx, gateway, "ImportSecrets", req, importRsp, importRsp.GetRetInfo); err != nil {
		return nil, err
	}
	return &setupBackupVerifyOutput{
		ChannelCount: len(opened.Channels),
		Created:      importRsp.GetCreated(),
		Updated:      importRsp.GetUpdated(),
		Skipped:      len(importRsp.GetSkipped()),
		Failed:       len(importRsp.GetFailed()),
	}, nil
}

// invokeSecretMgr 调用一个 SecretMgr 方法并检查业务返回码；错误信息不含口令与迁移包内容。
func invokeSecretMgr(ctx context.Context, gateway gatewayclient.Invoker, method string, req, rsp any, ret func() *commonpb.RetInfo) error {
	if err := gateway.Invoke(ctx, secretMgrSvc, method, req, rsp); err != nil {
		return fmt.Errorf("secret %s: %w", method, err)
	}
	if info := ret(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("secret %s: %s %s", method, info.GetCode(), info.GetMsg())
	}
	return nil
}
