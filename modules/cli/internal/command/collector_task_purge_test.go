package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCollectorTaskPurgeDefaultsToReadOnlyInventory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_task_instances (c_id INTEGER PRIMARY KEY, c_space_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_fetch_batches (c_id INTEGER PRIMARY KEY, c_space_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_fetch_retry_items (c_id INTEGER PRIMARY KEY, c_space_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_task_instances VALUES (1, 'crypto')`).Error)

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{DBPath: dbPath, SpaceID: "crypto"})
	require.NoError(t, err)
	require.True(t, summary.DryRun)
	require.Equal(t, "dry_run", summary.Status)
	require.EqualValues(t, 1, summary.TaskCount)
	require.EqualValues(t, 1, summary.TaskInstanceCount)
	require.Len(t, summary.Results, 1)
	require.Equal(t, "crypto", summary.Results[0].SpaceID)
	require.Equal(t, "task-1", summary.Results[0].TaskID)
	require.Equal(t, "view-1", summary.Results[0].ResultViewID)
	require.Equal(t, "dataset-1", summary.Results[0].ResultDatasetID)
	require.Equal(t, "unavailable", summary.StorageInventoryStatus)
	require.NotEmpty(t, summary.StorageInventoryReason)
	require.Zero(t, summary.StorageOwnedDatasetCount)
	require.Zero(t, summary.StorageOwnedViewCount)
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM t_collector_tasks`).Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCollectorTaskPurgeRejectsApplyAndExplicitDryRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath: dbPath, Apply: true, DryRunSet: true, Confirm: true,
		StopCommand: "false",
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "--apply and --dry-run")
	require.Equal(t, "dry_run", summary.Status)
	require.True(t, summary.DryRun)
	require.FileExists(t, dbPath)
}

func TestCollectorTaskPurgeApplyRequiresConfirmation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)

	orderPath := filepath.Join(t.TempDir(), "order.log")
	server := newCollectorTaskPurgeControlServer(t, orderPath)
	defer server.Close()
	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath: dbPath, ControlURL: server.URL, Apply: true,
		StopCommand: fmt.Sprintf("printf 'stop\\n' >> %q", orderPath),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "--apply and --confirm")
	require.Equal(t, "dry_run", summary.Status)
	require.FileExists(t, dbPath)
	require.Empty(t, readCollectorPurgeOrder(t, orderPath))
}

func TestCollectorTaskPurgeApplyBacksUpBeforeInitializing(t *testing.T) {
	useEmptyCollectorStorageInventory(t)
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)

	initScript := filepath.Join(t.TempDir(), "collector-init.sh")
	require.NoError(t, os.WriteFile(initScript, []byte("#!/bin/sh\n[ \"$1\" = init ] && [ \"$2\" = --db-path ] && [ -n \"$3\" ] || exit 2\nexit 0\n"), 0o700))
	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:           dbPath,
		MetadataTarget:   "test-storage",
		Apply:            true,
		Confirm:          true,
		StopCommand:      "true",
		CollectorInitBin: initScript,
	})
	require.NoError(t, err)
	require.Equal(t, "applied", summary.Status)
	require.False(t, summary.DryRun)
	require.NoFileExists(t, dbPath)
	require.DirExists(t, filepath.Dir(summary.BackupPath))
	require.FileExists(t, summary.BackupPath)
	require.Equal(t, []string{"collector_database_backed_up", "collector_writes_stopped", "collector_database_reset_for_init", "collector_schema_initialized"}, summary.CompletedStages)
}

func TestCollectorTaskPurgeApplyStopsBeforeDeletingTaskAndRuntime(t *testing.T) {
	useEmptyCollectorStorageInventory(t)
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_task_instances (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_fetch_batches (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_fetch_retry_items (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (2, 'crypto', 'task-keep', 'ETH', '', '')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (3, 'stockcn', 'other-space-task', '600000', 'view-other', 'dataset-other')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_task_instances VALUES (1, 'crypto', 'task-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_batches VALUES (1, 'crypto', 'task-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items VALUES (1, 'crypto', 'task-1')`).Error)

	orderPath := filepath.Join(t.TempDir(), "order.log")
	server := newCollectorTaskPurgeControlServer(t, orderPath)
	defer server.Close()
	stopCommand := fmt.Sprintf("printf 'stop\\n' >> %q", orderPath)

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:         dbPath,
		SpaceID:        "crypto",
		MetadataTarget: "test-storage",
		ControlURL:     server.URL,
		Apply:          true,
		Confirm:        true,
		StopCommand:    stopCommand,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"stop"}, readCollectorPurgeOrder(t, orderPath))
	require.Contains(t, summary.CompletedStages, "collector_writes_stopped")
	require.NotContains(t, summary.CompletedStages, "collector_task_results_deleted")
	require.Contains(t, summary.CompletedStages, "collector_runtime_deleted")
	require.Contains(t, summary.CompletedStages, "collector_space_purged")

	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM t_collector_tasks WHERE c_space_id = 'crypto'`).Scan(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Raw(`SELECT count(*) FROM t_collector_tasks WHERE c_space_id = 'stockcn'`).Scan(&count).Error)
	require.EqualValues(t, 1, count)
	for _, table := range []string{
		"t_collector_task_instances",
		"t_collector_fetch_batches",
		"t_collector_fetch_retry_items",
	} {
		require.NoError(t, db.Raw("SELECT count(*) FROM "+table).Scan(&count).Error)
		require.Zero(t, count, table)
	}
}

func TestCollectorTaskPurgeApplyUsesSpaceForEachTask(t *testing.T) {
	useEmptyCollectorStorageInventory(t)
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'same-task', 'BTC', 'view-crypto', 'dataset-crypto')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (2, 'stockcn', 'same-task', '600000', 'view-stockcn', 'dataset-stockcn')`).Error)

	orderPath := filepath.Join(t.TempDir(), "order.log")
	server := newCollectorTaskPurgeControlServer(t, orderPath)
	defer server.Close()
	initScript := filepath.Join(t.TempDir(), "collector-init.sh")
	initBody := fmt.Sprintf("#!/bin/sh\nprintf 'init\\n' >> %q\n[ \"$1\" = init ] && [ \"$2\" = --db-path ] && [ -n \"$3\" ]\n", orderPath)
	require.NoError(t, os.WriteFile(initScript, []byte(initBody), 0o700))

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:           dbPath,
		ControlURL:       server.URL,
		MetadataTarget:   "test-storage",
		Apply:            true,
		Confirm:          true,
		StopCommand:      fmt.Sprintf("printf 'stop\\n' >> %q", orderPath),
		CollectorInitBin: initScript,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"stop", "init"}, readCollectorPurgeOrder(t, orderPath))
	require.Equal(t, "crypto", summary.TaskIDs[0].SpaceID)
	require.Equal(t, "stockcn", summary.TaskIDs[1].SpaceID)
}

func TestCollectorTaskPurgeStopFailureDoesNotDelete(t *testing.T) {
	useEmptyCollectorStorageInventory(t)
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)

	orderPath := filepath.Join(t.TempDir(), "order.log")
	server := newCollectorTaskPurgeControlServer(t, orderPath)
	defer server.Close()
	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:         dbPath,
		SpaceID:        "crypto",
		ControlURL:     server.URL,
		MetadataTarget: "test-storage",
		Apply:          true,
		Confirm:        true,
		StopCommand:    "printf 'stop\\n' >> " + shellQuoteCollectorPurge(orderPath) + "; exit 7",
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "stop Collector")
	require.Equal(t, []string{"stop"}, readCollectorPurgeOrder(t, orderPath))
	require.Equal(t, []string{"collector_database_backed_up"}, summary.CompletedStages)
	require.FileExists(t, dbPath)
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM t_collector_tasks`).Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCollectorTaskPurgeMetadataDeleteFailureKeepsLocalRetryState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-ok', 'BTC', 'view-ok', 'dataset-ok')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (2, 'crypto', 'task-fail', 'ETH', 'view-fail', 'dataset-fail')`).Error)

	originalFactory := newCollectorStorageMetadataClient
	t.Cleanup(func() { newCollectorStorageMetadataClient = originalFactory })
	fake := &fakeCollectorStorageMetadataClient{
		datasets: []*storagepb.Dataset{
			{SpaceId: "crypto", DatasetId: "dataset-ok", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-ok"}},
			{SpaceId: "crypto", DatasetId: "dataset-fail", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-fail"}},
		},
		views: []*storagepb.View{
			{SpaceId: "crypto", ViewId: "view-ok", DatasetId: "dataset-ok", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-ok"}},
			{SpaceId: "crypto", ViewId: "view-fail", DatasetId: "dataset-fail", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-fail"}},
		},
		failViewID: "view-fail",
	}
	newCollectorStorageMetadataClient = func(string, string, string, string) collectorStorageMetadataClient {
		return fake
	}

	orderPath := filepath.Join(t.TempDir(), "order.log")
	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:         dbPath,
		SpaceID:        "crypto",
		MetadataTarget: "test-storage",
		Apply:          true,
		Confirm:        true,
		StopCommand:    fmt.Sprintf("printf 'stop\\n' >> %q", orderPath),
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "view-fail")
	require.Equal(t, "apply_failed", summary.Status)
	require.Equal(t, []string{"stop"}, readCollectorPurgeOrder(t, orderPath))
	require.Contains(t, summary.CompletedStages, "collector_database_backed_up")
	require.Contains(t, summary.CompletedStages, "collector_writes_stopped")
	require.Contains(t, summary.CompletedStages, "collector_runtime_deleted")
	require.NotContains(t, summary.CompletedStages, "collector_owned_views_deleted")

	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM t_collector_tasks`).Scan(&count).Error)
	require.Zero(t, count)
}

func TestCollectorTaskPurgeStorageInventoryCountsOnlyCollectorOwnedObjects(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)

	originalFactory := newCollectorStorageMetadataClient
	t.Cleanup(func() { newCollectorStorageMetadataClient = originalFactory })
	fake := &fakeCollectorStorageMetadataClient{
		datasets: []*storagepb.Dataset{
			{SpaceId: "crypto", DatasetId: "dataset-1", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-1"}},
			{SpaceId: "crypto", DatasetId: "dataset-orphan", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-orphan"}},
			{SpaceId: "crypto", DatasetId: "dataset-factor", Attributes: map[string]string{"owner_module": "factor", "collector_task_id": "task-1"}},
			{SpaceId: "crypto", DatasetId: "dataset-no-task", Attributes: map[string]string{"owner_module": "collector"}},
		},
		views: []*storagepb.View{
			{SpaceId: "crypto", ViewId: "view-1", DatasetId: "dataset-1", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-1"}},
			{SpaceId: "crypto", ViewId: "view-orphan", DatasetId: "dataset-orphan", Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-orphan"}},
			{SpaceId: "crypto", ViewId: "view-factor", DatasetId: "dataset-factor", Attributes: map[string]string{"owner_module": "factor", "collector_task_id": "task-1"}},
		},
		pageSize: 1,
	}
	newCollectorStorageMetadataClient = func(string, string, string, string) collectorStorageMetadataClient {
		return fake
	}

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:                dbPath,
		MetadataTarget:        "ip://storage:11003",
		MetadataServiceKey:    "storage-metadata",
		MetadataServiceSecret: "secret",
	})
	require.NoError(t, err)
	require.Equal(t, "available", summary.StorageInventoryStatus)
	require.Empty(t, summary.StorageInventoryReason)
	require.Equal(t, 2, summary.StorageOwnedDatasetCount)
	require.Equal(t, 1, summary.StorageOrphanDatasetCount)
	require.Equal(t, 2, summary.StorageOwnedViewCount)
	require.Equal(t, 1, summary.StorageOrphanViewCount)
	require.Equal(t, 4, fake.datasetCalls)
	require.Equal(t, 3, fake.viewCalls)
}

func newCollectorTaskPurgeControlServer(t *testing.T, orderPath string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			SpaceID          string `json:"space_id"`
			TaskID           string `json:"task_id"`
			DeleteResultData bool   `json:"delete_result_data"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		lines := append(readCollectorPurgeOrder(t, orderPath), fmt.Sprintf("delete:%s:%s:%t", request.SpaceID, request.TaskID, request.DeleteResultData))
		require.NoError(t, os.WriteFile(orderPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
		_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
	}))
}

func readCollectorPurgeOrder(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func shellQuoteCollectorPurge(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

type fakeCollectorStorageMetadataClient struct {
	datasets     []*storagepb.Dataset
	views        []*storagepb.View
	pageSize     int
	datasetCalls int
	viewCalls    int
	failViewID   string
}

func (f *fakeCollectorStorageMetadataClient) ListDatasets(_ context.Context, req *storagepb.ListDatasetsReq) (*storagepb.ListDatasetsRsp, error) {
	f.datasetCalls++
	page := int(req.GetPage().GetPage())
	if page < 1 {
		page = 1
	}
	return &storagepb.ListDatasetsRsp{
		RetInfo:    &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Datasets:   paginateCollectorPurgeItems(f.datasets, page, f.pageSize),
		PageResult: collectorPurgePageResult(len(f.datasets), page, f.pageSize),
	}, nil
}

func (f *fakeCollectorStorageMetadataClient) ListViews(_ context.Context, req *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error) {
	f.viewCalls++
	page := int(req.GetPage().GetPage())
	if page < 1 {
		page = 1
	}
	return &storagepb.ListViewsRsp{
		RetInfo:    storageOKForCollectorPurge(),
		Views:      paginateCollectorPurgeItems(f.views, page, f.pageSize),
		PageResult: collectorPurgePageResult(len(f.views), page, f.pageSize),
	}, nil
}

func (f *fakeCollectorStorageMetadataClient) DeleteDataset(_ context.Context, _ *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	return &storagepb.DeleteDatasetRsp{RetInfo: storageOKForCollectorPurge()}, nil
}

func (f *fakeCollectorStorageMetadataClient) DeleteView(_ context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	if f.failViewID != "" && req.GetViewId() == f.failViewID {
		return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "view delete failed"}}, nil
	}
	return &storagepb.DeleteViewRsp{RetInfo: storageOKForCollectorPurge()}, nil
}

func (f *fakeCollectorStorageMetadataClient) DeleteDatasetRows(_ context.Context, _ *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	return &storagepb.PrimaryDeleteDatasetRowsRsp{RetInfo: storageOKForCollectorPurge()}, nil
}

func storageOKForCollectorPurge() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
}

func useEmptyCollectorStorageInventory(t *testing.T) {
	t.Helper()
	original := newCollectorStorageMetadataClient
	newCollectorStorageMetadataClient = func(string, string, string, string) collectorStorageMetadataClient {
		return &fakeCollectorStorageMetadataClient{}
	}
	t.Cleanup(func() {
		newCollectorStorageMetadataClient = original
	})
}

func paginateCollectorPurgeItems[T any](items []T, page, pageSize int) []T {
	if pageSize < 1 {
		pageSize = 1
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return nil
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func collectorPurgePageResult(total, page, pageSize int) *commonpb.PageResult {
	if pageSize < 1 {
		pageSize = 1
	}
	return &commonpb.PageResult{
		Page:    uint32(page),
		Size:    uint32(pageSize),
		Total:   uint32(total),
		HasMore: page*pageSize < total,
	}
}
