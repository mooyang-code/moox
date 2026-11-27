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
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

var ErrTargetViewNotReady = errors.New("target View is not ready")

type metadataAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq, ...client.Option) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq, ...client.Option) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
}

// Catalog is the narrow Metadata API needed to provision a task-owned target
// dataset; its query View is created by Storage together with the dataset.
type Catalog struct {
	Metadata metadataAPI
	// View 用来等目标数据集的查询索引把列建好。
	View ViewWaiter
}

// klineField 是合成任务取的一列与它是否必填；类型、显示名与分组取自 taskresult.KlineOutputFields。
type klineField struct {
	name     string
	required bool
}

var priceFields = []klineField{
	{"open", true},
	{"high", true},
	{"low", true},
	{"close", true},
	{"volume", true},
}

// fieldsFor 返回市场的合成结果字段：加密货币带计价币成交额和成交笔数；A 股只有成交额，且可以为空。
func fieldsFor(tradeCount bool) []klineField {
	if tradeCount {
		return append(append([]klineField(nil), priceFields...),
			klineField{"quote_volume", true},
			klineField{"trade_num", true})
	}
	return append(append([]klineField(nil), priceFields...),
		klineField{"amount", false})
}

// AllOutputFieldNames 按 K 线基础字段顺序返回重采样可输出的所有基础字段名（供管理台任务表单展示）。
func AllOutputFieldNames() []string {
	return []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "amount"}
}

// ValidateOutputFields 校验重采样任务指定的输出字段是否均属于该市场可合成的字段集合。
func ValidateOutputFields(spaceID string, outputFields []string) error {
	if len(outputFields) == 0 {
		return nil
	}
	// 保留对资金费率的明确提示，源数据集带这一列不影响合成，但不能显式要求输出它。
	if domain.HasOutputField(outputFields, domain.FieldFundingRate) {
		return fmt.Errorf("重采样不能合成输出字段 %s，请从输出字段里去掉它", domain.FieldFundingRate)
	}
	tradeCount, err := hasTradeCount(spaceID)
	if err != nil {
		return err
	}
	allowed := fieldsFor(tradeCount)
	allowedSet := make(map[string]struct{}, len(allowed))
	allowedNames := make([]string, 0, len(allowed))
	for _, field := range allowed {
		allowedSet[field.name] = struct{}{}
		allowedNames = append(allowedNames, field.name)
	}
	for _, raw := range outputFields {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := allowedSet[name]; !ok {
			return fmt.Errorf("重采样不能合成输出字段 %s（当前市场可选：%s）", name, strings.Join(allowedNames, "、"))
		}
	}
	return nil
}

// PrepareTarget creates or validates the target Dataset and its columns. Existing resources with a mismatched immutable contract
// are rejected instead of being silently overwritten.
func (c *Catalog) PrepareTarget(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, source storagesource.DatasetInfo, subjects []domain.Subject) error {
	if c == nil || c.Metadata == nil {
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
	sessionAttrs, _, err := taskresult.SessionAttributes(task.SpaceID)
	if err != nil {
		return err
	}
	ids := taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID)
	params.TargetDatasetID = ids.DatasetID
	targetDatasetID := ids.DatasetID
	// 逐级合成时源本身是合成结果、没有数据来源：沿用它记下的原始 K 线来源，因子任务据此绑定数据来源。
	sourceDataSourceID := source.DataSourceID
	if sourceDataSourceID == "" {
		sourceDataSourceID = source.Attributes["source_data_source_id"]
	}
	immutableAttrs := map[string]string{
		"owner_module": "collector", "managed_by": "collector", "collector_task_id": task.TaskID, "market_type": strings.ToLower(params.MarketType),
		"storage_model": "wide_common_metrics", "dataset_role": "kline_resample_result",
		"source_dataset_id": params.SourceDatasetID, "source_data_source_id": sourceDataSourceID,
		"source_freq": params.SourceFrequency, "source_series_tag": params.SourceSeriesTag,
		"target_freq": targetFreq.Storage,
	}
	attrs := cloneStringMap(immutableAttrs)
	// 交易时段市场写入与原始采集一致的 calendar 属性，避免下游策略因缺省回退到 crypto_24x7 日历。
	for key, value := range sessionAttrs {
		attrs[key] = value
	}
	createdDataset := false
	compensate := func(original error) error {
		if original == nil {
			return nil
		}
		// 查询索引还没追上只是需要等：资源的 ID 是确定的，保留它们，下一轮准备时继续等，不能每轮删了重建。
		if errors.Is(original, ErrTargetViewNotReady) || !createdDataset {
			return original
		}
		if cleanupErr := c.cleanupCreatedTarget(ctx, task.SpaceID, targetDatasetID); cleanupErr != nil {
			return errors.Join(original, cleanupErr)
		}
		return original
	}
	target, getErr := c.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: task.SpaceID, DatasetId: targetDatasetID})
	if getErr != nil {
		return fmt.Errorf("get target Dataset: %w", getErr)
	}
	if target.GetRetInfo() == nil {
		return errors.New("get target Dataset: empty ret_info")
	}
	columns := targetDatasetColumns(tradeCount, params.OutputFields)
	if taskresult.IsDatasetNotFound(target.GetRetInfo().GetCode()) {
		// 数据集连同列在一次调用里建好，建好即可写入。
		created, createErr := c.Metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{Dataset: &storagepb.Dataset{
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
	if target.GetDataset().GetAlignment() != market.Alignment {
		return compensate(fmt.Errorf("目标数据集的 alignment %q 与市场的 %q 不一致", target.GetDataset().GetAlignment(), market.Alignment))
	}
	// 先校验除派生属性（如 calendar）外的全部不可变血缘契约，确保不会误修改其他任务的数据集。
	if err := validateTargetDataset(target.GetDataset(), immutableAttrs, targetFreq.Storage); err != nil {
		return compensate(err)
	}
	// 存量交易时段市场目标数据集仅缺失 calendar 时原地回填；非空且不匹配时按血缘冲突拒绝。
	if !createdDataset && len(sessionAttrs) > 0 {
		missing := map[string]string{}
		for key, expected := range sessionAttrs {
			actual := strings.TrimSpace(target.GetDataset().GetAttributes()[key])
			if actual == "" {
				missing[key] = expected
				continue
			}
			if actual != expected {
				return compensate(fmt.Errorf("target Dataset immutable lineage attribute %s does not match task", key))
			}
		}
		if len(missing) > 0 {
			updated := proto.Clone(target.GetDataset()).(*storagepb.Dataset)
			if updated.Attributes == nil {
				updated.Attributes = map[string]string{}
			}
			for key, value := range missing {
				updated.Attributes[key] = value
			}
			resp, updateErr := c.Metadata.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{Dataset: updated})
			if updateErr != nil {
				return fmt.Errorf("update target Dataset attributes: %w", updateErr)
			}
			if err := ensureMetadataSuccess("update target Dataset attributes", resp.GetRetInfo()); err != nil {
				return err
			}
			if resp.GetDataset() != nil {
				target.Dataset = resp.GetDataset()
			} else {
				target.Dataset = updated
			}
		}
	}
	if err := validateTargetDataset(target.GetDataset(), attrs, targetFreq.Storage); err != nil {
		return compensate(err)
	}
	_ = subjects
	if !createdDataset {
		// 已有的目标数据集按同一份列定义补齐：上一轮建数据集时缓存没发布成功，这里也会让 Storage 重新发布。
		for _, column := range columns {
			column.SpaceId, column.DatasetId = task.SpaceID, targetDatasetID
			resp, callErr := c.Metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{Column: column})
			if callErr != nil {
				return fmt.Errorf("upsert target column %s: %w", column.GetColumnName(), callErr)
			}
			if err := ensureMetadataSuccess("upsert target column", resp.GetRetInfo()); err != nil {
				return err
			}
		}
	}
	// 查询索引随目标数据集由 Storage 建好；等声明的列都建进索引再放行采集。
	err = waitTargetViewColumns(ctx, c.View, task.SpaceID, targetDatasetID, columns, 5*time.Second)
	return compensate(err)
}

// targetDatasetColumns 返回目标数据集的列定义：市场的合成字段，指定了输出字段时只取选中的那些。
// 显示名与分组按字段名取自采集结果的基础字段（taskresult.KlineOutputFields），合成任务不另起名字。
func targetDatasetColumns(tradeCount bool, outputFields []string) []*storagepb.DatasetColumn {
	var columns []*storagepb.DatasetColumn
	for _, field := range fieldsFor(tradeCount) {
		if len(outputFields) > 0 && !containsOutputField(outputFields, field.name) {
			continue
		}
		spec, _ := taskresult.KlineOutputField(field.name)
		columns = append(columns, &storagepb.DatasetColumn{
			ColumnName: field.name, ValueType: spec.Kind, DisplayName: spec.DisplayName, Group: spec.Group,
			Required: field.required, Status: "active",
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

// cleanupCreatedTarget 删掉本轮刚建的目标数据集；它的查询索引由 storage-view 随之摘掉。
func (c *Catalog) cleanupCreatedTarget(ctx context.Context, spaceID, datasetID string) error {
	if c == nil || c.Metadata == nil {
		return errors.New("resample catalog metadata is not configured")
	}
	dataset, err := c.Metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return fmt.Errorf("delete newly-created target Dataset: %w", err)
	}
	if dataset == nil || (dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
		return catalogMetadataError("delete newly-created target Dataset", nil, retInfoDatasetDelete(dataset))
	}
	return nil
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

// waitTargetViewColumns 等到目标数据集的活动索引建好、声明的列都已建进索引才放行采集。storage-view 还没发现这个数据集
// （VIEW_NOT_FOUND）或索引还在建（index_id 为空）都继续等，超时返回 ErrTargetViewNotReady，下一轮准备时再等。
func waitTargetViewColumns(ctx context.Context, view ViewWaiter, spaceID, datasetID string, columns []*storagepb.DatasetColumn, timeout time.Duration) error {
	if view == nil {
		return errors.New("合成需要 DataView 客户端")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		response, err := view.GetViewState(waitCtx, &storagepb.GetViewStateReq{SpaceId: spaceID, DatasetId: datasetID})
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

func targetViewColumnsReady(state *storagepb.GetViewStateRsp, columns []*storagepb.DatasetColumn) bool {
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
