package command

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

const defaultSetupConfigDir = "./config/setup"

type setupInitBundle struct {
	Spaces   []setupclient.Space
	Datasets []seedDataset
	Calls    []metadataImportCall
}

type setupInitStorage interface {
	Apply(context.Context, []metadataImportCall) (metadataImportSummary, error)
	Activate(context.Context, []seedDataset) (setupDatasetActivationSummary, error)
	Verify(context.Context, []metadataImportCall) (metadataImportSummary, error)
	Close() error
}

type setupDatasetActivationSummary struct {
	Total     int `json:"total"`
	Activated int `json:"activated"`
	Unchanged int `json:"unchanged"`
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

type setupInitSummary struct {
	Status           string                        `json:"status"`
	Firewall         setupFirewallSummary          `json:"firewall"`
	BusinessSpaces   int                           `json:"business_spaces"`
	BusinessSpaceIDs []string                      `json:"business_space_ids"`
	Admin            setupclient.ApplyResult       `json:"admin"`
	CloudAccounts    *setupCloudAccountSummary     `json:"cloud_accounts,omitempty"`
	CLS              *setupCLSSummary              `json:"cls,omitempty"`
	SCFPlan          *setupSCFPlanSummary          `json:"scf_plan,omitempty"`
	AdminState       string                        `json:"admin_state"`
	LoginAPI         string                        `json:"login_api"`
	Metadata         setupInitMetadataCounts       `json:"metadata"`
	Datasets         setupDatasetActivationSummary `json:"datasets"`
	Verification     setupInitMetadataCounts       `json:"verification"`
	Factors          *setupFactorSummary           `json:"factors,omitempty"`
	BrowserTrust     *setupBrowserTrustSummary     `json:"browser_trust,omitempty"`
}

func newSetupInitCommand(deps setupDeps) *cobra.Command {
	var file, configDir, storageHost string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "导入默认空间、数据集、字段和因子",
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := deps.loadInitBundle(configDir)
			if err != nil {
				return err
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			result, err := deps.initStorage(cmd.Context(), snapshot, file, configDir, storageHost, bundle)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&configDir, "config-dir", defaultSetupConfigDir, "默认配置目录")
	cmd.Flags().StringVar(&storageHost, "storage-host", "", "已部署 Storage 的主机名称")
	_ = cmd.MarkFlagRequired("storage-host")
	return cmd
}

func runSetupInit(
	ctx context.Context,
	deps setupDeps,
	snapshot *setupconfig.Snapshot,
	file, configDir, storageHost string,
	bundle setupInitBundle,
) (setupInitSummary, error) {
	if err := snapshot.Manifest.RequireNotification(); err != nil {
		return setupInitSummary{}, err
	}
	// Open the cloud ingress required by every configured public runtime endpoint
	// before the first Admin/Storage mutation.
	firewall, err := deps.ensureFirewall(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, fmt.Errorf("firewall: %w", err)
	}
	if firewall.Status != "" && firewall.Status != "ready" {
		return setupInitSummary{}, fmt.Errorf("firewall_incomplete: status=%s skipped=%d", firewall.Status, firewall.Skipped)
	}
	if err := ensureSetupBrowserCATrust(ctx, snapshot); err != nil {
		return setupInitSummary{}, err
	}

	admin, err := deps.applySpaces(ctx, snapshot, bundle.Spaces)
	if err != nil {
		return setupInitSummary{}, err
	}
	adminStatus, err := deps.statusSpaces(ctx, snapshot, bundle.Spaces)
	if err != nil {
		return setupInitSummary{}, err
	}
	if adminStatus.State != "completed" || adminStatus.Spaces != len(bundle.Spaces) {
		return setupInitSummary{}, fmt.Errorf("setup_incomplete")
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
	scfPlan, err := deps.ensureSCFPlan(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, err
	}
	login, err := deps.login(ctx, snapshot)
	if err != nil {
		return setupInitSummary{}, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return setupInitSummary{}, fmt.Errorf("config_changed")
	}

	storage, err := deps.openInitStorage(ctx, snapshot, storageHost)
	if err != nil {
		return setupInitSummary{}, err
	}
	defer storage.Close()
	metadata, err := storage.Apply(ctx, bundle.Calls)
	if err != nil {
		return setupInitSummary{}, err
	}
	datasets, err := storage.Activate(ctx, bundle.Datasets)
	if err != nil {
		return setupInitSummary{}, err
	}
	verification, err := storage.Verify(ctx, bundle.Calls)
	if err != nil {
		return setupInitSummary{}, err
	}
	if verification.Applied != 0 || verification.Unchanged != len(bundle.Calls) {
		return setupInitSummary{}, fmt.Errorf("metadata_verification_failed")
	}
	factorPlan, err := loadSetupFactors(snapshot.Manifest, filepath.Dir(file))
	if err != nil {
		return setupInitSummary{}, err
	}
	var factorSummary *setupFactorSummary
	if !factorPlan.empty() {
		factorService, openErr := deps.openInitFactor(ctx, snapshot)
		if openErr != nil {
			return setupInitSummary{}, openErr
		}
		appliedSummary, applyErr := factorService.Apply(ctx, factorPlan)
		closeErr := factorService.Close()
		if applyErr != nil {
			return setupInitSummary{}, applyErr
		}
		if closeErr != nil {
			return setupInitSummary{}, closeErr
		}
		factorSummary = &appliedSummary
	}
	adminStatus, err = deps.statusSpaces(ctx, snapshot, bundle.Spaces)
	if err != nil {
		return setupInitSummary{}, err
	}
	if adminStatus.State != "completed" || adminStatus.Spaces != len(bundle.Spaces) {
		return setupInitSummary{}, fmt.Errorf("setup_incomplete")
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return setupInitSummary{}, fmt.Errorf("config_changed")
	}
	return setupInitSummary{
		Status: "ready", Firewall: firewall, BusinessSpaces: len(bundle.Spaces), BusinessSpaceIDs: setupSpaceIDs(bundle.Spaces),
		Admin: admin, CloudAccounts: cloudAccounts, CLS: cls, SCFPlan: scfPlan, AdminState: adminStatus.State, LoginAPI: login.LoginAPI,
		Metadata: setupInitMetadataCounts{
			Planned: metadata.Planned, Applied: metadata.Applied, Unchanged: metadata.Unchanged,
		},
		Datasets: datasets,
		Verification: setupInitMetadataCounts{
			Planned: verification.Planned, Unchanged: verification.Unchanged,
		},
		Factors: factorSummary, BrowserTrust: describeSetupBrowserTrust(snapshot),
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
func defaultSetupProvisionCLS(ctx context.Context, snapshot *setupconfig.Snapshot) (*setupCLSSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("cls: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFEnabled() {
		return nil, nil
	}
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return &setupCLSSummary{Enabled: true, Status: "failed", Warning: "open gateway failed"}, nil
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway}
	return provisionSetupCLS(ctx, client, defaultCLSFactory, snapshot.Manifest.SCFCloudAccount().AccountID), nil
}

func defaultCLSFactory(secretID, secretKey string) (tencent.CLSAPI, error) {
	return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{SecretID: secretID, SecretKey: secretKey, Region: clsprepare.Region, Endpoint: "cls.tencentcloudapi.com"})
}

func provisionSetupCLS(ctx context.Context, source clsprepare.AccountSource, factory clsprepare.Factory, accountID string) *setupCLSSummary {
	provisionCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := clsprepare.Provision(provisionCtx, source, factory, accountID)
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

// defaultSetupRegisterCloudAccounts makes the single declarative SCF account
// in moox.toml a real CloudNode catalog record. The Tencent credential itself
// is owned by Admin; CloudNode stores only its secret reference and COS
// metadata, never the SecretKey. SCF regions are execution targets, not
// separate cloud accounts.
func defaultSetupRegisterCloudAccounts(ctx context.Context, snapshot *setupconfig.Snapshot) (*setupCloudAccountSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("cloud_accounts: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFEnabled() {
		return nil, nil
	}

	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return nil, fmt.Errorf("cloud_accounts: open gateway: %w", err)
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway}
	existingAccounts, err := client.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return nil, fmt.Errorf("cloud_accounts: list Tencent cloud accounts: %w", err)
	}
	existing := make(map[string]adminclient.CloudAccount, len(existingAccounts))
	for _, account := range existingAccounts {
		if account.Provider == "tencent" && !account.IsDeleted && strings.TrimSpace(account.AccountID) != "" {
			existing[account.AccountID] = account
		}
	}

	account := snapshot.Manifest.SCFCloudAccount()
	input := adminclient.CloudAccountInput{
		AccountID: account.AccountID, AccountName: account.AccountName,
		Provider: "tencent", CredentialSecretID: account.CredentialSecretID,
		AppID: account.AppID, COSRegion: account.COSRegion, COSBucket: account.COSBucket,
	}
	if strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.AccountName) == "" || strings.TrimSpace(input.CredentialSecretID) == "" || strings.TrimSpace(input.AppID) == "" || strings.TrimSpace(input.COSRegion) == "" || strings.TrimSpace(input.COSBucket) == "" {
		return nil, fmt.Errorf("cloud_accounts: incomplete Tencent account in scf_plan.cloud_account")
	}

	summary := &setupCloudAccountSummary{Enabled: true, Planned: 1}
	if existingAccount, ok := existing[input.AccountID]; ok {
		if !sameSetupCloudAccount(existingAccount, input) {
			return nil, fmt.Errorf("cloud_accounts: existing Tencent account %q differs from moox.toml", input.AccountID)
		}
		summary.Unchanged++
		return summary, nil
	}
	if _, err := client.CreateCloudAccount(ctx, input); err != nil {
		return nil, fmt.Errorf("cloud_accounts: register Tencent account %q: %w", input.AccountID, err)
	}
	summary.Registered++
	return summary, nil
}

func sameSetupCloudAccount(account adminclient.CloudAccount, input adminclient.CloudAccountInput) bool {
	return account.AccountID == input.AccountID && account.AccountName == input.AccountName &&
		account.Provider == input.Provider && account.CredentialSecretID == input.CredentialSecretID &&
		account.AppID == input.AppID && account.COSRegion == input.COSRegion && account.COSBucket == input.COSBucket
}

// newSetupFactorsCommand imports the repository's YAML factor library definitions
// without touching Storage metadata.  It is useful when metadata has already
// been initialized (possibly from an older seed) and a full setup init would
// correctly refuse to overwrite that existing contract.
func newSetupFactorsCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "factors",
		Short: "导入本地 YAML 因子库并建立默认 View 绑定",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			plan, err := loadSetupFactors(snapshot.Manifest, filepath.Dir(file))
			if err != nil {
				return err
			}
			summary := setupFactorSummary{Enabled: !plan.empty(), Definitions: len(plan.Definitions), Members: len(plan.Members)}
			if !plan.empty() {
				service, openErr := deps.openInitFactor(cmd.Context(), snapshot)
				if openErr != nil {
					return openErr
				}
				summary, err = service.Apply(cmd.Context(), plan)
				closeErr := service.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ready", "factors": summary})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func setupSpaceIDs(spaces []setupclient.Space) []string {
	ids := make([]string, 0, len(spaces))
	for _, space := range spaces {
		ids = append(ids, strings.TrimSpace(space.SpaceID))
	}
	return ids
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
	if err := validateReservedInternalSpaces(seed); err != nil {
		return setupInitBundle{}, err
	}
	spaces, err := businessSetupSpaces(seed)
	if err != nil {
		return setupInitBundle{}, err
	}
	if err := validateSetupMetadataDependencies(seed); err != nil {
		return setupInitBundle{}, err
	}
	calls, err := buildMetadataImportCalls(seed)
	if err != nil {
		return setupInitBundle{}, err
	}
	datasets := append([]seedDataset(nil), seed.Datasets...)
	sort.Slice(datasets, func(i, j int) bool {
		if datasets[i].SpaceID == datasets[j].SpaceID {
			return datasets[i].DatasetID < datasets[j].DatasetID
		}
		return datasets[i].SpaceID < datasets[j].SpaceID
	})
	return setupInitBundle{Spaces: spaces, Datasets: datasets, Calls: calls}, nil
}

func validateSetupMetadataDependencies(seed metadataSeed) error {
	if len(seed.Subjects) > 0 {
		return fmt.Errorf("default setup metadata cannot contain runtime subject catalog entries")
	}
	spaces := make(map[string]struct{}, len(seed.Spaces))
	for _, item := range seed.Spaces {
		if err := addSetupMetadataKey(spaces, item.SpaceID, "space"); err != nil {
			return err
		}
	}
	requireSpace := func(resource, spaceID string) error {
		if _, ok := spaces[strings.TrimSpace(spaceID)]; !ok {
			return fmt.Errorf("%s references undefined space %q", resource, spaceID)
		}
		return nil
	}

	sources := make(map[string]struct{}, len(seed.DataSources))
	for _, item := range seed.DataSources {
		if err := requireSpace("data_source", item.SpaceID); err != nil {
			return err
		}
		if err := addSetupMetadataKey(sources, setupMetadataKey(item.SpaceID, item.DataSourceID), "data_source"); err != nil {
			return err
		}
	}
	datasets := make(map[string]struct{}, len(seed.Datasets))
	datasetDefinitions := make(map[string]seedDataset, len(seed.Datasets))
	for _, item := range seed.Datasets {
		if err := requireSpace("dataset", item.SpaceID); err != nil {
			return err
		}
		if _, ok := sources[setupMetadataKey(item.SpaceID, item.DataSourceID)]; !ok {
			return fmt.Errorf("dataset %s/%s references undefined data_source %q", item.SpaceID, item.DatasetID, item.DataSourceID)
		}
		if err := addSetupMetadataKey(datasets, setupMetadataKey(item.SpaceID, item.DatasetID), "dataset"); err != nil {
			return err
		}
		if kind, kindErr := parseDataKind(item.DataKind); kindErr == nil && kind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
			if !frequencypkg.IsCanonical(item.Freq) {
				return fmt.Errorf("dataset %s/%s freq %q is not canonical; use one of %s", item.SpaceID, item.DatasetID, item.Freq, strings.Join(frequencypkg.Strings(), ", "))
			}
		}
		datasetDefinitions[setupMetadataKey(item.SpaceID, item.DatasetID)] = item
	}
	groups := make(map[string]struct{}, len(seed.FieldGroups))
	for _, item := range seed.FieldGroups {
		if err := requireSpace("field_group", item.SpaceID); err != nil {
			return err
		}
		if err := addSetupMetadataKey(groups, setupMetadataKey(item.SpaceID, item.GroupID), "field_group"); err != nil {
			return err
		}
	}
	fields := make(map[string]struct{}, len(seed.Fields))
	for _, item := range seed.Fields {
		if err := requireSpace("field", item.SpaceID); err != nil {
			return err
		}
		if strings.TrimSpace(item.GroupID) != "" {
			if _, ok := groups[setupMetadataKey(item.SpaceID, item.GroupID)]; !ok {
				return fmt.Errorf("field %s/%s references undefined field_group %q", item.SpaceID, item.FieldID, item.GroupID)
			}
		}
		if err := addSetupMetadataKey(fields, setupMetadataKey(item.SpaceID, item.FieldID), "field"); err != nil {
			return err
		}
	}
	datasetColumns := make(map[string]struct{}, len(seed.DatasetColumns))
	for _, item := range seed.DatasetColumns {
		if _, ok := datasets[setupMetadataKey(item.SpaceID, item.DatasetID)]; !ok {
			return fmt.Errorf("dataset_column references undefined dataset %s/%s", item.SpaceID, item.DatasetID)
		}
		columnKey := setupMetadataColumnKey(item.SpaceID, item.DatasetID, item.ColumnName)
		if _, ok := datasetColumns[columnKey]; ok {
			return fmt.Errorf(
				"duplicate metadata dataset_column %s/%s/%s",
				item.SpaceID,
				item.DatasetID,
				item.ColumnName,
			)
		}
		datasetColumns[columnKey] = struct{}{}
		originType, err := parseDatasetColumnOriginType(item.OriginType)
		if err != nil {
			return err
		}
		switch originType {
		case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD:
			if err := validateSetupFieldColumnOrigin(fields, item); err != nil {
				return err
			}
		case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR:
			// Factor definitions are owned by FactorMgr, not Storage metadata.
		}
	}
	views := make(map[string]struct{}, len(seed.Views))
	viewDatasets := make(map[string]string, len(seed.Views))
	for _, item := range seed.Views {
		if err := requireSpace("view", item.SpaceID); err != nil {
			return err
		}
		primaryDatasetID := strings.TrimSpace(item.PrimaryDatasetID)
		if primaryDatasetID == "" || item.PrimaryDatasetID != primaryDatasetID {
			return fmt.Errorf(
				"view %s/%s dataset_id must be explicit and trimmed",
				item.SpaceID,
				item.ViewID,
			)
		}
		viewKey := setupMetadataKey(item.SpaceID, item.ViewID)
		if _, ok := datasets[setupMetadataKey(item.SpaceID, primaryDatasetID)]; !ok {
			return fmt.Errorf("view %s/%s references undefined dataset %q", item.SpaceID, item.ViewID, primaryDatasetID)
		}
		if err := validateCanonicalSetupView(item, datasetDefinitions); err != nil {
			return err
		}
		if err := addSetupMetadataKey(views, viewKey, "view"); err != nil {
			return err
		}
		viewDatasets[viewKey] = primaryDatasetID
	}
	viewColumns := make(map[string]struct{}, len(seed.ViewColumns))
	for _, item := range seed.ViewColumns {
		if _, ok := views[setupMetadataKey(item.SpaceID, item.ViewID)]; !ok {
			return fmt.Errorf("view_column references undefined view %s/%s", item.SpaceID, item.ViewID)
		}
		columnKey := setupMetadataColumnKey(item.SpaceID, item.ViewID, item.ColumnName)
		if _, ok := viewColumns[columnKey]; ok {
			return fmt.Errorf(
				"duplicate metadata view_column %s/%s/%s",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
			)
		}
		viewColumns[columnKey] = struct{}{}
		originType, err := parseColumnOriginType(item.OriginType)
		if err != nil {
			return err
		}
		if originType != storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			continue
		}
		// A View indexes exactly one Dataset, so a dataset column is the bare
		// column name of the View's Dataset.
		origin := strings.TrimSpace(item.OriginID)
		if origin == "" || strings.Contains(origin, ".") || origin != strings.TrimSpace(item.ColumnName) {
			return fmt.Errorf(
				"view_column %s/%s/%s has invalid dataset_column origin %q; use the bare column name",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
				item.OriginID,
			)
		}
		datasetID := viewDatasets[setupMetadataKey(item.SpaceID, item.ViewID)]
		if _, ok := datasetColumns[setupMetadataColumnKey(item.SpaceID, datasetID, origin)]; !ok {
			return fmt.Errorf(
				"view_column %s/%s/%s references undefined dataset_column %q",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
				item.OriginID,
			)
		}
	}
	return nil
}

func addSetupMetadataKey(seen map[string]struct{}, raw, resource string) error {
	key := strings.TrimSpace(raw)
	if key == "" {
		return fmt.Errorf("%s id is required", resource)
	}
	if _, ok := seen[key]; ok {
		return fmt.Errorf("duplicate metadata %s %q", resource, key)
	}
	seen[key] = struct{}{}
	return nil
}

func setupMetadataKey(spaceID, id string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(id)
}

func setupMetadataColumnKey(spaceID, parentID, columnName string) string {
	return strings.Join([]string{
		strings.TrimSpace(spaceID),
		strings.TrimSpace(parentID),
		strings.TrimSpace(columnName),
	}, "\x00")
}

func validateSetupFieldColumnOrigin(fields map[string]struct{}, item seedDatasetColumn) error {
	if _, ok := fields[setupMetadataKey(item.SpaceID, item.OriginID)]; ok {
		return nil
	}
	return fmt.Errorf(
		"dataset_column %s/%s/%s references undefined field %q",
		item.SpaceID,
		item.DatasetID,
		item.ColumnName,
		item.OriginID,
	)
}

func validateCanonicalSetupView(view seedView, datasets map[string]seedDataset) error {
	normalized, err := canonicalMetadataView(view, datasets)
	if err != nil {
		return err
	}
	if !slices.Equal(view.GrainKeys, normalized.GrainKeys) {
		return fmt.Errorf("view %s/%s grain_keys must be canonical", view.SpaceID, view.ViewID)
	}
	if view.Engine != normalized.Engine {
		return fmt.Errorf(
			"view %s/%s engine must be %q",
			view.SpaceID,
			view.ViewID,
			normalized.Engine,
		)
	}
	return nil
}

func canonicalMetadataView(view seedView, datasets map[string]seedDataset) (seedView, error) {
	primaryDatasetID := strings.TrimSpace(view.PrimaryDatasetID)
	if primaryDatasetID == "" {
		return seedView{}, fmt.Errorf("view %s/%s dataset_id is required", view.SpaceID, view.ViewID)
	}
	primary, ok := datasets[setupMetadataKey(view.SpaceID, primaryDatasetID)]
	if !ok {
		return seedView{}, fmt.Errorf(
			"view %s/%s dataset %q must be included in the metadata seed",
			view.SpaceID,
			view.ViewID,
			primaryDatasetID,
		)
	}
	dataKind, err := parseDataKind(primary.DataKind)
	if err != nil {
		return seedView{}, fmt.Errorf("view %s/%s dataset: %w", view.SpaceID, view.ViewID, err)
	}
	grainKeys := []string{"record_id", "version"}
	engine := "bleve"
	if dataKind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
		grainKeys = []string{"subject_id", "freq", "data_time", "series_tag"}
		engine = "duckdb"
	}
	view.PrimaryDatasetID = primaryDatasetID
	view.GrainKeys = grainKeys
	view.Engine = engine
	return view, nil
}

type remoteSetupInitStorage struct {
	transport setupssh.Client
	session   *remoteStorageSession
}

func defaultOpenSetupInitStorage(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	host string,
) (setupInitStorage, error) {
	_, transport, session, _, err := openRemoteStorage(ctx, snapshot, host)
	if err != nil {
		return nil, err
	}
	return &remoteSetupInitStorage{transport: transport, session: session}, nil
}

func (s *remoteSetupInitStorage) Apply(ctx context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	if s == nil || s.session == nil || s.session.gateway == nil {
		return metadataImportSummary{}, fmt.Errorf("storage_not_reachable")
	}
	return runMetadataApply(ctx, s.session.gateway, calls)
}

func (s *remoteSetupInitStorage) Activate(ctx context.Context, datasets []seedDataset) (setupDatasetActivationSummary, error) {
	if s == nil || s.session == nil {
		return setupDatasetActivationSummary{}, fmt.Errorf("storage_not_reachable")
	}
	return activateSetupDatasets(ctx, s.session.metadata, s.session.auth, datasets)
}

func (s *remoteSetupInitStorage) Verify(ctx context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	return s.Apply(ctx, calls)
}

func (s *remoteSetupInitStorage) Close() error {
	if s == nil {
		return nil
	}
	if s.session != nil {
		s.session.Close()
	}
	if s.transport != nil {
		return s.transport.Close()
	}
	return nil
}

type datasetActivationAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

func activateSetupDatasets(
	ctx context.Context,
	api datasetActivationAPI,
	auth *storagepb.AuthInfo,
	datasets []seedDataset,
) (setupDatasetActivationSummary, error) {
	result := setupDatasetActivationSummary{Total: len(datasets)}
	if api == nil {
		return result, fmt.Errorf("storage_metadata_unavailable")
	}
	for _, item := range datasets {
		spaceID, datasetID := strings.TrimSpace(item.SpaceID), strings.TrimSpace(item.DatasetID)
		current, err := api.GetDataset(ctx, &storagepb.GetDatasetReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
		})
		if err != nil || current == nil || current.GetRetInfo() == nil ||
			current.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || current.GetDataset() == nil {
			return result, fmt.Errorf("dataset_activation_read_failed: %s/%s", spaceID, datasetID)
		}
		if current.GetDataset().GetStatus() == "active" && current.GetDataset().GetBindingLocked() {
			result.Unchanged++
			continue
		}
		check, err := api.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
		})
		if err != nil || check == nil || check.GetRetInfo() == nil ||
			check.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return result, fmt.Errorf("dataset_activation_check_failed: %s/%s", spaceID, datasetID)
		}
		if !check.GetReady() {
			return result, fmt.Errorf(
				"dataset_activation_not_ready: %s/%s: %s",
				spaceID, datasetID, summarizeDatasetActivationChecks(check.GetChecks()),
			)
		}
		activated, err := api.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
			ExpectedRevision: check.GetDatasetRevision(),
		})
		if err != nil || activated == nil || activated.GetRetInfo() == nil ||
			activated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS ||
			activated.GetDataset() == nil ||
			activated.GetDataset().GetStatus() != "active" ||
			!activated.GetDataset().GetBindingLocked() {
			return result, fmt.Errorf("dataset_activation_failed: %s/%s", spaceID, datasetID)
		}
		result.Activated++
	}
	return result, nil
}

func summarizeDatasetActivationChecks(checks []*storagepb.DatasetActivationCheck) string {
	if len(checks) == 0 {
		return "no checks returned"
	}
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		if check == nil {
			continue
		}
		state := "not_ready"
		if check.GetReady() {
			state = "ready"
		}
		part := strings.TrimSpace(check.GetCheckId()) + "=" + state
		if summary := strings.TrimSpace(check.GetSummary()); summary != "" {
			part += "(" + summary + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "no checks returned"
	}
	return strings.Join(parts, ", ")
}

var _ setupInitStorage = (*remoteSetupInitStorage)(nil)
var _ datasetActivationAPI = (storageMetadataAPI)(nil)

// setupBrowserTrustSummary 记录初始化时已在本机安装信任的控制台内部 CA，以及其他机器怎么快速做同样的事。
type setupBrowserTrustSummary struct {
	Status        string `json:"status"`
	CAPath        string `json:"ca_path"`
	Fingerprint   string `json:"fingerprint,omitempty"`
	OtherMachines string `json:"other_machines"`
}

// describeSetupBrowserTrust 只在控制台使用内部 CA 时返回摘要；公共证书不需要本机信任，返回 nil。
func describeSetupBrowserTrust(snapshot *setupconfig.Snapshot) *setupBrowserTrustSummary {
	if snapshot == nil {
		return nil
	}
	host := snapshot.Manifest.ControlHost()
	if !tlstrust.RequiresLocalCATrust(tlstrust.TLSMode(host.TLSMode), host.Address) {
		return nil
	}
	caPath := tlstrust.CAPath(host.Address)
	return &setupBrowserTrustSummary{
		Status:      "trusted",
		CAPath:      caPath,
		Fingerprint: tlstrust.Fingerprint(caPath),
		OtherMachines: fmt.Sprintf("换电脑或迁移机器：打开 https://%s:9527 的登录页或“秘钥管理”页，点“下载并安装根证书”，下载安装包后核对指纹，"+
			"向运维或已信任的机器取得根证书指纹，按安装包里 AGENT-INSTALL.md 的“直接安装”用系统命令安装（可把该文件交给 Agent，由它先向你确认指纹）；"+
			"不要不加检查地运行包内脚本。有仓库、moox.toml 和控制机 SSH 权限的机器更稳妥：先用可信渠道拿到 SSH 主机指纹，运行 "+
			"moox-cli setup trust-host --file moox.toml --host control --fingerprint <指纹>，再运行 moox-cli setup trust-browser --file moox.toml", host.Address),
	}
}
