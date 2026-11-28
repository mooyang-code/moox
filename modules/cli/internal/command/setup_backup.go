package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupbackup "github.com/mooyang-code/moox/modules/cli/internal/setup/backup"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
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

// setupBackupPreview 是卸载与全部覆盖预览里的备份计划，显示将备份或沿用的密钥与告警渠道条数。
type setupBackupPreview struct {
	Status       string `json:"status"`
	Path         string `json:"path"`
	SecretCount  int    `json:"secret_count,omitempty"`
	ChannelCount int    `json:"channel_count,omitempty"`
	ExportedAt   string `json:"exported_at,omitempty"`
	Error        string `json:"error,omitempty"`
	Note         string `json:"note"`
}

// setupWipeBackupSummary 是卸载或全部覆盖前实际执行的备份结果。
type setupWipeBackupSummary struct {
	Status       string `json:"status"`
	Path         string `json:"path"`
	SecretCount  int    `json:"secret_count,omitempty"`
	ChannelCount int    `json:"channel_count,omitempty"`
	Note         string `json:"note,omitempty"`
}

// setupRestoreBackupSummary 是安装过程中自动导入密钥（Admin 就绪后）与告警渠道（Monitor 就绪后）的结果汇总。
type setupRestoreBackupSummary struct {
	Status          string `json:"status"`
	Path            string `json:"path"`
	SecretsCreated  int32  `json:"secrets_created,omitempty"`
	SecretsUpdated  int32  `json:"secrets_updated,omitempty"`
	SecretsSkipped  int    `json:"secrets_skipped,omitempty"`
	ChannelsCreated int32  `json:"channels_created,omitempty"`
	ChannelsUpdated int32  `json:"channels_updated,omitempty"`
	ImportedAt      string `json:"imported_at,omitempty"`
	Note            string `json:"note"`
}

// setupBackupPath 返回 moox.toml 同目录的备份文件路径；测试里未经过 topology.Load 时回退到命令行 --file。
func setupBackupPath(file string, cfg *topology.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Path()) != "" {
		return topology.BackupPathFor(cfg.Path())
	}
	return topology.BackupPathFor(defaultFlag(file, defaultSetupFile))
}

func newSetupBackupCommand(deps setupDeps) *cobra.Command {
	var file string
	var verify bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "把全部非系统密钥与告警渠道加密备份到 moox.toml 同目录的 moox.backup",
		Long: "调用 SecretMgr.ExportSecrets 与 MonitorMgr.ExportNotificationChannels，用本机钥匙串密钥在进程内派生口令与加密密钥，" +
			"把加密迁移包与加密后的告警渠道合并原子写入 moox.toml 同目录的 moox.backup（权限 0600）。" +
			"若本机已有带“卸载后待导入”标记的备份文件，仅在控制机仍处于已装好状态（该标记已过期）时直接重新导出并覆盖，否则拒绝覆盖以防半成品状态覆写完好备份。" +
			"不接受口令参数，不输出任何密钥内容或口令；--verify 在写入后读回备份并校验渠道解密与不覆盖导入。",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetupBackup(cmd, deps, file, verify)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "部署配置文件；用于定位备份目录并解析操作员 SSH 网关")
	cmd.Flags().BoolVar(&verify, "verify", false, "写入后读回备份，核对渠道解密条数并以 overwrite=false 调用 ImportSecrets 验收")
	return cmd
}

func runSetupBackup(cmd *cobra.Command, deps setupDeps, file string, verify bool) error {
	deps = completeSetupDeps(deps)
	cfg, err := topology.Load(defaultFlag(file, defaultSetupFile))
	if err != nil {
		return err
	}
	backupPath := topology.BackupPathFor(cfg.Path())
	overwritePending, err := checkManualBackupWritable(cmd.Context(), deps, cfg, backupPath)
	if err != nil {
		return err
	}
	if overwritePending {
		fmt.Fprintf(cmd.ErrOrStderr(), "提示：本机备份 %s 上的待导入标记因控制机仍处于已装好状态已过期，本次手动备份将重新导出并覆盖\n", backupPath)
	}

	gateway, err := openCommandGateway(cmd.Context(), file, cfg)
	if err != nil {
		return err
	}
	defer gateway.Close()

	output, err := exportBackupToFile(cmd.Context(), gateway, backupPath, overwritePending)
	if err != nil {
		return err
	}
	if !verify {
		return writeSetupJSON(cmd, output)
	}

	verifyResult, err := verifyBackupFile(cmd.Context(), gateway, backupPath, output.ChannelCount)
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

// checkManualBackupWritable 在手动备份发起导出前核对本机备份文件是否被“卸载后待导入”标记锁定。
// 若标记存在但控制机完成标记仍在（route == RouteInstalled），按定义该待导入标记已过期，允许直接重新导出并覆盖；
// 否则（未装完、已清空或无法确认控制机状态）拒绝覆盖，避免半成品状态覆写完好备份。
func checkManualBackupWritable(ctx context.Context, deps setupDeps, cfg *topology.Config, backupPath string) (bool, error) {
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	if err != nil {
		return false, err
	}
	if !exists || !meta.PendingImport {
		return false, nil
	}
	controlHost := cfg.ControlHost().Name
	reports, scanErr := deps.scanDeployment(ctx, &setupSnapshot{Manifest: cfg}, deployscan.Options{Host: controlHost})
	if scanErr != nil {
		return false, fmt.Errorf("%w: 本机备份 %s 带有“卸载后待导入”标记（含 %d 把密钥、%d 个告警渠道），且无法确认控制机安装完成状态（%v），拒绝覆盖；请先完成安装导入该备份或排查控制机连接",
			setupbackup.ErrPendingImport, backupPath, meta.Secrets.Count, meta.NotificationChannels.Count, scanErr)
	}
	if deployscan.RouteForControl(reports, controlHost) == deployscan.RouteInstalled {
		return true, nil
	}
	return false, pendingImportError(backupPath, meta)
}

// checkBackupWritable 在发起导出前先核对本机备份文件是否被“卸载后待导入”标记锁定。
func checkBackupWritable(backupPath string) error {
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	if err != nil {
		return err
	}
	if exists && meta.PendingImport {
		return pendingImportError(backupPath, meta)
	}
	return nil
}

func pendingImportError(backupPath string, meta *setupbackup.File) error {
	return fmt.Errorf("%w: 本机备份 %s 带有“卸载后待导入”标记（含 %d 把密钥、%d 个告警渠道），拒绝覆盖；请先完成安装导入该备份",
		setupbackup.ErrPendingImport, backupPath, meta.Secrets.Count, meta.NotificationChannels.Count)
}

// exportBackupToFile 经已打开的操作员网关导出全部非系统密钥与告警渠道，原子落盘为不带待导入标记的备份文件。
// overwritePending 为 false 时若本机备份已带待导入标记则拒绝覆盖；为 true 时允许覆盖（仅用于系统仍处于已装好状态时覆盖过期的待导入备份）。
func exportBackupToFile(ctx context.Context, gateway gatewayclient.Invoker, backupPath string, overwritePending bool) (setupBackupOutput, error) {
	if !overwritePending {
		if err := checkBackupWritable(backupPath); err != nil {
			return setupBackupOutput{}, err
		}
	}
	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), true)
	if err != nil {
		return setupBackupOutput{}, fmt.Errorf("派生备份密钥失败：%w", err)
	}
	defer creds.Clear()

	secretRsp := &adminpb.ExportSecretsRsp{}
	if err := invokeSecretMgr(ctx, gateway, "ExportSecrets", &adminpb.ExportSecretsReq{Passphrase: creds.Passphrase()}, secretRsp, secretRsp.GetRetInfo); err != nil {
		return setupBackupOutput{}, err
	}
	channelRsp := &monitorpb.ExportNotificationChannelsRsp{}
	if err := invokeMonitor(ctx, gateway, "ExportNotificationChannels", &monitorpb.ExportNotificationChannelsReq{}, channelRsp, channelRsp.GetRetInfo); err != nil {
		return setupBackupOutput{}, err
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
		return setupBackupOutput{}, err
	}
	if overwritePending {
		if err := setupbackup.WriteAtomic(backupPath, sealed); err != nil {
			return setupBackupOutput{}, err
		}
	} else {
		if err := setupbackup.WriteGuarded(backupPath, sealed); err != nil {
			return setupBackupOutput{}, err
		}
	}
	return setupBackupOutput{
		Path:         backupPath,
		SecretCount:  int(secretRsp.GetCount()),
		ChannelCount: len(channels),
	}, nil
}

// defaultSetupPreviewBackup 只读查询当前线上非系统密钥与告警渠道条数，供卸载与全部覆盖预览展示。
// 不派生口令、不传输明文或密文迁移包。
func defaultSetupPreviewBackup(ctx context.Context, file string, snapshot *setupSnapshot) (int, int, error) {
	if snapshot == nil || snapshot.Manifest == nil {
		return 0, 0, fmt.Errorf("缺少拓扑配置")
	}
	gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
	if err != nil {
		return 0, 0, err
	}
	defer gateway.Close()
	return countBackupItems(ctx, gateway)
}

func countBackupItems(ctx context.Context, gateway gatewayclient.Invoker) (int, int, error) {
	seen := make(map[string]struct{})
	const pageSize = 200
	for page := uint32(1); ; page++ {
		rsp := &adminpb.ListSecretsRsp{}
		req := &adminpb.ListSecretsReq{Page: &commonpb.Page{Page: page, Size: pageSize}}
		if err := invokeSecretMgr(ctx, gateway, "ListSecrets", req, rsp, rsp.GetRetInfo); err != nil {
			return 0, 0, err
		}
		for _, item := range rsp.GetSecrets() {
			if isEnvironmentBoundSecret(item) {
				continue
			}
			seen[item.GetSecretId()] = struct{}{}
		}
		if !rsp.GetPageResult().GetHasMore() || len(rsp.GetSecrets()) == 0 {
			break
		}
	}
	channelRsp := &monitorpb.ListNotificationChannelsRsp{}
	if err := invokeMonitor(ctx, gateway, "ListNotificationChannels", &monitorpb.ListNotificationChannelsReq{}, channelRsp, channelRsp.GetRetInfo); err != nil {
		return 0, 0, err
	}
	return len(seen), len(channelRsp.GetChannels()), nil
}

// isEnvironmentBoundSecret 与 Admin 侧 Secret.EnvironmentBound 口径一致：排除主机 SSH 凭据与 EventBus 内部凭据。
func isEnvironmentBoundSecret(secret *adminpb.Secret) bool {
	if secret == nil {
		return true
	}
	if strings.HasPrefix(secret.GetSecretId(), "moox:ssh-host:") {
		return true
	}
	return secret.GetCategory() == "eventbus" && secret.GetProvider() == "moox_eventbus"
}

func defaultSetupBackupBeforeWipe(ctx context.Context, file string, snapshot *setupSnapshot, backupPath string, overwritePending bool) (setupBackupOutput, error) {
	if snapshot == nil || snapshot.Manifest == nil {
		return setupBackupOutput{}, fmt.Errorf("缺少拓扑配置")
	}
	gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
	if err != nil {
		return setupBackupOutput{}, err
	}
	defer gateway.Close()
	return exportBackupToFile(ctx, gateway, backupPath, overwritePending)
}

// checkPendingBackup 检查本机备份是否存在且带有“卸载后待导入”标记；不需要导入时返回跳过的摘要。
func checkPendingBackup(file string, snapshot *setupSnapshot) (backupPath string, skipped *setupRestoreBackupSummary, err error) {
	if snapshot == nil || snapshot.Manifest == nil {
		return "", nil, fmt.Errorf("缺少拓扑配置")
	}
	backupPath = setupBackupPath(file, snapshot.Manifest)
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	if err != nil {
		return "", nil, fmt.Errorf("读取本机备份文件 %s 失败：%w", backupPath, err)
	}
	if !exists {
		return backupPath, &setupRestoreBackupSummary{
			Status: "no_backup",
			Path:   backupPath,
			Note:   fmt.Sprintf("本机没有密钥与告警渠道备份文件（%s），安装完成后请到管理台录入交易所密钥和告警渠道", backupPath),
		}, nil
	}
	if !meta.PendingImport {
		if meta.ImportedAt != "" {
			return backupPath, &setupRestoreBackupSummary{
				Status:     "already_imported",
				Path:       backupPath,
				ImportedAt: meta.ImportedAt,
				Note:       fmt.Sprintf("本机备份已于 %s 导入过，本次跳过", meta.ImportedAt),
			}, nil
		}
		return backupPath, &setupRestoreBackupSummary{
			Status: "not_pending",
			Path:   backupPath,
			Note:   fmt.Sprintf("本机备份 %s 未标记为卸载后待导入，本次跳过；如需新增密钥或告警渠道请在管理台录入", backupPath),
		}, nil
	}
	return backupPath, nil, nil
}

// defaultSetupRestoreBackupSecrets 在控制机 Admin 就绪后、部署其余主机之前，按覆盖自动导入待恢复密钥备份；
// 不清除“待导入”标记（等后续告警渠道也导入完成后再清），中途失败重跑幂等。
func defaultSetupRestoreBackupSecrets(ctx context.Context, file string, snapshot *setupSnapshot) (*setupRestoreBackupSummary, error) {
	backupPath, skipped, err := checkPendingBackup(file, snapshot)
	if err != nil || skipped != nil {
		return skipped, err
	}
	gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	return importBackupSecrets(ctx, gateway, backupPath)
}

// defaultSetupRestoreBackupChannels 在全部组件就绪后、完整初始化之前，按地址幂等覆盖导入待恢复告警渠道备份，
// 成功后清除“待导入”标记。
func defaultSetupRestoreBackupChannels(ctx context.Context, file string, snapshot *setupSnapshot) (*setupRestoreBackupSummary, error) {
	backupPath, skipped, err := checkPendingBackup(file, snapshot)
	if err != nil || skipped != nil {
		return skipped, err
	}
	gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	return importBackupChannels(ctx, gateway, backupPath)
}

// openBackupForImport 读取、校验并用本机钥匙串密钥解开待导入的备份文件；调用方负责在用完后调用 creds.Clear()。
func openBackupForImport(backupPath string) (*setupbackup.Opened, *setupbackup.Credentials, error) {
	raw, err := setupbackup.ReadFile(backupPath)
	if err != nil {
		return nil, nil, err
	}
	creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), false)
	if err != nil {
		return nil, nil, fmt.Errorf("读取本机备份密钥失败：%w", err)
	}
	opened, err := setupbackup.Open(raw, creds)
	if err != nil {
		creds.Clear()
		return nil, nil, err
	}
	return opened, creds, nil
}

// importBackupSecrets 解开待导入的备份文件，仅按覆盖导入全部非系统密钥（保留原密钥 ID），不清除待导入标记。
func importBackupSecrets(ctx context.Context, gateway gatewayclient.Invoker, backupPath string) (*setupRestoreBackupSummary, error) {
	opened, creds, err := openBackupForImport(backupPath)
	if err != nil {
		return nil, err
	}
	defer creds.Clear()

	secretRsp := &adminpb.ImportSecretsRsp{}
	secretReq := &adminpb.ImportSecretsReq{
		Bundle:     opened.File.Secrets.Bundle,
		Passphrase: creds.Passphrase(),
		Overwrite:  true,
	}
	if err := invokeSecretMgr(ctx, gateway, "ImportSecrets", secretReq, secretRsp, secretRsp.GetRetInfo); err != nil {
		return nil, fmt.Errorf("导入密钥备份失败：%w", err)
	}
	if len(secretRsp.GetFailed()) > 0 {
		return nil, fmt.Errorf("导入密钥备份有 %d 条失败（为避免泄露明文不输出具体名称，请查看 Admin 日志）", len(secretRsp.GetFailed()))
	}
	return &setupRestoreBackupSummary{
		Status:         "secrets_imported",
		Path:           backupPath,
		SecretsCreated: secretRsp.GetCreated(),
		SecretsUpdated: secretRsp.GetUpdated(),
		SecretsSkipped: len(secretRsp.GetSkipped()),
		Note: fmt.Sprintf("已自动从备份恢复密钥（新建 %d、覆盖 %d）；告警渠道将在全部组件就绪后导入",
			secretRsp.GetCreated(), secretRsp.GetUpdated()),
	}, nil
}

// importBackupChannels 解开待导入的备份文件，按地址幂等覆盖导入全部告警渠道，完成后清除待导入标记并记录导入时刻。
func importBackupChannels(ctx context.Context, gateway gatewayclient.Invoker, backupPath string) (*setupRestoreBackupSummary, error) {
	opened, creds, err := openBackupForImport(backupPath)
	if err != nil {
		return nil, err
	}
	defer creds.Clear()

	var channelsCreated, channelsUpdated int32
	if len(opened.Channels) > 0 {
		items := make([]*monitorpb.ImportNotificationChannel, 0, len(opened.Channels))
		for _, ch := range opened.Channels {
			items = append(items, &monitorpb.ImportNotificationChannel{
				ChannelType: ch.ChannelType,
				WebhookUrl:  ch.WebhookURL,
				Secret:      ch.Secret,
			})
		}
		channelRsp := &monitorpb.ImportNotificationChannelsRsp{}
		if err := invokeMonitor(ctx, gateway, "ImportNotificationChannels", &monitorpb.ImportNotificationChannelsReq{Channels: items}, channelRsp, channelRsp.GetRetInfo); err != nil {
			return nil, fmt.Errorf("导入告警渠道备份失败：%w", err)
		}
		channelsCreated = channelRsp.GetCreated()
		channelsUpdated = channelRsp.GetUpdated()
	}

	importedAt := time.Now().UTC()
	if err := setupbackup.MarkImported(backupPath, importedAt); err != nil {
		return nil, fmt.Errorf("告警渠道已导入，但更新备份状态失败：%w", err)
	}
	return &setupRestoreBackupSummary{
		Status:          "imported",
		Path:            backupPath,
		ChannelsCreated: channelsCreated,
		ChannelsUpdated: channelsUpdated,
		ImportedAt:      importedAt.Format(time.RFC3339),
		Note: fmt.Sprintf("已自动从备份恢复告警渠道（新建 %d、覆盖 %d），并标记备份为已导入",
			channelsCreated, channelsUpdated),
	}, nil
}

// previewWipeBackup 构造清空前自动备份的只读预览：线上不可达时不让预览失败，以便用户仍能拿到 digest 决定是否加 --no-backup。
// 当控制机完成标记仍存在（route == RouteInstalled）时，本机带待导入标记的备份视为过期，将照常重新导出覆盖；
// 沿用本机已有备份的两条路径会核对当前钥匙串主密钥指纹，不符时在预览中明确标出。
func previewWipeBackup(ctx context.Context, deps setupDeps, file string, snapshot *setupSnapshot, route string, noBackup bool) setupBackupPreview {
	backupPath := setupBackupPath(file, snapshot.Manifest)
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	if err != nil {
		return setupBackupPreview{
			Status: "invalid_existing",
			Path:   backupPath,
			Error:  err.Error(),
			Note:   fmt.Sprintf("本机已有备份文件 %s 无法读取或格式损坏（%v），执行前请先处理该文件", backupPath, err),
		}
	}
	stalePending := exists && meta.PendingImport && route == deployscan.RouteInstalled
	if exists && meta.PendingImport && !stalePending {
		if fpErr := setupbackup.VerifyKeyFingerprint(meta, configcrypt.Default()); fpErr != nil {
			return setupBackupPreview{
				Status:       "key_mismatch",
				Path:         backupPath,
				SecretCount:  meta.Secrets.Count,
				ChannelCount: meta.NotificationChannels.Count,
				ExportedAt:   meta.ExportedAt,
				Error:        fpErr.Error(),
				Note: fmt.Sprintf("本机已有“卸载后待导入”的备份（%d 把密钥、%d 个告警渠道），但钥匙串指纹校验未通过（%v），执行清空前会拒绝继续；请切回生成该备份的钥匙串或先处理该备份文件",
					meta.Secrets.Count, meta.NotificationChannels.Count, fpErr),
			}
		}
		return setupBackupPreview{
			Status:       "reuse_pending",
			Path:         backupPath,
			SecretCount:  meta.Secrets.Count,
			ChannelCount: meta.NotificationChannels.Count,
			ExportedAt:   meta.ExportedAt,
			Note: fmt.Sprintf("本机已有“卸载后待导入”的备份（%d 把密钥、%d 个告警渠道），清空前不再重新导出、也不会覆盖该文件，重装后自动导入",
				meta.Secrets.Count, meta.NotificationChannels.Count),
		}
	}
	if noBackup {
		if exists {
			if fpErr := setupbackup.VerifyKeyFingerprint(meta, configcrypt.Default()); fpErr != nil {
				return setupBackupPreview{
					Status:       "key_mismatch",
					Path:         backupPath,
					SecretCount:  meta.Secrets.Count,
					ChannelCount: meta.NotificationChannels.Count,
					ExportedAt:   meta.ExportedAt,
					Error:        fpErr.Error(),
					Note: fmt.Sprintf("已指定 --no-backup 且本机已有备份（%d 把密钥、%d 个告警渠道），但钥匙串指纹校验未通过（%v），执行清空前会拒绝继续；请切回生成该备份的钥匙串，或移走该备份文件后再试",
						meta.Secrets.Count, meta.NotificationChannels.Count, fpErr),
				}
			}
			return setupBackupPreview{
				Status:       "no_backup_reuse_existing",
				Path:         backupPath,
				SecretCount:  meta.Secrets.Count,
				ChannelCount: meta.NotificationChannels.Count,
				ExportedAt:   meta.ExportedAt,
				Note: fmt.Sprintf("已指定 --no-backup：跳过在线备份，将在开始清空主机前沿用本机已有备份（%d 把密钥、%d 个告警渠道）并打上待导入标记，重装后自动导入",
					meta.Secrets.Count, meta.NotificationChannels.Count),
			}
		}
		return setupBackupPreview{
			Status: "no_backup",
			Path:   backupPath,
			Note:   "已指定 --no-backup 且本机没有已有备份：清空前不备份，重装后需到管理台手动录入交易所密钥与告警渠道",
		}
	}
	secrets, channels, err := deps.previewBackup(ctx, file, snapshot)
	if err != nil {
		note := fmt.Sprintf("清空前自动备份暂时不可用（%v）；执行时会因备份失败而中止不清，确认跳过在线备份请加 --no-backup", err)
		preview := setupBackupPreview{
			Status: "unavailable",
			Path:   backupPath,
			Error:  err.Error(),
			Note:   note,
		}
		if exists {
			preview.SecretCount = meta.Secrets.Count
			preview.ChannelCount = meta.NotificationChannels.Count
			preview.ExportedAt = meta.ExportedAt
			if fpErr := setupbackup.VerifyKeyFingerprint(meta, configcrypt.Default()); fpErr != nil {
				preview.Note += fmt.Sprintf("（本机已有上一次备份含 %d 把密钥、%d 个告警渠道，但与当前钥匙串指纹不符：%v；带 --no-backup 也会在清空前被拒绝）",
					meta.Secrets.Count, meta.NotificationChannels.Count, fpErr)
			} else {
				preview.Note += fmt.Sprintf("（本机已有上一次备份含 %d 把密钥、%d 个告警渠道，带 --no-backup 卸载会沿用它并标记待导入）",
					meta.Secrets.Count, meta.NotificationChannels.Count)
			}
		}
		return preview
	}
	note := fmt.Sprintf("清空前将自动备份 %d 把密钥、%d 个告警渠道到 %s（交易账户与组合账户不纳入，重装后自动导入）", secrets, channels, backupPath)
	if stalePending {
		note += "；本机已有的待导入备份因控制机仍处于已装好状态已过期，将被本次备份覆盖"
	}
	return setupBackupPreview{
		Status:       "will_backup",
		Path:         backupPath,
		SecretCount:  secrets,
		ChannelCount: channels,
		Note:         note,
	}
}

// previewRestoreBackup 在新安装预览里检查本机是否已有卸载留下的待导入备份。
func previewRestoreBackup(file string, snapshot *setupSnapshot) setupBackupPreview {
	backupPath := setupBackupPath(file, snapshot.Manifest)
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	if err != nil {
		return setupBackupPreview{
			Status: "invalid_existing",
			Path:   backupPath,
			Error:  err.Error(),
			Note:   fmt.Sprintf("本机已有备份文件 %s 无法读取（%v）", backupPath, err),
		}
	}
	if exists && meta.PendingImport {
		if fpErr := setupbackup.VerifyKeyFingerprint(meta, configcrypt.Default()); fpErr != nil {
			return setupBackupPreview{
				Status:       "key_mismatch",
				Path:         backupPath,
				SecretCount:  meta.Secrets.Count,
				ChannelCount: meta.NotificationChannels.Count,
				ExportedAt:   meta.ExportedAt,
				Error:        fpErr.Error(),
				Note:         fmt.Sprintf("本机待导入备份（%d 把密钥、%d 个告警渠道）与当前钥匙串指纹不符（%v），安装导入阶段将失败", meta.Secrets.Count, meta.NotificationChannels.Count, fpErr),
			}
		}
		return setupBackupPreview{
			Status:       "will_import",
			Path:         backupPath,
			SecretCount:  meta.Secrets.Count,
			ChannelCount: meta.NotificationChannels.Count,
			ExportedAt:   meta.ExportedAt,
			Note:         fmt.Sprintf("控制机 Admin 就绪后将先自动覆盖导入密钥（%d 把），全部组件就绪后再导入告警渠道（%d 个）", meta.Secrets.Count, meta.NotificationChannels.Count),
		}
	}
	return setupBackupPreview{
		Status: "no_backup",
		Path:   backupPath,
		Note:   "本机没有待导入备份，安装完成后请到管理台录入交易所密钥和告警渠道",
	}
}

// prepareBackupBeforeWipe 在清空任何主机之前完成自动备份或核验将要沿用的本机已有备份。
// 备份写出时不带“卸载后待导入”标记；标记统一由 executeSetupWipe 在真正开始清空主机前写入。
// 若系统仍处于已装好状态（route == RouteInstalled），已有待导入标记视为过期，照常重新导出覆盖。
// 沿用本机已有备份时先核对当前钥匙串主密钥指纹，不符则立即报错、不清空任何主机。
func prepareBackupBeforeWipe(ctx context.Context, deps setupDeps, file string, snapshot *setupSnapshot, route string, plan deployscan.Plan, noBackup bool) (*setupWipeBackupSummary, error) {
	if len(plan.Hosts) == 0 {
		return nil, nil
	}
	backupPath := setupBackupPath(file, snapshot.Manifest)
	meta, exists, err := setupbackup.ReadMetadata(backupPath)
	if err != nil {
		return nil, fmt.Errorf("backup_invalid: 读取本机备份文件 %s 失败：%w；没有清空任何主机", backupPath, err)
	}
	stalePending := exists && meta.PendingImport && route == deployscan.RouteInstalled
	if exists && meta.PendingImport && !stalePending {
		if fpErr := setupbackup.VerifyKeyFingerprint(meta, configcrypt.Default()); fpErr != nil {
			return nil, fmt.Errorf("backup_key_mismatch: 本机待导入备份 %s 无法用当前钥匙串解密（%w）；没有清空任何主机。请切回生成该备份的钥匙串，或处理该备份文件后重试", backupPath, fpErr)
		}
		return &setupWipeBackupSummary{
			Status:       "reused_pending",
			Path:         backupPath,
			SecretCount:  meta.Secrets.Count,
			ChannelCount: meta.NotificationChannels.Count,
			Note:         "本机备份已带待导入标记，保持原文件不被覆盖",
		}, nil
	}
	if noBackup {
		if exists {
			if fpErr := setupbackup.VerifyKeyFingerprint(meta, configcrypt.Default()); fpErr != nil {
				return nil, fmt.Errorf("backup_key_mismatch: 本机已有备份 %s 无法用当前钥匙串解密（%w）；没有清空任何主机。请切回生成该备份的钥匙串，或移走该备份文件后再带 --no-backup 重试", backupPath, fpErr)
			}
			return &setupWipeBackupSummary{
				Status:       "marked_existing",
				Path:         backupPath,
				SecretCount:  meta.Secrets.Count,
				ChannelCount: meta.NotificationChannels.Count,
				Note:         "已跳过在线备份，本机已有备份将在开始清空主机前标记为待导入，重装后自动导入",
			}, nil
		}
		return &setupWipeBackupSummary{
			Status: "skipped",
			Path:   backupPath,
			Note:   "已指定 --no-backup 且本机没有已有备份，清空前未备份",
		}, nil
	}
	out, err := deps.backupBeforeWipe(ctx, file, snapshot, backupPath, stalePending)
	if err != nil {
		return nil, fmt.Errorf("backup_failed: 清空前自动备份失败：%w。没有清空任何主机；修好后重试，或加 --no-backup 跳过在线备份（本机已有备份会被沿用并标记待导入）", err)
	}
	note := fmt.Sprintf("已备份 %d 把密钥、%d 个告警渠道，并在开始清空主机前标记为待导入", out.SecretCount, out.ChannelCount)
	if stalePending {
		note = fmt.Sprintf("已覆盖过期的待导入备份（%d 把密钥、%d 个告警渠道），并在开始清空主机前标记为待导入", out.SecretCount, out.ChannelCount)
	}
	return &setupWipeBackupSummary{
		Status:       "backed_up",
		Path:         out.Path,
		SecretCount:  out.SecretCount,
		ChannelCount: out.ChannelCount,
		Note:         note,
	}, nil
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
