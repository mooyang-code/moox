package resample

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"trpc.group/trpc-go/trpc-go/client"
)

var ErrTargetViewNotReady = errors.New("target View is not ready")

type metadataAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq, ...client.Option) (*storagepb.CreateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error)
	CreateView(context.Context, *storagepb.CreateViewReq, ...client.Option) (*storagepb.CreateViewRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq, ...client.Option) (*storagepb.DeleteViewRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
}

// Catalog is the narrow Metadata API needed to provision a task-owned target
// dataset and its query View.
type Catalog struct {
	Metadata metadataAPI
	Auth     *storagepb.AuthInfo
	// View 用来等目标 View 的列建好。
	View ViewWaiter
}

type klineField struct {
	name     string
	type_    storagepb.FieldValueType
	label    string
	required bool
}

var priceFields = []klineField{
	{"open", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "开盘价", true},
	{"high", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "最高价", true},
	{"low", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "最低价", true},
	{"close", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "收盘价", true},
	{"volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "成交量", true},
}

// fieldsFor 返回市场的合成结果字段：加密货币带报价成交量和成交笔数；A 股只有成交额，且可以为空。
func fieldsFor(tradeCount bool) []klineField {
	if tradeCount {
		return append(append([]klineField(nil), priceFields...),
			klineField{"quote_volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "报价成交量", true},
			klineField{"trade_num", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, "成交笔数", true})
	}
	return append(append([]klineField(nil), priceFields...),
		klineField{"amount", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "成交额", false})
}

// PrepareTarget creates or validates the target Dataset, columns, subject
// bindings and View. Existing resources with a mismatched immutable contract
// are rejected instead of being silently overwritten.
func (c *Catalog) PrepareTarget(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, source storagesource.DatasetInfo, subjects []domain.Subject) error {
	if c == nil || c.Metadata == nil || c.Auth == nil {
		return errors.New("resample catalog dependencies are required")
	}
	if params == nil {
		return errors.New("resample params are required")
	}
	targetFreq, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return err
	}
	tradeCount, err := hasTradeCount(task.SpaceID)
	if err != nil {
		return err
	}
	market, err := barclock.MarketFor(task.SpaceID)
	if err != nil {
		return err
	}
	ids := taskresult.PersistedResultIDs(task.SpaceID, task.TaskID, task.ResultDatasetID, task.ResultViewID)
	params.TargetDatasetID = ids.DatasetID
	targetDatasetID, targetViewID := ids.DatasetID, ids.ViewID
	attrs := map[string]string{
		"owner_module": "collector", "managed_by": "collector", "collector_task_id": task.TaskID, "market_type": strings.ToLower(params.MarketType),
		"storage_model": "wide_common_metrics", "dataset_role": "kline_resample_result",
		"source_dataset_id": params.SourceDatasetID, "source_data_source_id": source.DataSourceID,
		"source_freq": params.SourceFrequency, "source_series_tag": strings.Join(params.SourceTags(), ","),
		"target_freq": targetFreq.Storage,
	}
	createdDataset, createdView := false, false
	compensate := func(original error) error {
		if original == nil {
			return nil
		}
		// View 还没追上只是需要等：资源的 ID 是确定的，保留它们，下一轮准备时继续等，不能每轮删了重建。
		if errors.Is(original, ErrTargetViewNotReady) {
			return original
		}
		if cleanupErr := c.cleanupCreatedTarget(ctx, task.SpaceID, targetDatasetID, targetViewID, createdView, createdDataset); cleanupErr != nil {
			return errors.Join(original, cleanupErr)
		}
		return original
	}
	target, getErr := c.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, DatasetId: targetDatasetID})
	if getErr != nil {
		return fmt.Errorf("get target Dataset: %w", getErr)
	}
	if target.GetRetInfo() == nil {
		return errors.New("get target Dataset: empty ret_info")
	}
	columns := targetDatasetColumns(tradeCount, params.OutputFields)
	if target.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || target.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		// 数据集连同列在一次调用里建好，建好即可写入。
		created, createErr := c.Metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: c.Auth, Dataset: &storagepb.Dataset{
			SpaceId: task.SpaceID, DatasetId: targetDatasetID,
			// 数据集名称在空间内唯一且必须含中文：用目标 ID 派生一个稳定的短后缀，避免不同合成目标重名。
			Name: uniqueResampleDisplayName(targetDatasetID), Description: "Collector生成的K线重采样结果", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
			// 周期的迟到容忍（收盘后 2 分钟）：Storage 据此算周期截止，写入目标与引擎的截止判断一致。
			Freq: targetFreq.Storage, Alignment: market.Alignment, AllowedLatenessSeconds: TargetLatenessSeconds,
			Attributes: attrs, SubjectTags: append([]string(nil), source.SubjectTags...),
		}, Columns: columns})
		if createErr != nil {
			return fmt.Errorf("create target Dataset: %w", createErr)
		}
		if err := ensureMetadataSuccess("create target Dataset", created.GetRetInfo()); err != nil {
			return err
		}
		createdDataset = true
		target.Dataset = created.GetDataset()
	} else if err := ensureMetadataSuccess("get target Dataset", target.GetRetInfo()); err != nil {
		return err
	}
	if target.GetDataset() == nil {
		return compensate(errors.New("target Dataset is empty"))
	}
	if err := validateTargetDataset(target.GetDataset(), attrs, targetFreq.Storage); err != nil {
		return compensate(err)
	}
	if target.GetDataset().GetAlignment() != market.Alignment {
		return compensate(fmt.Errorf("目标数据集的 alignment %q 与市场的 %q 不一致", target.GetDataset().GetAlignment(), market.Alignment))
	}
	_ = subjects
	if !createdDataset {
		// 已有的目标数据集按同一份列定义补齐：上一轮建数据集时缓存没发布成功，这里也会让 Storage 重新发布。
		for _, column := range columns {
			column.SpaceId, column.DatasetId = task.SpaceID, targetDatasetID
			resp, callErr := c.Metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: c.Auth, Column: column})
			if callErr != nil {
				return fmt.Errorf("upsert target column %s: %w", column.GetColumnName(), callErr)
			}
			if err := ensureMetadataSuccess("upsert target column", resp.GetRetInfo()); err != nil {
				return err
			}
		}
	}
	viewResp, viewErr := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, ViewId: targetViewID})
	if viewErr != nil {
		return compensate(fmt.Errorf("get target View: %w", viewErr))
	}
	if viewResp.GetRetInfo() == nil {
		return compensate(errors.New("get target View: empty ret_info"))
	}
	if viewResp.GetRetInfo().GetCode() == storagepb.ErrorCode_VIEW_NOT_FOUND || viewResp.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := c.Metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: c.Auth, CreateOnly: true, View: &storagepb.View{
			SpaceId: task.SpaceID, ViewId: targetViewID, Name: uniqueResampleDisplayName(targetDatasetID), Description: "Collector生成的K线重采样查询视图", DatasetId: targetDatasetID,
			GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Engine: "duckdb", Status: "active",
			Attributes: map[string]string{"owner_module": "collector", "managed_by": "collector", "collector_task_id": task.TaskID, "view_role": "collection_browse"},
		}})
		if createErr != nil {
			return compensate(fmt.Errorf("create target View: %w", createErr))
		}
		if err := ensureMetadataSuccess("create target View", created.GetRetInfo()); err != nil {
			return compensate(err)
		}
		createdView = true
		viewResp.View = created.GetView()
	} else if err := ensureMetadataSuccess("get target View", viewResp.GetRetInfo()); err != nil {
		return compensate(err)
	} else if err := validateTargetView(viewResp.GetView(), task, params, targetFreq.Storage); err != nil {
		return compensate(err)
	}
	// View 的列由 Storage 按目标数据集启用的列推出；重新读一次 View，按它现在的列（即目标数据集的列）等列建进索引。
	finalViewResp, finalViewErr := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, ViewId: targetViewID})
	if finalViewErr != nil {
		return compensate(fmt.Errorf("reread target View: %w", finalViewErr))
	}
	if err := ensureMetadataSuccess("reread target View", finalViewResp.GetRetInfo()); err != nil {
		return compensate(err)
	}
	finalView := finalViewResp.GetView()
	if finalView == nil {
		return compensate(errors.New("reread target View is empty"))
	}
	if err := waitTargetViewColumns(ctx, c.View, c.Auth, task.SpaceID, targetViewID, finalView.GetColumns(), 5*time.Second); err != nil {
		return compensate(err)
	}
	return nil
}

// targetDatasetColumns 返回目标数据集的列定义：市场的合成字段，指定了输出字段时只取选中的那些。
func targetDatasetColumns(tradeCount bool, outputFields []string) []*storagepb.DatasetColumn {
	var columns []*storagepb.DatasetColumn
	for _, field := range fieldsFor(tradeCount) {
		if len(outputFields) > 0 && !containsOutputField(outputFields, field.name) {
			continue
		}
		columns = append(columns, &storagepb.DatasetColumn{
			ColumnName: field.name, OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: field.name,
			ValueType: field.type_, Required: field.required, Status: "active", Attributes: map[string]string{"display_name": field.label},
		})
	}
	return columns
}

func containsOutputField(fields []string, name string) bool {
	for _, field := range fields {
		if strings.EqualFold(strings.TrimSpace(field), name) {
			return true
		}
	}
	return false
}

func (c *Catalog) cleanupCreatedTarget(ctx context.Context, spaceID, datasetID, viewID string, deleteView, deleteDataset bool) error {
	if c == nil || c.Metadata == nil {
		return errors.New("resample catalog metadata is not configured")
	}
	var cleanupErrors []error
	if deleteView {
		view, err := c.Metadata.DeleteView(ctx, &storagepb.DeleteViewReq{AuthInfo: c.Auth, SpaceId: spaceID, ViewId: viewID})
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete newly-created target View: %w", err))
		} else if view == nil || (view.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && view.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && view.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
			cleanupErrors = append(cleanupErrors, catalogMetadataError("delete newly-created target View", nil, retInfoViewDelete(view)))
		}
	}
	if deleteDataset {
		dataset, err := c.Metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: c.Auth, SpaceId: spaceID, DatasetId: datasetID})
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete newly-created target Dataset: %w", err))
		} else if dataset == nil || (dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
			cleanupErrors = append(cleanupErrors, catalogMetadataError("delete newly-created target Dataset", nil, retInfoDatasetDelete(dataset)))
		}
	}
	return errors.Join(cleanupErrors...)
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

func catalogMetadataError(action string, callErr error, ret *storagepb.RetInfo) error {
	if callErr != nil {
		return fmt.Errorf("%s: %w", action, callErr)
	}
	if ret == nil {
		return fmt.Errorf("%s: empty response", action)
	}
	return fmt.Errorf("%s: %s", action, ret.GetMsg())
}

func uniqueResampleDisplayName(targetDatasetID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(targetDatasetID)))
	// Storage display names must contain Chinese and be at most ten runes.
	// Keep one Chinese marker plus nine base32 characters (45 bits) so the
	// deterministic names remain readable while making collisions extremely
	// unlikely across arbitrary target dataset IDs.
	suffix := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "重" + strings.ToLower(suffix[:9])
}

// waitTargetViewColumns 等到 View 的活动索引建好、声明的列都已建进索引才放行采集。View 还没开始建索引
// （VIEW_NOT_FOUND）或索引还在建（index_id 为空）都继续等，超时返回 ErrTargetViewNotReady，下一轮准备时再等。
func waitTargetViewColumns(ctx context.Context, view ViewWaiter, auth *storagepb.AuthInfo, spaceID, viewID string, columns []*storagepb.ViewColumn, timeout time.Duration) error {
	if view == nil {
		return errors.New("合成需要 DataView 客户端")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		response, err := view.GetViewState(waitCtx, &storagepb.GetViewStateReq{AuthInfo: auth, SpaceId: spaceID, ViewId: viewID})
		if err != nil {
			if waitCtx.Err() != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrTargetViewNotReady
			}
			return fmt.Errorf("get target View state: %w", err)
		}
		if response.GetRetInfo() == nil {
			return errors.New("get target View state: empty ret_info")
		}
		switch response.GetRetInfo().GetCode() {
		case storagepb.ErrorCode_SUCCESS:
			if targetViewColumnsReady(response, columns) {
				return nil
			}
		case storagepb.ErrorCode_VIEW_NOT_FOUND:
		default:
			return fmt.Errorf("get target View state: %s", response.GetRetInfo().GetMsg())
		}
		if err := sleepContext(waitCtx, viewRetryInterval); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrTargetViewNotReady
		}
	}
}

func targetViewColumnsReady(state *storagepb.GetViewStateRsp, columns []*storagepb.ViewColumn) bool {
	if strings.TrimSpace(state.GetIndexId()) == "" {
		return false
	}
	built := make(map[string]struct{}, len(state.GetColumns()))
	for _, column := range state.GetColumns() {
		built[column.GetColumnName()] = struct{}{}
	}
	for _, column := range columns {
		if _, ok := built[column.GetColumnName()]; !ok {
			return false
		}
	}
	return true
}

func validateTargetView(view *storagepb.View, task domain.CollectionTask, params *domain.CollectParams, frequency string) error {
	if view == nil {
		return errors.New("target View is empty")
	}
	if view.GetDatasetId() != params.TargetDatasetID {
		return errors.New("target View immutable Dataset contract does not match task")
	}
	if view.GetFreq() != frequency || view.GetEngine() != "duckdb" {
		return errors.New("target View immutable frequency contract does not match task")
	}
	wantGrain := []string{"subject_id", "freq", "data_time", "series_tag"}
	if len(view.GetGrainKeys()) != len(wantGrain) {
		return errors.New("target View grain contract does not match task")
	}
	for i := range wantGrain {
		if view.GetGrainKeys()[i] != wantGrain[i] {
			return errors.New("target View grain contract does not match task")
		}
	}
	if strings.TrimSpace(task.TaskID) != "" {
		attributes := view.GetAttributes()
		if attributes["owner_module"] != "collector" || attributes["collector_task_id"] != task.TaskID {
			return errors.New("target View owner contract does not match task")
		}
	}
	return nil
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input)+1)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func validateTargetDataset(dataset *storagepb.Dataset, want map[string]string, frequency string) error {
	if dataset.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return errors.New("target Dataset must be time_series")
	}
	if strings.TrimSpace(dataset.GetDataSourceId()) != "" {
		return fmt.Errorf("collector-owned target Dataset must not declare a single data source: got %q", dataset.GetDataSourceId())
	}
	for key, expected := range want {
		if dataset.GetAttributes()[key] != expected {
			return fmt.Errorf("target Dataset immutable lineage attribute %s does not match task", key)
		}
	}
	wantedFrequency, err := frequencypkg.Normalize(frequency)
	if err != nil {
		return fmt.Errorf("target Dataset frequency %q is invalid: %w", frequency, err)
	}
	if dataset.GetFreq() != wantedFrequency {
		return fmt.Errorf("target Dataset freq is %q, not %s", dataset.GetFreq(), frequency)
	}
	return nil
}

func ensureMetadataSuccess(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}
