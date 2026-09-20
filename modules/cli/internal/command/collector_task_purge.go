package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/client"
)

type collectorTaskPurgeFlags struct {
	File                  string
	DBPath                string
	SpaceID               string
	ControlURL            string
	AccessToken           string
	ServiceAccessKey      string
	ServiceSecretKey      string
	StopCommand           string
	BackupDir             string
	SeedFile              string
	CollectorInitBin      string
	MetadataTarget        string
	MetadataURL           string
	MetadataServiceKey    string
	MetadataServiceSecret string
	Apply                 bool
	DryRun                bool
	DryRunSet             bool
	Confirm               bool
}

type collectorTaskResultRef struct {
	SpaceID         string `gorm:"column:c_space_id" json:"space_id"`
	TaskID          string `gorm:"column:c_task_id" json:"task_id"`
	TaskName        string `gorm:"column:c_task_name" json:"task_name"`
	ResultViewID    string `gorm:"column:c_result_view_id" json:"result_view_id"`
	ResultDatasetID string `gorm:"column:c_result_dataset_id" json:"result_dataset_id"`
}

type collectorStorageObjectRef struct {
	SpaceID  string
	ObjectID string
}

type collectorTaskPurgeSummary struct {
	Status                    string `json:"status"`
	DryRun                    bool   `json:"dry_run"`
	SpaceID                   string `json:"space_id"`
	DBPath                    string `json:"db_path"`
	BackupPath                string `json:"backup_path,omitempty"`
	TaskCount                 int64  `json:"task_count"`
	TaskInstanceCount         int64  `json:"task_instance_count"`
	FetchBatchCount           int64  `json:"fetch_batch_count"`
	FetchRetryCount           int64  `json:"fetch_retry_count"`
	LocalResultReferenceCount int    `json:"local_result_reference_count"`
	UnlinkedResultCount       int64  `json:"unlinked_result_count"`
	// TaskIDs retains the historical JSON key, but every item is a complete
	// space-scoped task/result reference rather than a bare task ID.
	TaskIDs                   []collectorTaskResultRef `json:"task_ids,omitempty"`
	Results                   []collectorTaskResultRef `json:"results,omitempty"`
	StorageInventoryStatus    string                   `json:"storage_inventory_status"`
	StorageInventoryReason    string                   `json:"storage_inventory_reason,omitempty"`
	StorageOwnedDatasetCount  int                      `json:"storage_owned_dataset_count"`
	StorageOrphanDatasetCount int                      `json:"storage_orphan_dataset_count"`
	StorageOwnedViewCount     int                      `json:"storage_owned_view_count"`
	StorageOrphanViewCount    int                      `json:"storage_orphan_view_count"`
	StorageDatasetIDs         []string                 `json:"storage_dataset_ids,omitempty"`
	StorageViewIDs            []string                 `json:"storage_view_ids,omitempty"`
	CompletedStages           []string                 `json:"completed_stages,omitempty"`
	storageDatasetRefs        []collectorStorageObjectRef
	storageViewRefs           []collectorStorageObjectRef
}

var collectorTaskPurgeFlagsValue collectorTaskPurgeFlags

type collectorStorageMetadataClient interface {
	ListDatasets(context.Context, *storagepb.ListDatasetsReq) (*storagepb.ListDatasetsRsp, error)
	ListViews(context.Context, *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error)
	DeleteDatasetRows(context.Context, *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error)
}

type collectorStorageMetadataProxy struct {
	proxy   storagepb.MetadataClientProxy
	primary storagepb.PrimaryStoreClientProxy
	options []client.Option
	auth    *storagepb.AuthInfo
}

var newCollectorStorageMetadataClient = func(target, protocol, accessKey, secret string) collectorStorageMetadataClient {
	accessKey = firstNonEmpty(strings.TrimSpace(accessKey), "storage-metadata")
	options := []client.Option{
		client.WithTarget(target),
		client.WithNetwork("tcp"),
		client.WithProtocol(protocol),
	}
	return &collectorStorageMetadataProxy{
		proxy:   storagepb.NewMetadataClientProxy(options...),
		primary: storagepb.NewPrimaryStoreClientProxy(options...),
		options: options,
		auth: &storagepb.AuthInfo{
			AppId:  accessKey,
			AppKey: security.HMACSHA256Hex(secret, []byte(accessKey)),
		},
	}
}

func (c *collectorStorageMetadataProxy) ListDatasets(ctx context.Context, req *storagepb.ListDatasetsReq) (*storagepb.ListDatasetsRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, fmt.Errorf("storage metadata client is unavailable")
	}
	request := &storagepb.ListDatasetsReq{}
	if req != nil {
		copy := *req
		request = &copy
	}
	request.AuthInfo = c.auth
	return c.proxy.ListDatasets(ctx, request, c.options...)
}

func (c *collectorStorageMetadataProxy) ListViews(ctx context.Context, req *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, fmt.Errorf("storage metadata client is unavailable")
	}
	request := &storagepb.ListViewsReq{}
	if req != nil {
		copy := *req
		request = &copy
	}
	request.AuthInfo = c.auth
	return c.proxy.ListViews(ctx, request, c.options...)
}

func (c *collectorStorageMetadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, fmt.Errorf("storage metadata client is unavailable")
	}
	request := &storagepb.DeleteDatasetReq{}
	if req != nil {
		copy := *req
		request = &copy
	}
	request.AuthInfo = c.auth
	return c.proxy.DeleteDataset(ctx, request, c.options...)
}

func (c *collectorStorageMetadataProxy) DeleteView(ctx context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, fmt.Errorf("storage metadata client is unavailable")
	}
	request := &storagepb.DeleteViewReq{}
	if req != nil {
		copy := *req
		request = &copy
	}
	request.AuthInfo = c.auth
	return c.proxy.DeleteView(ctx, request, c.options...)
}

func (c *collectorStorageMetadataProxy) DeleteDatasetRows(ctx context.Context, req *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	if c == nil || c.primary == nil {
		return nil, fmt.Errorf("storage primary client is unavailable")
	}
	request := &storagepb.PrimaryDeleteDatasetRowsReq{}
	if req != nil {
		copy := *req
		request = &copy
	}
	request.AuthInfo = c.auth
	return c.primary.DeleteDatasetRows(ctx, request, c.options...)
}

var collectorTaskCmd = &cobra.Command{
	Use:   "task",
	Short: "采集任务维护工具",
}

var collectorTaskPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "清理 Collector 任务运行数据和结果引用",
	Long:  "默认只输出清单，不修改任何数据。执行清理必须同时指定 --apply --confirm；--apply 与 --dry-run 互斥。apply 会先执行 --stop-command，成功后才删除任务、运行记录和结果，只有全空间模式才重建 Schema。",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		flags := collectorTaskPurgeFlagsValue
		flags.DryRunSet = cmd.Flags().Changed("dry-run")
		summary, err := runCollectorTaskPurge(cmd.Context(), flags)
		if summary != nil {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if encodeErr := encoder.Encode(summary); encodeErr != nil && err == nil {
				return encodeErr
			}
		}
		return err
	},
}

func init() {
	collectorCmd.AddCommand(collectorTaskCmd)
	collectorTaskCmd.AddCommand(collectorTaskPurgeCmd)

	flags := collectorTaskPurgeCmd.Flags()
	flags.StringVar(&collectorTaskPurgeFlagsValue.File, "file", "", "部署配置文件；用于解析数据库路径和可选 StorageRPCGatewayTarget")
	flags.StringVar(&collectorTaskPurgeFlagsValue.DBPath, "db-path", "", "Collector SQLite 数据库路径；默认取 MOOX_COLLECTOR_DB_PATH 或 ./data/moox_collector.db")
	flags.StringVar(&collectorTaskPurgeFlagsValue.SpaceID, "space-id", "", "只清理指定空间；留空表示清理全部空间")
	flags.StringVar(&collectorTaskPurgeFlagsValue.ControlURL, "control-url", "", "保留兼容；apply 不再调用控制面 DeleteTask，避免停机前物理删除结果")
	flags.StringVar(&collectorTaskPurgeFlagsValue.AccessToken, "access-token", "", "控制面登录态；默认取 MOOX_ACCESS_TOKEN")
	flags.StringVar(&collectorTaskPurgeFlagsValue.ServiceAccessKey, "service-access-key", "", "控制面服务签名 key id")
	flags.StringVar(&collectorTaskPurgeFlagsValue.ServiceSecretKey, "service-secret-key", "", "控制面服务签名 secret")
	flags.StringVar(&collectorTaskPurgeFlagsValue.StopCommand, "stop-command", "", "执行 apply 时首先停止 Collector 写入的本地命令")
	flags.StringVar(&collectorTaskPurgeFlagsValue.BackupDir, "backup-dir", "", "备份目录；默认写入数据库所在目录")
	flags.StringVar(&collectorTaskPurgeFlagsValue.SeedFile, "seed-file", "", "重建 Schema 后使用的 Collector 任务种子文件")
	flags.StringVar(&collectorTaskPurgeFlagsValue.CollectorInitBin, "collector-init-bin", "moox-collector-cli", "Collector CLI 初始化程序路径")
	flags.StringVar(&collectorTaskPurgeFlagsValue.MetadataTarget, "metadata-target", "", "Storage Metadata tRPC target；未指定时尝试从 --file 的 StorageRPCGatewayTarget 读取")
	flags.StringVar(&collectorTaskPurgeFlagsValue.MetadataURL, "metadata-url", "", "Storage Metadata HTTP URL；与 --metadata-target 二选一")
	flags.StringVar(&collectorTaskPurgeFlagsValue.MetadataServiceKey, "metadata-service-key", "", "Storage Metadata AuthInfo app_id；默认 storage-metadata")
	flags.StringVar(&collectorTaskPurgeFlagsValue.MetadataServiceSecret, "metadata-service-secret", "", "Storage Metadata AuthInfo secret；默认取 MOOX_STORAGE_NODE_AUTH_SECRET")
	flags.StringVar(&collectorTaskPurgeFlagsValue.MetadataServiceKey, "metadata-service-access-key", "", "Storage Metadata AuthInfo app_id；--metadata-service-key 的别名")
	flags.StringVar(&collectorTaskPurgeFlagsValue.MetadataServiceSecret, "metadata-service-secret-key", "", "Storage Metadata AuthInfo secret；--metadata-service-secret 的别名")
	flags.BoolVar(&collectorTaskPurgeFlagsValue.Apply, "apply", false, "执行删除；必须同时指定 --confirm")
	flags.BoolVar(&collectorTaskPurgeFlagsValue.DryRun, "dry-run", false, "只输出清单，不修改文件或远端；默认行为")
	flags.BoolVar(&collectorTaskPurgeFlagsValue.Confirm, "confirm", false, "确认执行破坏性重置")
}

func runCollectorTaskPurge(ctx context.Context, flags collectorTaskPurgeFlags) (*collectorTaskPurgeSummary, error) {
	dbPath := strings.TrimSpace(flags.DBPath)
	var snapshot *setupconfig.Snapshot
	if strings.TrimSpace(flags.File) != "" {
		root, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve collector purge config root: %w", err)
		}
		snapshot, err = setupconfig.Load(flags.File, root)
		if err != nil {
			return nil, fmt.Errorf("load collector purge config: %w", err)
		}
		if dbPath == "" {
			paths := snapshot.Manifest.Paths.Resolved()
			dbPath = filepath.Join(paths.ControlRoot, "data", "moox_collector.db")
		}
	}
	if dbPath == "" {
		dbPath = strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_DB_PATH"))
	}
	if dbPath == "" {
		dbPath = "./data/moox_collector.db"
	}
	dbPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve collector database path: %w", err)
	}

	summary := &collectorTaskPurgeSummary{
		Status:                 "dry_run",
		DryRun:                 true,
		SpaceID:                strings.TrimSpace(flags.SpaceID),
		DBPath:                 dbPath,
		Results:                []collectorTaskResultRef{},
		StorageInventoryStatus: "unavailable",
	}

	if flags.Apply && (flags.DryRun || flags.DryRunSet) {
		summary.StorageInventoryReason = "apply and dry-run are mutually exclusive"
		return summary, fmt.Errorf("collector task purge cannot combine --apply and --dry-run")
	}
	if flags.Apply && !flags.Confirm {
		summary.StorageInventoryReason = "apply requires --confirm"
		return summary, fmt.Errorf("collector task purge requires both --apply and --confirm")
	}

	if err := inspectCollectorTaskPurge(dbPath, summary); err != nil {
		return summary, err
	}

	metadataTarget, metadataProtocol, metadataReason, err := resolveCollectorMetadataEndpoint(flags, snapshot)
	if err != nil {
		return summary, err
	}
	var metadataClient collectorStorageMetadataClient
	if metadataTarget == "" {
		summary.StorageInventoryReason = metadataReason
	} else {
		metadataSecret := firstNonEmpty(
			strings.TrimSpace(flags.MetadataServiceSecret),
			os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET"),
			strings.TrimSpace(flags.ServiceSecretKey),
		)
		metadataKey := firstNonEmpty(
			strings.TrimSpace(flags.MetadataServiceKey),
			strings.TrimSpace(flags.ServiceAccessKey),
			"storage-metadata",
		)
		metadataClient = newCollectorStorageMetadataClient(metadataTarget, metadataProtocol, metadataKey, metadataSecret)
		if err := inspectCollectorStorageInventory(ctx, metadataClient, summary); err != nil {
			resetCollectorStorageInventory(summary)
			summary.StorageInventoryStatus = "unavailable"
			summary.StorageInventoryReason = err.Error()
		}
	}
	if !flags.Apply {
		if summary.StorageInventoryStatus != "available" && summary.StorageInventoryReason == "" {
			summary.StorageInventoryReason = "metadata inventory was not configured"
		}
		return summary, nil
	}

	if err := validateCollectorTaskPurgeApply(flags, summary); err != nil {
		return summary, err
	}

	summary.Status = "applying"
	summary.DryRun = false
	if err := applyCollectorTaskPurge(ctx, flags, summary, metadataClient); err != nil {
		summary.Status = "apply_failed"
		return summary, err
	}
	summary.Status = "applied"
	return summary, nil
}

func inspectCollectorTaskPurge(dbPath string, summary *collectorTaskPurgeSummary) error {
	if summary == nil {
		return fmt.Errorf("collector task purge summary is nil")
	}
	if summary.StorageInventoryStatus == "" {
		summary.StorageInventoryStatus = "unavailable"
	}
	if summary.StorageInventoryReason == "" {
		summary.StorageInventoryReason = "metadata inventory was not configured"
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("collector database not found: %s", dbPath)
		}
		return fmt.Errorf("stat collector database: %w", err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+dbPath+"?mode=ro"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open collector database read-only: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get collector database handle: %w", err)
	}
	defer sqlDB.Close()
	space := strings.TrimSpace(summary.SpaceID)
	where := ""
	args := []any{}
	if space != "" {
		where = " WHERE c_space_id = ?"
		args = append(args, space)
	}
	for _, item := range []struct {
		name  string
		table string
		out   *int64
	}{
		{name: "tasks", table: "t_collector_tasks", out: &summary.TaskCount},
		{name: "task instances", table: "t_collector_task_instances", out: &summary.TaskInstanceCount},
		{name: "fetch batches", table: "t_collector_fetch_batches", out: &summary.FetchBatchCount},
		{name: "fetch retries", table: "t_collector_fetch_retry_items", out: &summary.FetchRetryCount},
	} {
		if !collectorTableExists(db, item.table) {
			continue
		}
		query := "SELECT count(*) FROM " + item.table + where
		if err := db.Raw(query, args...).Scan(item.out).Error; err != nil {
			return fmt.Errorf("count collector %s: %w", item.name, err)
		}
	}
	if collectorTableExists(db, "t_collector_tasks") {
		var refs []collectorTaskResultRef
		query := db.Raw("SELECT c_space_id, c_task_id, c_task_name, c_result_view_id, c_result_dataset_id FROM t_collector_tasks"+where+" ORDER BY c_id", args...)
		if err := query.Scan(&refs).Error; err != nil {
			return fmt.Errorf("list collector-owned results: %w", err)
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref.TaskID) != "" {
				summary.TaskIDs = append(summary.TaskIDs, ref)
			}
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref.ResultViewID) == "" || strings.TrimSpace(ref.ResultDatasetID) == "" {
				summary.UnlinkedResultCount++
				continue
			}
			summary.Results = append(summary.Results, ref)
		}
		summary.LocalResultReferenceCount = len(summary.Results)
	}
	return nil
}

func collectorTableExists(db *gorm.DB, table string) bool {
	var count int64
	return db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error == nil && count > 0
}

func resolveCollectorMetadataEndpoint(flags collectorTaskPurgeFlags, snapshot *setupconfig.Snapshot) (string, string, string, error) {
	explicitTarget := strings.TrimSpace(flags.MetadataTarget)
	explicitURL := strings.TrimSpace(flags.MetadataURL)
	if explicitTarget != "" && explicitURL != "" {
		return "", "", "", fmt.Errorf("collector task purge accepts only one of --metadata-target and --metadata-url")
	}
	if explicitURL != "" {
		return explicitURL, "http", "", nil
	}
	if explicitTarget != "" {
		return explicitTarget, collectorMetadataProtocol(explicitTarget), "", nil
	}

	targets := collectorManifestStorageTargets(snapshot, strings.TrimSpace(flags.SpaceID))
	switch len(targets) {
	case 0:
		return "", "", "no metadata target/url was supplied and --file has no StorageRPCGatewayTarget", nil
	case 1:
		return targets[0], collectorMetadataProtocol(targets[0]), "", nil
	default:
		return "", "", "manifest contains multiple StorageRPCGatewayTarget values; specify --metadata-target or --metadata-url", nil
	}
}

func collectorManifestStorageTargets(snapshot *setupconfig.Snapshot, spaceID string) []string {
	if snapshot == nil {
		return nil
	}
	seen := make(map[string]struct{})
	targets := make([]string, 0, len(snapshot.Manifest.SCFFetcher.Spaces))
	for _, space := range snapshot.Manifest.SCFFetcher.Spaces {
		if spaceID != "" && strings.TrimSpace(space.SpaceID) != spaceID {
			continue
		}
		target := strings.TrimSpace(space.StorageRPCGatewayTarget)
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

func collectorMetadataProtocol(target string) string {
	lower := strings.ToLower(strings.TrimSpace(target))
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "http"
	}
	return "trpc"
}

func inspectCollectorStorageInventory(ctx context.Context, metadata collectorStorageMetadataClient, summary *collectorTaskPurgeSummary) error {
	if metadata == nil {
		return fmt.Errorf("storage metadata client is unavailable")
	}
	if summary == nil {
		return fmt.Errorf("collector task purge summary is nil")
	}
	taskKeys := make(map[string]struct{}, len(summary.TaskIDs))
	for _, task := range summary.TaskIDs {
		taskKeys[collectorTaskPurgeKey(task.SpaceID, task.TaskID)] = struct{}{}
	}

	const (
		pageSize = uint32(500)
		maxPages = 10000
	)
	for page := uint32(1); page <= maxPages; page++ {
		response, err := metadata.ListDatasets(ctx, &storagepb.ListDatasetsReq{
			SpaceId: strings.TrimSpace(summary.SpaceID),
			Page:    &storagepb.Page{Page: page, Size: pageSize},
		})
		if err != nil {
			return fmt.Errorf("ListDatasets failed: %w", err)
		}
		if response == nil {
			return fmt.Errorf("ListDatasets returned no response")
		}
		if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return fmt.Errorf("ListDatasets rejected")
		}
		for _, dataset := range response.GetDatasets() {
			if dataset == nil || !collectorStorageObjectMatchesSpace(dataset.GetSpaceId(), summary.SpaceID) {
				continue
			}
			taskID, ok := collectorStorageObjectTaskID(dataset.GetAttributes())
			if !ok {
				continue
			}
			summary.StorageOwnedDatasetCount++
			summary.StorageDatasetIDs = appendUniqueString(summary.StorageDatasetIDs, dataset.GetDatasetId())
			summary.storageDatasetRefs = appendUniqueStorageObject(summary.storageDatasetRefs, dataset.GetSpaceId(), dataset.GetDatasetId())
			if _, exists := taskKeys[collectorTaskPurgeKey(dataset.GetSpaceId(), taskID)]; !exists {
				summary.StorageOrphanDatasetCount++
			}
		}
		pageResult := response.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() {
			break
		}
		if page == maxPages {
			return fmt.Errorf("ListDatasets exceeded %d pages", maxPages)
		}
	}

	for page := uint32(1); page <= maxPages; page++ {
		response, err := metadata.ListViews(ctx, &storagepb.ListViewsReq{
			SpaceId: strings.TrimSpace(summary.SpaceID),
			Page:    &storagepb.Page{Page: page, Size: pageSize},
		})
		if err != nil {
			return fmt.Errorf("ListViews failed: %w", err)
		}
		if response == nil {
			return fmt.Errorf("ListViews returned no response")
		}
		if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return fmt.Errorf("ListViews rejected")
		}
		for _, view := range response.GetViews() {
			if view == nil || !collectorStorageObjectMatchesSpace(view.GetSpaceId(), summary.SpaceID) {
				continue
			}
			taskID, ok := collectorStorageObjectTaskID(view.GetAttributes())
			if !ok {
				continue
			}
			summary.StorageOwnedViewCount++
			summary.StorageViewIDs = appendUniqueString(summary.StorageViewIDs, view.GetViewId())
			summary.storageViewRefs = appendUniqueStorageObject(summary.storageViewRefs, view.GetSpaceId(), view.GetViewId())
			if _, exists := taskKeys[collectorTaskPurgeKey(view.GetSpaceId(), taskID)]; !exists {
				summary.StorageOrphanViewCount++
			}
		}
		pageResult := response.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() {
			break
		}
		if page == maxPages {
			return fmt.Errorf("ListViews exceeded %d pages", maxPages)
		}
	}

	summary.StorageInventoryStatus = "available"
	summary.StorageInventoryReason = ""
	return nil
}

func collectorStorageObjectMatchesSpace(objectSpaceID, requestedSpaceID string) bool {
	requestedSpaceID = strings.TrimSpace(requestedSpaceID)
	return requestedSpaceID == "" || strings.TrimSpace(objectSpaceID) == requestedSpaceID
}

func collectorStorageObjectTaskID(attributes map[string]string) (string, bool) {
	if strings.TrimSpace(attributes["owner_module"]) != "collector" {
		return "", false
	}
	taskID := strings.TrimSpace(attributes["collector_task_id"])
	return taskID, taskID != ""
}

func resetCollectorStorageInventory(summary *collectorTaskPurgeSummary) {
	if summary == nil {
		return
	}
	summary.StorageOwnedDatasetCount = 0
	summary.StorageOrphanDatasetCount = 0
	summary.StorageOwnedViewCount = 0
	summary.StorageOrphanViewCount = 0
	summary.StorageDatasetIDs = nil
	summary.StorageViewIDs = nil
	summary.storageDatasetRefs = nil
	summary.storageViewRefs = nil
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueStorageObject(values []collectorStorageObjectRef, spaceID, objectID string) []collectorStorageObjectRef {
	spaceID, objectID = strings.TrimSpace(spaceID), strings.TrimSpace(objectID)
	if spaceID == "" || objectID == "" {
		return values
	}
	for _, existing := range values {
		if existing.SpaceID == spaceID && existing.ObjectID == objectID {
			return values
		}
	}
	return append(values, collectorStorageObjectRef{SpaceID: spaceID, ObjectID: objectID})
}

func collectorTaskPurgeKey(spaceID, taskID string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(taskID)
}

func validateCollectorTaskPurgeApply(flags collectorTaskPurgeFlags, summary *collectorTaskPurgeSummary) error {
	if summary == nil {
		return fmt.Errorf("collector task purge summary is nil")
	}
	if summary.StorageInventoryStatus != "available" {
		return fmt.Errorf("collector task purge apply requires a complete Storage-owned View/Dataset inventory; run dry-run with Metadata target and fix: %s", summary.StorageInventoryReason)
	}
	if err := validateCollectorTaskPurgeRefs(summary.TaskIDs); err != nil {
		return err
	}
	if strings.TrimSpace(flags.StopCommand) == "" {
		return fmt.Errorf("collector task purge apply requires --stop-command to stop Collector writes")
	}
	if key, secret := strings.TrimSpace(flags.ServiceAccessKey), strings.TrimSpace(flags.ServiceSecretKey); key != "" || secret != "" {
		if key == "" || secret == "" {
			return fmt.Errorf("service access key and secret must be provided together")
		}
	}
	if strings.TrimSpace(flags.SpaceID) == "" {
		initBin := strings.TrimSpace(flags.CollectorInitBin)
		if initBin == "" {
			return fmt.Errorf("collector init program is required")
		}
		if _, err := exec.LookPath(initBin); err != nil && !filepath.IsAbs(initBin) {
			return fmt.Errorf("collector init program %q is unavailable: %w", initBin, err)
		}
	}
	return nil
}

func validateCollectorTaskPurgeRefs(tasks []collectorTaskResultRef) error {
	for _, task := range tasks {
		if strings.TrimSpace(task.SpaceID) == "" {
			return fmt.Errorf("collector task %q has no space_id; refusing to purge without a space-scoped reference", task.TaskID)
		}
		if strings.TrimSpace(task.TaskID) == "" {
			return fmt.Errorf("collector task reference has no task_id")
		}
	}
	return nil
}

func applyCollectorTaskPurge(ctx context.Context, flags collectorTaskPurgeFlags, summary *collectorTaskPurgeSummary, metadata collectorStorageMetadataClient) error {
	if summary == nil {
		return fmt.Errorf("collector task purge summary is nil")
	}
	stopCommand := strings.TrimSpace(flags.StopCommand)
	if stopCommand == "" {
		return fmt.Errorf("collector task purge apply requires --stop-command to stop Collector writes")
	}
	backupPath, err := backupCollectorDatabase(ctx, summary.DBPath, flags.BackupDir)
	if err != nil {
		return err
	}
	summary.BackupPath = backupPath
	summary.CompletedStages = append(summary.CompletedStages, "collector_database_backed_up")

	if output, err := exec.CommandContext(ctx, "sh", "-c", stopCommand).CombinedOutput(); err != nil {
		return fmt.Errorf("stop Collector before local purge: %w: %s", err, strings.TrimSpace(string(output)))
	}
	summary.CompletedStages = append(summary.CompletedStages, "collector_writes_stopped")

	if err := deleteCollectorTaskData(ctx, summary.DBPath, summary.TaskIDs, flags.SpaceID); err != nil {
		return fmt.Errorf("delete Collector task runtime data: %w", err)
	}
	if len(summary.TaskIDs) > 0 {
		summary.CompletedStages = append(summary.CompletedStages, "collector_runtime_deleted")
	}
	if metadata != nil {
		if err := deleteCollectorOwnedMetadata(ctx, metadata, summary); err != nil {
			return err
		}
	}
	if strings.TrimSpace(flags.SpaceID) != "" {
		summary.CompletedStages = append(summary.CompletedStages, "collector_space_purged")
		return nil
	}

	initBin := strings.TrimSpace(flags.CollectorInitBin)
	if initBin == "" {
		return fmt.Errorf("collector init program is required")
	}
	if _, err := exec.LookPath(initBin); err != nil && !filepath.IsAbs(initBin) {
		return fmt.Errorf("collector init program %q is unavailable: %w", initBin, err)
	}
	resetPath := summary.DBPath + ".purge-reset-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(summary.DBPath, resetPath); err != nil {
		return fmt.Errorf("move cleaned collector database before schema initialization: %w", err)
	}
	summary.CompletedStages = append(summary.CompletedStages, "collector_database_reset_for_init")
	args := []string{"init", "--db-path", summary.DBPath}
	if strings.TrimSpace(flags.SeedFile) != "" {
		args = append(args, "--seed-file", flags.SeedFile)
	}
	command := exec.CommandContext(ctx, initBin, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("initialize collector schema after backup: %w: %s", err, strings.TrimSpace(string(output)))
	}
	summary.CompletedStages = append(summary.CompletedStages, "collector_schema_initialized")
	return nil
}

func backupCollectorDatabase(ctx context.Context, dbPath, requestedDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backupDir := strings.TrimSpace(requestedDir)
	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(dbPath), "collector-reset-backups")
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create collector backup directory: %w", err)
	}
	backupPath := filepath.Join(backupDir, filepath.Base(dbPath)+"."+time.Now().UTC().Format("20060102T150405Z"))
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("stat collector database before backup: %w", err)
	}
	// VACUUM INTO creates a consistent SQLite snapshot while preserving the
	// source database. It also folds WAL contents into the backup, unlike a
	// plain file copy made while SQLite is using WAL mode.
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return "", fmt.Errorf("open collector database for backup: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return "", fmt.Errorf("get collector backup database handle: %w", err)
	}
	defer sqlDB.Close()
	if err := db.WithContext(ctx).Exec("VACUUM INTO ?", backupPath).Error; err != nil {
		return "", fmt.Errorf("backup collector database: %w", err)
	}
	return backupPath, nil
}

// deleteCollectorOwnedMetadata removes the complete Collector-owned metadata
// inventory captured by the dry-run after Collector writes have been stopped.
func deleteCollectorOwnedMetadata(ctx context.Context, metadata collectorStorageMetadataClient, summary *collectorTaskPurgeSummary) error {
	if metadata == nil || summary == nil {
		return nil
	}
	for _, object := range summary.storageViewRefs {
		response, err := metadata.DeleteView(ctx, &storagepb.DeleteViewReq{
			SpaceId: object.SpaceID,
			ViewId:  object.ObjectID,
		})
		if err != nil || !collectorDeleteViewAccepted(response) {
			return fmt.Errorf("delete Collector-owned View %s/%s: %w", object.SpaceID, object.ObjectID, collectorDeleteResponseError(err, response))
		}
	}
	if len(summary.storageViewRefs) > 0 {
		summary.CompletedStages = append(summary.CompletedStages, "collector_owned_views_deleted")
	}
	for _, object := range summary.storageDatasetRefs {
		physical, physicalErr := metadata.DeleteDatasetRows(ctx, &storagepb.PrimaryDeleteDatasetRowsReq{
			SpaceId: object.SpaceID, DatasetId: object.ObjectID,
		})
		if physicalErr != nil || !collectorDeleteDatasetRowsAccepted(physical) {
			return fmt.Errorf("delete Collector-owned Dataset rows %s/%s: %w", object.SpaceID, object.ObjectID, collectorDeleteRowsResponseError(physicalErr, physical))
		}
		response, err := metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{
			SpaceId:   object.SpaceID,
			DatasetId: object.ObjectID,
		})
		if err != nil || !collectorDeleteDatasetAccepted(response) {
			return fmt.Errorf("delete Collector-owned Dataset %s/%s: %w", object.SpaceID, object.ObjectID, collectorDeleteResponseError(err, response))
		}
	}
	if len(summary.storageDatasetRefs) > 0 {
		summary.CompletedStages = append(summary.CompletedStages, "collector_owned_datasets_deleted")
	}
	return nil
}

func collectorDeleteDatasetRowsAccepted(response *storagepb.PrimaryDeleteDatasetRowsRsp) bool {
	if response == nil || response.GetRetInfo() == nil {
		return false
	}
	switch response.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS, storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

func collectorDeleteRowsResponseError(callErr error, response *storagepb.PrimaryDeleteDatasetRowsRsp) error {
	if callErr != nil {
		return callErr
	}
	if response == nil || response.GetRetInfo() == nil {
		return fmt.Errorf("empty response")
	}
	return fmt.Errorf("%s", response.GetRetInfo().GetMsg())
}

func collectorDeleteViewAccepted(response *storagepb.DeleteViewRsp) bool {
	if response == nil || response.GetRetInfo() == nil {
		return false
	}
	switch response.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS, storagepb.ErrorCode_VIEW_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

func collectorDeleteDatasetAccepted(response *storagepb.DeleteDatasetRsp) bool {
	if response == nil || response.GetRetInfo() == nil {
		return false
	}
	switch response.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS, storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

func collectorDeleteResponseError(callErr error, response interface{ GetRetInfo() *storagepb.RetInfo }) error {
	if callErr != nil {
		return callErr
	}
	if response == nil || response.GetRetInfo() == nil {
		return fmt.Errorf("empty response")
	}
	return fmt.Errorf("%s", response.GetRetInfo().GetMsg())
}

func deleteCollectorTaskData(ctx context.Context, dbPath string, tasks []collectorTaskResultRef, requestedSpaceID string) error {
	requestedSpaceID = strings.TrimSpace(requestedSpaceID)
	if len(tasks) == 0 && requestedSpaceID == "" {
		return nil
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open collector database for purge: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get collector purge database handle: %w", err)
	}
	defer sqlDB.Close()

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if requestedSpaceID != "" {
			if collectorTableExists(tx, "t_period_readiness_items") && collectorTableExists(tx, "t_collector_task_instances") {
				if err := tx.Exec(`DELETE FROM t_period_readiness_items WHERE c_instance_id IN (SELECT c_instance_id FROM t_collector_task_instances WHERE c_space_id = ?)`, requestedSpaceID).Error; err != nil {
					return err
				}
			}
			for _, table := range []string{
				"t_collector_task_instances",
				"t_collector_fetch_batches",
				"t_collector_fetch_retry_items",
			} {
				if !collectorTableExists(tx, table) {
					continue
				}
				if err := tx.Exec("DELETE FROM "+table+" WHERE c_space_id = ?", requestedSpaceID).Error; err != nil {
					return err
				}
			}
			if collectorTableExists(tx, "t_collector_tasks") {
				if err := tx.Exec(`DELETE FROM t_collector_tasks WHERE c_space_id = ?`, requestedSpaceID).Error; err != nil {
					return err
				}
			}
			if collectorTableExists(tx, "t_period_readiness") && collectorTableExists(tx, "t_period_readiness_items") {
				if err := tx.Exec(`DELETE FROM t_period_readiness WHERE c_space_id = ? AND NOT EXISTS (SELECT 1 FROM t_period_readiness_items WHERE c_readiness_id = t_period_readiness.c_id)`, requestedSpaceID).Error; err != nil {
					return err
				}
			}
			return nil
		}

		for _, task := range tasks {
			spaceID := strings.TrimSpace(task.SpaceID)
			taskID := strings.TrimSpace(task.TaskID)
			if taskID == "" {
				continue
			}
			if collectorTableExists(tx, "t_period_readiness_items") && collectorTableExists(tx, "t_collector_task_instances") {
				if err := tx.Exec(`DELETE FROM t_period_readiness_items WHERE c_instance_id IN (SELECT c_instance_id FROM t_collector_task_instances WHERE c_space_id = ? AND c_task_id = ?)`, spaceID, taskID).Error; err != nil {
					return err
				}
			}
			for _, table := range []string{
				"t_collector_task_instances",
				"t_collector_fetch_batches",
				"t_collector_fetch_retry_items",
			} {
				if !collectorTableExists(tx, table) {
					continue
				}
				if err := tx.Exec("DELETE FROM "+table+" WHERE c_space_id = ? AND c_task_id = ?", spaceID, taskID).Error; err != nil {
					return err
				}
			}
			if collectorTableExists(tx, "t_collector_tasks") {
				if err := tx.Exec(`DELETE FROM t_collector_tasks WHERE c_space_id = ? AND c_task_id = ?`, spaceID, taskID).Error; err != nil {
					return err
				}
			}
		}
		if collectorTableExists(tx, "t_period_readiness") && collectorTableExists(tx, "t_period_readiness_items") {
			spaces := make(map[string]struct{}, len(tasks))
			for _, task := range tasks {
				if spaceID := strings.TrimSpace(task.SpaceID); spaceID != "" {
					spaces[spaceID] = struct{}{}
				}
			}
			for spaceID := range spaces {
				if err := tx.Exec(`DELETE FROM t_period_readiness WHERE c_space_id = ? AND NOT EXISTS (SELECT 1 FROM t_period_readiness_items WHERE c_readiness_id = t_period_readiness.c_id)`, spaceID).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
