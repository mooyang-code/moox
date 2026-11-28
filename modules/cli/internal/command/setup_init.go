package command

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

const defaultSetupConfigDir = "./config/setup"

// setupInitBundle 是元数据种子：业务空间也在里面，随元数据经 metadata apply 写进 Storage。
type setupInitBundle struct {
	SpaceIDs []string
	Calls    []metadataImportCall
}

type setupInitStorage interface {
	Apply(context.Context, []metadataImportCall) (metadataImportSummary, error)
	Verify(context.Context, []metadataImportCall) (metadataImportSummary, error)
	Close() error
}

type setupInitMetadataCounts struct {
	Planned   int `json:"planned"`
	Applied   int `json:"applied,omitempty"`
	Unchanged int `json:"unchanged"`
}

type setupCloudAccountSummary struct {
	Enabled    bool `json:"enabled"`
	Planned    int  `json:"planned"`
	Registered int  `json:"registered"`
	Unchanged  int  `json:"unchanged"`
}

// setupConsoleLinks 给出管理台入口与安装后需要在页面完成的引导地址（交易所密钥、告警渠道）。
type setupConsoleLinks struct {
	URL        string `json:"url"`
	SecretsURL string `json:"secrets_url"`
	MonitorURL string `json:"monitor_url"`
	Note       string `json:"note"`
}

func describeSetupConsoleLinks(snapshot *setupSnapshot) *setupConsoleLinks {
	if snapshot == nil || snapshot.Manifest == nil {
		return nil
	}
	address := strings.TrimSpace(snapshot.Manifest.ControlHost().Address)
	if address == "" {
		return nil
	}
	base := fmt.Sprintf("https://%s:9527", address)
	return &setupConsoleLinks{
		URL:        base,
		SecretsURL: base + "/#/settings/secrets",
		MonitorURL: base + "/#/ops/monitor",
		Note:       "交易所密钥请到管理台「系统设置 › 密钥管理」录入，告警通知渠道请到「资源与运维 › 监控告警」录入",
	}
}

type setupInitSummary struct {
	Status           string                  `json:"status"`
	Firewall         setupFirewallSummary    `json:"firewall"`
	BusinessSpaces   int                     `json:"business_spaces"`
	BusinessSpaceIDs []string                `json:"business_space_ids"`
	Admin            setupclient.ApplyResult `json:"admin"`
	// Notification 是 Monitor 告警渠道的只读检查结果；尚未配置渠道时附带提醒。
	Notification  *setupNotificationSummary `json:"notification,omitempty"`
	CloudAccounts *setupCloudAccountSummary `json:"cloud_accounts,omitempty"`
	CLS           *setupCLSSummary          `json:"cls,omitempty"`
	SCFPlan       *setupSCFPlanSummary      `json:"scf_plan,omitempty"`
	LoginAPI      string                    `json:"login_api"`
	Metadata      setupInitMetadataCounts   `json:"metadata"`
	Verification  setupInitMetadataCounts   `json:"verification"`
	ArchiveTasks  *setupArchiveTasksSummary `json:"archive_tasks,omitempty"`
	Console       *setupConsoleLinks        `json:"console,omitempty"`
	BrowserTrust  *setupBrowserTrustSummary `json:"browser_trust,omitempty"`
}

// setupInitStorageSummary 是 setup init --stage storage 的结果。
type setupInitStorageSummary struct {
	Status           string                  `json:"status"`
	BusinessSpaceIDs []string                `json:"business_space_ids"`
	Metadata         setupInitMetadataCounts `json:"metadata"`
	Verification     setupInitMetadataCounts `json:"verification"`
}

func newSetupInitCommand(deps setupDeps) *cobra.Command {
	var file, configDir, storageHost, stage string
	var source setupSource
	cmd := &cobra.Command{
		Use:   "init",
		Short: "导入默认空间与元数据，写入默认云账号密钥（只建不改）并检查告警渠道",
		Long: `--stage storage 在控制机只装了 Admin、存储机装好之后运行：写入默认云账号密钥、把业务空间与元数据导入 Storage，控制机上的 Collector 等要这些就绪才能启动。
不带 --stage 在全部组件装好后运行：完成全部初始化。初始秘密只含秘密文件 moox.secrets.toml 里的腾讯云默认密钥（tencent-default），规则为只建不改，管理台已有时以管理台为准。
业务空间已在 Storage 时以管理台为准，不比对；告警通知渠道与交易所密钥不在配置文件里填写，这里只检查 Monitor 是否已有告警渠道并在为空时提醒，安装完成后在管理台页面录入。
还会登记云账户、开通日志服务，并在部署了数据归档时为有限保留期的内置时序数据集自动创建归档任务；因子任务由 setup start 导入（同时为新建的因子结果数据集补建归档任务）。已经做过的步骤不会重复写入。
云函数代码包从 --source-dir（默认当前目录）下的 modules/collector 构建，元数据种子默认读它下面的 config/setup：
要装哪个分支或工作副本的代码，就把 --source-dir 指到那里，并和 setup deploy、setup start 用同一个目录。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if stage != "" && stage != "storage" {
				return errors.New("setup init --stage 只支持 storage")
			}
			if err := source.resolve(); err != nil {
				return err
			}
			bundle, err := deps.loadInitBundle(source.configDir(cmd, configDir))
			if err != nil {
				return err
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if storageHost == "" {
				storageHost = snapshot.Manifest.StorageHost().Name
			}
			if stage == "storage" {
				result, err := deps.initMetadata(cmd.Context(), snapshot, storageHost, bundle)
				if err != nil {
					return err
				}
				return writeSetupJSON(cmd, result)
			}
			result, err := deps.initStorage(cmd.Context(), snapshot, storageHost, source.root, bundle)
			if err != nil {
				return err
			}
			if result.Notification != nil && result.Notification.Warning != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "提示：", result.Notification.Warning)
			}
			return writeSetupJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	cmd.Flags().StringVar(&configDir, "config-dir", defaultSetupConfigDir, "默认配置目录（不写时取 --source-dir 下的 config/setup）")
	cmd.Flags().StringVar(&source.sourceDir, "source-dir", "", "源码或产物包目录（默认当前目录）：云函数代码包和元数据种子从这里取")
	cmd.Flags().StringVar(&storageHost, "storage-host", "", "部署了 Storage 的主机 ID（默认取 moox.toml 里 storage-primary 所在的主机）")
	cmd.Flags().StringVar(&stage, "stage", "", "只做哪个阶段：storage 只写初始秘密、业务空间与元数据；不填做全部初始化")
	return cmd
}

// runSetupInitMetadata 只做不依赖管理台服务的初始化：初始秘密写进 Admin，业务空间与元数据写进 Storage。
// Admin 在 admin 就有，Storage 经 SSH 访问，所以这一步能排在管理台之前。
func runSetupInitMetadata(ctx context.Context, deps setupDeps, snapshot *setupSnapshot, storageHost string, bundle setupInitBundle) (setupInitStorageSummary, error) {
	if err := applySetupSecrets(ctx, deps, snapshot); err != nil {
		return setupInitStorageSummary{}, err
	}
	metadata, verification, err := applySetupMetadata(ctx, deps, snapshot, storageHost, bundle)
	if err != nil {
		return setupInitStorageSummary{}, err
	}
	return setupInitStorageSummary{
		Status: "ready", BusinessSpaceIDs: bundle.SpaceIDs,
		Metadata:     setupInitMetadataCounts{Planned: metadata.Planned, Applied: metadata.Applied, Unchanged: metadata.Unchanged},
		Verification: setupInitMetadataCounts{Planned: verification.Planned, Unchanged: verification.Unchanged},
	}, nil
}

// applySetupSecrets 经 SecretMgr.EnsureInitialSecret 写入默认云账号密钥（只建不改）。
func applySetupSecrets(ctx context.Context, deps setupDeps, snapshot *setupSnapshot) error {
	_, err := deps.applySecrets(ctx, snapshot)
	return err
}

// applySetupMetadata 导入元数据并核对，重复运行结果不变；数据集建好即可写入，不用另外激活。
func applySetupMetadata(ctx context.Context, deps setupDeps, snapshot *setupSnapshot, storageHost string, bundle setupInitBundle) (metadataImportSummary, metadataImportSummary, error) {
	var empty metadataImportSummary
	storage, err := deps.openInitStorage(ctx, snapshot, storageHost)
	if err != nil {
		return empty, empty, err
	}
	defer storage.Close()
	metadata, err := storage.Apply(ctx, bundle.Calls)
	if err != nil {
		return empty, empty, err
	}
	verification, err := storage.Verify(ctx, bundle.Calls)
	if err != nil {
		return empty, empty, err
	}
	if verification.Applied != 0 || verification.Unchanged != len(bundle.Calls) {
		return empty, empty, fmt.Errorf("metadata_verification_failed")
	}
	return metadata, verification, nil
}

func runSetupInit(
	ctx context.Context,
	deps setupDeps,
	snapshot *setupSnapshot,
	storageHost, sourceRoot string,
	bundle setupInitBundle,
) (setupInitSummary, error) {
	// Open the cloud ingress required by every configured public runtime endpoint
	// before the first Admin/Storage mutation.
	firewall, err := deps.ensureFirewall(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, fmt.Errorf("firewall: %w", err)
	}
	if firewall.Status != "" && firewall.Status != "ready" {
		return setupInitSummary{}, fmt.Errorf("firewall_incomplete: status=%s skipped=%d", firewall.Status, firewall.Skipped)
	}
	admin, err := deps.applySecrets(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, err
	}
	notification, err := deps.checkNotification(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, fmt.Errorf("notification: %w", err)
	}
	cloudAccounts, err := deps.registerCloudAccounts(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, err
	}
	// 日志服务（CLS）开箱即用：幂等开通服务、日志集、主题与采集日志字段索引。它只影响排障可观测性，
	// 失败不阻断初始化，结果记入摘要，修复后重跑 init 即可。
	cls, err := deps.provisionCLS(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, err
	}
	// Timer 采集计划：保证每个空间有可用代码包（没有则构建上传）。Collector 的采集计划配置由部署渲染，
	// 代码包按名称取最新版本，所以这里不需要改任何配置；失败不阻断，记入摘要。
	scfPlan, err := deps.ensureSCFPlan(ctx, snapshot, sourceRoot)
	if err != nil {
		return setupInitSummary{}, err
	}
	login, err := deps.login(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, err
	}

	metadata, verification, err := applySetupMetadata(ctx, deps, snapshot, storageHost, bundle)
	if err != nil {
		return setupInitSummary{}, err
	}
	archiveTasks, err := deps.ensureArchiveTasks(ctx, snapshot, "")
	if err != nil {
		return setupInitSummary{}, fmt.Errorf("archive_tasks: %w", err)
	}
	return setupInitSummary{
		Status: "ready", Firewall: firewall, BusinessSpaces: len(bundle.SpaceIDs), BusinessSpaceIDs: bundle.SpaceIDs,
		Admin: admin, Notification: notification, CloudAccounts: cloudAccounts, CLS: cls, SCFPlan: scfPlan, LoginAPI: login.LoginAPI,
		Metadata: setupInitMetadataCounts{
			Planned: metadata.Planned, Applied: metadata.Applied, Unchanged: metadata.Unchanged,
		},
		Verification: setupInitMetadataCounts{
			Planned: verification.Planned, Unchanged: verification.Unchanged,
		},
		ArchiveTasks: archiveTasks,
		Console:      describeSetupConsoleLinks(snapshot),
		BrowserTrust: describeSetupBrowserTrust(snapshot),
	}, nil
}

type setupCLSSummary struct {
	Enabled            bool   `json:"enabled"`
	Status             string `json:"status"`
	Region             string `json:"region,omitempty"`
	LogsetID           string `json:"logset_id,omitempty"`
	TopicID            string `json:"topic_id,omitempty"`
	ServiceOpened      bool   `json:"service_opened,omitempty"`
	LogsetCreated      bool   `json:"logset_created,omitempty"`
	TopicCreated       bool   `json:"topic_created,omitempty"`
	IndexFieldsChanged bool   `json:"index_fields_changed,omitempty"`
	Warning            string `json:"warning,omitempty"`
}

// defaultSetupProvisionCLS 用已登记的腾讯云账号幂等开通 CLS。账号凭证由 Admin 持有，这里只取用不落盘。
// SCF 采集未启用时不做任何事；开通失败返回 status=failed 与脱敏的原因，不中断初始化。
func defaultSetupProvisionCLS(ctx context.Context, snapshot *setupSnapshot) (*setupCLSSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("cls: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFEnabled() {
		return nil, nil
	}
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return &setupCLSSummary{Enabled: true, Status: "failed", Warning: "open gateway failed"}, nil
	}
	defer gateway.Close()
	client := &rpcclient.Client{Gateway: gateway}
	return provisionSetupCLS(ctx, client, defaultCLSFactory, snapshot.Manifest.SCFCloudAccount().AccountID, snapshot.Manifest.Logging.RetentionDays), nil
}

func defaultCLSFactory(secretID, secretKey string) (tencent.CLSAPI, error) {
	return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{SecretID: secretID, SecretKey: secretKey, Region: clsprepare.Region, Endpoint: "cls.tencentcloudapi.com"})
}

func provisionSetupCLS(ctx context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, accountID string, retentionDays int64) *setupCLSSummary {
	provisionCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := clsprepare.Provision(provisionCtx, source, factory, accountID, retentionDays)
	if err != nil {
		// 错误文本已由 clsprepare 脱敏，只带操作名。
		return &setupCLSSummary{Enabled: true, Status: "failed", Region: clsprepare.Region, Warning: err.Error()}
	}
	return &setupCLSSummary{
		Enabled: true, Status: "ready", Region: result.Region, LogsetID: result.LogsetID, TopicID: result.TopicID,
		ServiceOpened: result.ServiceOpened, LogsetCreated: result.LogsetCreated, TopicCreated: result.TopicCreated,
		IndexFieldsChanged: result.IndexFieldsChanged,
	}
}

// defaultSetupRegisterCloudAccounts 把 moox.toml 里 scf_plan 的云账号登记进 Collector。腾讯云密钥本身在 Admin，
// Collector 只存密钥 ID 与 COS 桶，不存 SecretKey。SCF 的各地域是执行目标，不是不同的云账号。
func defaultSetupRegisterCloudAccounts(ctx context.Context, snapshot *setupSnapshot) (*setupCloudAccountSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("cloud_accounts: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFEnabled() {
		return nil, nil
	}

	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return nil, fmt.Errorf("cloud_accounts: open gateway: %w", err)
	}
	defer gateway.Close()
	client := &rpcclient.Client{Gateway: gateway}
	existingAccounts, err := client.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return nil, fmt.Errorf("cloud_accounts: list Tencent cloud accounts: %w", err)
	}
	existing := make(map[string]*collectorpb.CloudAccount, len(existingAccounts))
	for _, account := range existingAccounts {
		if account.GetProvider() == "tencent" && strings.TrimSpace(account.GetAccountId()) != "" {
			existing[account.GetAccountId()] = account
		}
	}

	account := snapshot.Manifest.SCFCloudAccount()
	input := &collectorpb.CloudAccount{
		AccountId: account.AccountID, AccountName: account.AccountName,
		Provider: "tencent", CredentialSecretId: account.CredentialSecretID,
		AppId: account.AppID, CosRegion: account.COSRegion, CosBucket: account.COSBucket,
	}
	if strings.TrimSpace(input.AccountId) == "" || strings.TrimSpace(input.AccountName) == "" || strings.TrimSpace(input.CredentialSecretId) == "" || strings.TrimSpace(input.AppId) == "" || strings.TrimSpace(input.CosRegion) == "" || strings.TrimSpace(input.CosBucket) == "" {
		return nil, fmt.Errorf("cloud_accounts: incomplete Tencent account in scf_plan.cloud_account")
	}

	summary := &setupCloudAccountSummary{Enabled: true, Planned: 1}
	if existingAccount, ok := existing[input.AccountId]; ok {
		if !sameSetupCloudAccount(existingAccount, input) {
			return nil, fmt.Errorf("cloud_accounts: existing Tencent account %q differs from moox.toml", input.AccountId)
		}
		summary.Unchanged++
		return summary, nil
	}
	if _, err := client.CreateCloudAccount(ctx, input); err != nil {
		return nil, fmt.Errorf("cloud_accounts: register Tencent account %q: %w", input.AccountId, err)
	}
	summary.Registered++
	return summary, nil
}

// sameSetupCloudAccount 比较登记内容；时间戳不算。
func sameSetupCloudAccount(account, input *collectorpb.CloudAccount) bool {
	return account.GetAccountId() == input.GetAccountId() && account.GetAccountName() == input.GetAccountName() &&
		account.GetProvider() == input.GetProvider() && account.GetCredentialSecretId() == input.GetCredentialSecretId() &&
		account.GetAppId() == input.GetAppId() && account.GetCosRegion() == input.GetCosRegion() && account.GetCosBucket() == input.GetCosBucket()
}

func loadSetupInitBundle(configDir string) (setupInitBundle, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return setupInitBundle{}, fmt.Errorf("setup_config_dir_invalid")
	}
	seed, err := loadMetadataSeed(filepath.Join(configDir, "metadata.yaml"))
	if err != nil {
		return setupInitBundle{}, err
	}
	spaceIDs, err := businessSpaceIDs(seed)
	if err != nil {
		return setupInitBundle{}, err
	}
	calls, err := buildMetadataImportCalls(seed)
	if err != nil {
		return setupInitBundle{}, err
	}
	return setupInitBundle{SpaceIDs: spaceIDs, Calls: calls}, nil
}

func setupMetadataKey(spaceID, id string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(id)
}

type remoteSetupInitStorage struct {
	gateway commandGateway
}

func defaultOpenSetupInitStorage(
	ctx context.Context,
	snapshot *setupSnapshot,
	host string,
) (setupInitStorage, error) {
	gateway, err := openRemoteStorage(ctx, snapshot, host)
	if err != nil {
		return nil, err
	}
	return &remoteSetupInitStorage{gateway: gateway}, nil
}

func (s *remoteSetupInitStorage) Apply(ctx context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	if s == nil || s.gateway == nil {
		return metadataImportSummary{}, fmt.Errorf("storage_not_reachable")
	}
	return runMetadataApply(ctx, s.gateway, calls)
}

func (s *remoteSetupInitStorage) Verify(ctx context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	return s.Apply(ctx, calls)
}

func (s *remoteSetupInitStorage) Close() error {
	if s == nil || s.gateway == nil {
		return nil
	}
	return s.gateway.Close()
}

var _ setupInitStorage = (*remoteSetupInitStorage)(nil)

// setupBrowserTrustSummary 说明 MooX CA 的信任还要用户自己做：不做时管理台能打开，但浏览器有安全提示。
type setupBrowserTrustSummary struct {
	Status        string `json:"status"`
	CAPath        string `json:"ca_path"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	Note          string `json:"note"`
	OtherMachines string `json:"other_machines"`
}

// describeSetupBrowserTrust 返回信任摘要，status 是 pending，等用户在自己的终端运行 setup trust-browser。
func describeSetupBrowserTrust(snapshot *setupSnapshot) *setupBrowserTrustSummary {
	if snapshot == nil {
		return nil
	}
	host := snapshot.Manifest.ControlHost()
	caPath := tlstrust.CAPath(host.Address)
	return &setupBrowserTrustSummary{
		Status:      "pending",
		CAPath:      caPath,
		Fingerprint: tlstrust.Fingerprint(caPath),
		Note:        "这一步要这台电脑的管理员授权，Agent 做不了，请在自己的终端运行 moox-cli setup trust-browser。不做时管理台能打开，但浏览器有安全提示",
		OtherMachines: fmt.Sprintf("换电脑或迁移机器：打开 https://%s:9527 的登录页或“系统设置 › 密钥管理”页，点“下载并安装根证书”，下载安装包后核对指纹，"+
			"向运维或已信任的机器取得根证书指纹，按安装包里 AGENT-INSTALL.md 的“直接安装”用系统命令安装（可把该文件交给 Agent，由它先向你确认指纹）；"+
			"不要不加检查地运行包内脚本。有仓库、moox.toml 和控制机 SSH 权限的机器更稳妥：先用可信渠道拿到 SSH 主机指纹，运行 "+
			"moox-cli setup trust-host --file moox.toml --host control --fingerprint <指纹>，再运行 moox-cli setup trust-browser --file moox.toml", host.Address),
	}
}
