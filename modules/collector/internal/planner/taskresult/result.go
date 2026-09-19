package taskresult

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// IDs are the stable, Collector-owned metadata identities for one task result.
// A task never reuses another task's dataset or view, even when task names match.
type IDs struct {
	DatasetID string
	ViewID    string
}

func resultIDs(spaceID, taskID string) IDs {
	return resultIDsWithSuffix(spaceID, taskID, "")
}

func resultIDsWithSuffix(spaceID, taskID, suffix string) IDs {
	digest := sha256.Sum256([]byte(spaceID + "\x00" + taskID))
	hashSuffix := hex.EncodeToString(digest[:])[:16]
	datasetID := "dataset_collector_" + hashSuffix
	if suffix = strings.ToLower(strings.TrimSpace(suffix)); suffix != "" {
		datasetID += "_" + suffix
	}
	return IDs{
		DatasetID: datasetID,
		ViewID: "view_collector_" + hashSuffix + func() string {
			if suffix == "" {
				return ""
			}
			return "_" + suffix
		}(),
	}
}

// ResultIDs returns the stable metadata identities for a task result.
func ResultIDs(spaceID, taskID string) IDs { return resultIDs(spaceID, taskID) }

// ResultIDsWithFrequency returns stable task-exclusive identities for a
// frequency-specific derived result. The suffix is expected to be the
// canonical lower-case frequency slug (for example, 5m or 4h).
func ResultIDsWithFrequency(spaceID, taskID, frequencySlug string) IDs {
	return resultIDsWithSuffix(spaceID, taskID, frequencySlug)
}

type metadataAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	GetView(context.Context, *storagepb.GetViewReq) (*storagepb.GetViewRsp, error)
	CreateView(context.Context, *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

type metadataProxy struct{ client storagepb.MetadataClientProxy }

func (p metadataProxy) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return p.client.GetDataset(ctx, req)
}
func (p metadataProxy) CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	return p.client.CreateDataset(ctx, req)
}
func (p metadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	return p.client.DeleteDataset(ctx, req)
}
func (p metadataProxy) GetView(ctx context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	return p.client.GetView(ctx, req)
}
func (p metadataProxy) CreateView(ctx context.Context, req *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	return p.client.CreateView(ctx, req)
}
func (p metadataProxy) DeleteView(ctx context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	return p.client.DeleteView(ctx, req)
}
func (p metadataProxy) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return p.client.UpsertDatasetColumn(ctx, req)
}
func (p metadataProxy) UpsertViewColumn(ctx context.Context, req *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return p.client.UpsertViewColumn(ctx, req)
}
func (p metadataProxy) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return p.client.CheckDatasetActivation(ctx, req)
}
func (p metadataProxy) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return p.client.ActivateDataset(ctx, req)
}

type Manager struct {
	metadata metadataAPI
	cleaner  datasetRowsCleaner
	auth     *storagepb.AuthInfo
}

type datasetRowsCleaner interface {
	DeleteDatasetRows(context.Context, *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error)
}

type primaryDatasetRowsCleaner struct {
	client storagepb.PrimaryStoreClientProxy
}

func (c primaryDatasetRowsCleaner) DeleteDatasetRows(ctx context.Context, req *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	return c.client.DeleteDatasetRows(ctx, req)
}

func NewManager(client storagepb.MetadataClientProxy, auth *storagepb.AuthInfo) *Manager {
	if client == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadataProxy{client: client}, auth: auth}
}

func NewManagerWithCleaner(client storagepb.MetadataClientProxy, cleaner storagepb.PrimaryStoreClientProxy, auth *storagepb.AuthInfo) *Manager {
	if client == nil || cleaner == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadataProxy{client: client}, cleaner: primaryDatasetRowsCleaner{client: cleaner}, auth: auth}
}

func NewManagerWithAPI(metadata metadataAPI, auth *storagepb.AuthInfo) *Manager {
	if metadata == nil || auth == nil {
		return nil
	}
	return &Manager{metadata: metadata, auth: auth}
}

type Config struct {
	DataNodeID   string
	KeepDuration string
	Description  string
	DataSourceID string
	Frequency    string
}

func (m *Manager) Ensure(ctx context.Context, spaceID, taskID, dataType, marketType string, cfg Config) (IDs, error) {
	if m == nil || m.metadata == nil || m.auth == nil {
		return IDs{}, fmt.Errorf("task result metadata manager is not configured")
	}
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	if spaceID == "" || taskID == "" || strings.TrimSpace(cfg.DataNodeID) == "" {
		return IDs{}, fmt.Errorf("space_id, task_id and result data_node_id are required")
	}
	ids := resultIDs(spaceID, taskID)
	kind := storagepb.DataKind_DATA_KIND_TIME_SERIES
	if strings.EqualFold(strings.TrimSpace(dataType), "instrument") || strings.EqualFold(strings.TrimSpace(dataType), "symbol") {
		kind = storagepb.DataKind_DATA_KIND_RECORD
	}
	keep := strings.TrimSpace(cfg.KeepDuration)
	if keep == "" {
		keep = "0"
	}
	attrs := map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": taskID, "market_type": strings.TrimSpace(marketType)}
	get, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return IDs{}, fmt.Errorf("get result dataset: %w", err)
	}
	createdDataset := false
	if get == nil {
		return IDs{}, metadataError("get result dataset", nil, nil)
	}
	if get.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || get.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := m.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: m.auth, Dataset: &storagepb.Dataset{
			SpaceId: spaceID, DatasetId: ids.DatasetID, DataSourceId: resultDataSourceID(cfg.DataSourceID), DataNodeId: cfg.DataNodeID,
			Name: resultName(spaceID, taskID), Description: cfg.Description, DataKind: kind, Status: "draft", KeepDuration: keep, Freqs: nonEmptyFrequency(kind, cfg.Frequency), Attributes: attrs,
		}})
		if createErr != nil || created == nil || created.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return IDs{}, metadataError("create result dataset", createErr, retInfoDataset(created))
		}
		createdDataset = true
	} else if get.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return IDs{}, metadataError("get result dataset", nil, get.GetRetInfo())
	} else if !ownedByTask(get.GetDataset().GetAttributes(), taskID) {
		return IDs{}, fmt.Errorf("result dataset %s is owned by another task", ids.DatasetID)
	}
	if err := m.ensureColumns(ctx, spaceID, ids.DatasetID, ids.ViewID, kind); err != nil {
		if createdDataset {
			_, _ = m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
		}
		return IDs{}, err
	}
	check, err := m.metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return IDs{}, fmt.Errorf("check result dataset activation: %w", err)
	}
	if check.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || !check.GetReady() {
		return IDs{}, metadataError("check result dataset activation", nil, check.GetRetInfo())
	}
	get, err = m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return IDs{}, err
	}
	if get.GetDataset() == nil {
		return IDs{}, metadataError("get result dataset", nil, nil)
	}
	if get.GetDataset().GetStatus() != "active" {
		activated, activateErr := m.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID, ExpectedRevision: get.GetDataset().GetRevision()})
		if activateErr != nil || activated == nil || activated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return IDs{}, metadataError("activate result dataset", activateErr, retInfoActivate(activated))
		}
	}
	if err := m.ensureView(ctx, spaceID, taskID, ids, kind, keep, cfg.Frequency); err != nil {
		if createdDataset {
			_, _ = m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
		}
		return IDs{}, err
	}
	return ids, nil
}

// EnsureWithCleanup provisions a raw result and returns a compensating action
// for callers that have not yet committed the owning task row. Existing
// task-owned resources are never removed by that action.
func (m *Manager) EnsureWithCleanup(ctx context.Context, spaceID, taskID, dataType, marketType string, cfg Config) (IDs, func(context.Context) error, error) {
	ids := resultIDs(spaceID, taskID)
	datasetExists, viewExists, err := m.resultMetadataState(ctx, spaceID, ids)
	if err != nil {
		return IDs{}, nil, err
	}
	ensured, err := m.Ensure(ctx, spaceID, taskID, dataType, marketType, cfg)
	if err != nil {
		return IDs{}, nil, err
	}
	cleanup := func(cleanupCtx context.Context) error {
		if viewExists && datasetExists {
			return nil
		}
		if !viewExists {
			view, deleteErr := m.metadata.DeleteView(cleanupCtx, &storagepb.DeleteViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ensured.ViewID})
			if deleteErr != nil || (view != nil && view.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && view.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && view.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
				return metadataError("cleanup result view", deleteErr, retInfoViewDelete(view))
			}
		}
		if !datasetExists {
			if m.cleaner != nil {
				physical, cleanErr := m.cleaner.DeleteDatasetRows(cleanupCtx, &storagepb.PrimaryDeleteDatasetRowsReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ensured.DatasetID})
				if cleanErr != nil || physical == nil || !resultDeleteAccepted(physical.GetRetInfo()) {
					return metadataError("cleanup result dataset rows", cleanErr, func() *storagepb.RetInfo {
						if physical == nil {
							return nil
						}
						return physical.GetRetInfo()
					}())
				}
			}
			dataset, deleteErr := m.metadata.DeleteDataset(cleanupCtx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ensured.DatasetID})
			if deleteErr != nil || (dataset != nil && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
				return metadataError("cleanup result dataset", deleteErr, retInfoDatasetDelete(dataset))
			}
		}
		return nil
	}
	return ensured, cleanup, nil
}

func (m *Manager) resultMetadataState(ctx context.Context, spaceID string, ids IDs) (bool, bool, error) {
	dataset, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return false, false, err
	}
	datasetExists := dataset != nil && dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS
	if dataset != nil && !datasetExists && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
		return false, false, metadataError("get result dataset", nil, dataset.GetRetInfo())
	}
	view, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil {
		return false, false, err
	}
	viewExists := view != nil && view.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS
	if view != nil && !viewExists && view.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && view.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND {
		return false, false, metadataError("get result view", nil, view.GetRetInfo())
	}
	return datasetExists, viewExists, nil
}

func (m *Manager) ensureColumns(ctx context.Context, spaceID, datasetID, viewID string, kind storagepb.DataKind) error {
	fields := []struct {
		name string
		kind storagepb.FieldValueType
	}{
		{"open", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"high", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"low", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"close", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"quote_volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"trade_num", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT},
	}
	if kind == storagepb.DataKind_DATA_KIND_RECORD {
		fields = nil
	} else {
		fields = append(fields,
			struct {
				name string
				kind storagepb.FieldValueType
			}{"amount", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"instrument_name", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"provider_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"source_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"provider_symbol", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"volume_unit", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"amount_unit", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"trade_date", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"close_time", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"provider_timestamp", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"fetched_at", storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"request_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"route_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"route_rank", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"source_provider", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"quality_status", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
			struct {
				name string
				kind storagepb.FieldValueType
			}{"amount_quality", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		)
	}
	for _, field := range fields {
		resp, err := m.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: m.auth, Column: &storagepb.DatasetColumn{SpaceId: spaceID, DatasetId: datasetID, ColumnName: field.name, OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: field.name, ValueType: field.kind, Required: true, Status: "active", Attributes: map[string]string{"display_name": resultColumnDisplayName(field.name)}}})
		if err != nil || resp == nil || resp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result dataset column", err, retInfoDatasetColumn(resp))
		}
	}
	return nil
}

func resultColumnDisplayName(name string) string {
	names := map[string]string{
		"open": "开盘", "high": "最高", "low": "最低", "close": "收盘", "volume": "成交量", "quote_volume": "成交额", "trade_num": "成交笔数", "amount": "成交额", "instrument_name": "标的名称", "provider_id": "数据提供方", "source_id": "来源", "provider_symbol": "原始标的", "volume_unit": "量单位", "amount_unit": "额单位", "trade_date": "交易日期", "close_time": "收盘时间", "provider_timestamp": "源时间", "fetched_at": "采集时间", "request_id": "请求标识", "route_id": "路由", "route_rank": "路由序号", "source_provider": "源提供方", "quality_status": "质量状态", "amount_quality": "金额质量",
	}
	if displayName := names[name]; displayName != "" {
		return displayName
	}
	return "结果字段"
}

func (m *Manager) ensureView(ctx context.Context, spaceID, taskID string, ids IDs, kind storagepb.DataKind, keep, frequency string) error {
	get, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil {
		return fmt.Errorf("get result view: %w", err)
	}
	if get == nil {
		return metadataError("get result view", nil, nil)
	}
	if get.GetRetInfo().GetCode() == storagepb.ErrorCode_VIEW_NOT_FOUND || get.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		engine := "duckdb"
		grain := []string{"subject_id", "data_time", "freq"}
		if kind == storagepb.DataKind_DATA_KIND_RECORD {
			engine, grain = "bleve", []string{"subject_id", "version"}
		}
		filterJSON := ""
		if kind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
			encoded, _ := json.Marshal(map[string]string{"freq": strings.TrimSpace(frequency)})
			filterJSON = string(encoded)
		}
		created, createErr := m.metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: m.auth, View: &storagepb.View{SpaceId: spaceID, ViewId: ids.ViewID, Name: resultName(spaceID, taskID), Description: "Collector任务结果视图", DatasetId: ids.DatasetID, GrainKeys: grain, Engine: engine, FilterJson: filterJSON, KeepDuration: keep, Status: "active", Attributes: map[string]string{"owner_module": "collector", "view_role": "collection_browse", "collector_task_id": taskID}}})
		if createErr != nil || created == nil || created.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return metadataError("create result view", createErr, retInfoView(created))
		}
		return nil
	}
	if get.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return metadataError("get result view", nil, get.GetRetInfo())
	}
	if !ownedByTask(get.GetView().GetAttributes(), taskID) {
		return fmt.Errorf("result view %s is owned by another task", ids.ViewID)
	}
	return nil
}

func resultDataSourceID(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "stockcn_multi") {
		return "stockcn"
	}
	if strings.TrimSpace(provider) == "" {
		return "collector"
	}
	return strings.TrimSpace(provider)
}

func resultName(spaceID, taskID string) string {
	ids := resultIDs(spaceID, strings.TrimSpace(taskID))
	return "结果-" + strings.TrimPrefix(ids.DatasetID, "dataset_collector_")[:6]
}

func ownedByTask(attributes map[string]string, taskID string) bool {
	if attributes["owner_module"] != "collector" {
		return false
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	// Older resample results used resample_task_id before all Collector
	// results were normalized to collector_task_id. Keep that result lineage
	// verifiable so bootstrap migration and explicit deletion remain safe.
	return attributes["collector_task_id"] == trimmedTaskID || attributes["resample_task_id"] == trimmedTaskID
}

func (m *Manager) Delete(ctx context.Context, spaceID string, ids IDs) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if m.cleaner != nil {
		physical, err := m.cleaner.DeleteDatasetRows(ctx, &storagepb.PrimaryDeleteDatasetRowsReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
		if err != nil || physical == nil || !resultDeleteAccepted(physical.GetRetInfo()) {
			return metadataError("delete result dataset rows", err, func() *storagepb.RetInfo {
				if physical == nil {
					return nil
				}
				return physical.GetRetInfo()
			}())
		}
	}
	view, err := m.metadata.DeleteView(ctx, &storagepb.DeleteViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil || (view != nil && view.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && view.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && view.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
		return metadataError("delete result view", err, retInfoViewDelete(view))
	}
	dataset, err := m.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil || (dataset != nil && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
		return metadataError("delete result dataset", err, retInfoDatasetDelete(dataset))
	}
	return nil
}

func resultDeleteAccepted(info *storagepb.RetInfo) bool {
	if info == nil {
		return false
	}
	switch info.GetCode() {
	case storagepb.ErrorCode_SUCCESS, storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

// DeleteForTask verifies Collector ownership before deleting metadata. The
// deterministic Dataset ID prevents a malformed task row from targeting an
// unrelated result, while the ownership check protects the View side too.
func (m *Manager) DeleteForTask(ctx context.Context, spaceID, taskID string, ids IDs) error {
	if err := m.ValidateOwnedForTask(ctx, spaceID, taskID, ids); err != nil {
		return err
	}
	return m.Delete(ctx, spaceID, ids)
}

// ValidateOwnedForTask performs the destructive-delete ownership checks
// without changing Storage state. Callers can use it before deleting local
// runtime records so a rejected delete remains fully retryable.
func (m *Manager) ValidateOwnedForTask(ctx context.Context, spaceID, taskID string, ids IDs) error {
	if m == nil || m.metadata == nil || m.auth == nil {
		return fmt.Errorf("task result metadata manager is not configured")
	}
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	base := resultIDs(spaceID, taskID).DatasetID
	deterministicID := ids.DatasetID == base || strings.HasPrefix(ids.DatasetID, base+"_")
	expectedViewID := "view_" + strings.TrimPrefix(ids.DatasetID, "dataset_")
	if ids.ViewID != expectedViewID {
		return fmt.Errorf("result view %s is not owned by task %s", ids.ViewID, taskID)
	}
	dataset, err := m.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: m.auth, SpaceId: spaceID, DatasetId: ids.DatasetID})
	if err != nil {
		return fmt.Errorf("get result dataset ownership: %w", err)
	}
	if dataset != nil && dataset.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
		if dataset.GetDataset() == nil || !ownedByTask(dataset.GetDataset().GetAttributes(), taskID) {
			return fmt.Errorf("result dataset %s is not owned by task %s", ids.DatasetID, taskID)
		}
	} else if !deterministicID {
		// Legacy resample tasks could persist a user-selected result ID. Such
		// results remain deletable only when the persisted ownership marker is
		// present; a missing object must not turn an arbitrary ID into a delete
		// target.
		return fmt.Errorf("legacy result dataset %s is not verifiably owned by task %s", ids.DatasetID, taskID)
	}
	view, err := m.metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: m.auth, SpaceId: spaceID, ViewId: ids.ViewID})
	if err != nil {
		return fmt.Errorf("get result view ownership: %w", err)
	}
	if view != nil && view.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
		if view.GetView() == nil || !ownedByTask(view.GetView().GetAttributes(), taskID) {
			return fmt.Errorf("result view %s is not owned by task %s", ids.ViewID, taskID)
		}
	} else if !deterministicID {
		return fmt.Errorf("legacy result view %s is not verifiably owned by task %s", ids.ViewID, taskID)
	}
	return nil
}

func nonEmptyFrequency(kind storagepb.DataKind, frequency string) []string {
	if kind == storagepb.DataKind_DATA_KIND_TIME_SERIES && strings.TrimSpace(frequency) != "" {
		return []string{strings.TrimSpace(frequency)}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metadataError(action string, callErr error, ret *storagepb.RetInfo) error {
	if callErr != nil {
		return fmt.Errorf("%s: %w", action, callErr)
	}
	if ret == nil {
		return fmt.Errorf("%s: empty response", action)
	}
	return fmt.Errorf("%s: %s", action, ret.GetMsg())
}

func retInfoDataset(resp *storagepb.CreateDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoActivate(resp *storagepb.ActivateDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoDatasetColumn(resp *storagepb.UpsertDatasetColumnRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoView(resp *storagepb.CreateViewRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoViewDelete(resp *storagepb.DeleteViewRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoDatasetDelete(resp *storagepb.DeleteDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}
