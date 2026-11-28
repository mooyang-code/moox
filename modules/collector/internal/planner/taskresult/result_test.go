package taskresult

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/stretchr/testify/require"
)

func TestResultIDsUseReadableTaskSlug(t *testing.T) {
	ids := ResultIDs("builtin-binance-spot-kline-1m")
	require.Equal(t, "dataset_builtin_binance_spot_kline_1m", ids.DatasetID)

	custom := ResultIDs("builtin-custom-kline-1m")
	require.Equal(t, "dataset_builtin_custom_kline_1m", custom.DatasetID)

	fiveMinute := ResultIDs("task-5m")
	require.Equal(t, "dataset_task_5m", fiveMinute.DatasetID)
	require.NotEqual(t, fiveMinute, ResultIDs("task-1h"))
}

func TestBuiltinResultIDsRemainTaskOwnedAndDoNotReuseCatalogIDs(t *testing.T) {
	spot := ResultIDs("builtin-binance-spot-kline-1h")
	require.Equal(t, "dataset_builtin_binance_spot_kline_1h", spot.DatasetID)
	require.NotEqual(t, "dataset_spot_kline_1h", spot.DatasetID)

	swap := ResultIDs("builtin-binance-swap-kline-1h")
	require.Equal(t, "dataset_builtin_binance_swap_kline_1h", swap.DatasetID)
	require.NotEqual(t, "dataset_perpetual_kline_1h", swap.DatasetID)
}

func TestPersistedResultIDsPrefersCommittedDataset(t *testing.T) {
	require.Equal(t, IDs{DatasetID: "dataset_committed"}, PersistedResultIDs("task-1", " dataset_committed "))
	require.Equal(t, ResultIDs("task-1"), PersistedResultIDs("task-1", ""))
}

func TestResultDisplayNameUsesShortChinese(t *testing.T) {
	require.Equal(t, "合约小时K线", resultDisplayName(Config{Name: "Binance 合约 K 线 1H"}, "builtin-binance-swap-kline-1h"))
	require.Equal(t, "采集结果", resultDisplayName(Config{Name: "too long english name"}, "custom-task"))
	require.True(t, isChineseDisplayName("现货分钟K线"))
	require.False(t, isChineseDisplayName("Binance 现货 K 线 1m"))
}

// 基础 K 线字段都带中文显示名与一级分组；基础 K 线字段的名字按种子写法。
func TestOutputFieldsCarryDisplayNameAndGroup(t *testing.T) {
	seen := map[string]bool{}
	for _, field := range KlineOutputFields {
		require.NotEmpty(t, field.DisplayName, field.Name)
		require.Contains(t, []string{GroupIdentity, GroupQuote, GroupTrading}, field.Group, field.Name)
		require.False(t, seen[field.Name], "字段 %s 重复", field.Name)
		seen[field.Name] = true
	}
	for name, want := range map[string][2]string{
		"open": {"开盘价", GroupQuote}, "close": {"收盘价", GroupQuote},
		"volume": {"成交量", GroupTrading}, "amount": {"成交额", GroupTrading}, "quote_volume": {"计价币成交额", GroupTrading},
	} {
		field, ok := KlineOutputField(name)
		require.True(t, ok, name)
		require.Equal(t, want[0], field.DisplayName, name)
		require.Equal(t, want[1], field.Group, name)
	}
}

type resultMetadataFake struct {
	datasets map[string]*storagepb.Dataset
	// viewStates 是 storage-view 里各数据集查询索引的已建状态；没有的按 VIEW_NOT_FOUND 回答。
	viewStates     map[string]*storagepb.GetViewStateRsp
	createDatasets int
	columnNames    []string
	columns        []*storagepb.DatasetColumn
	// upsertColumns 只统计单独补列的调用，随 CreateDataset 一起写入的列不算。
	upsertColumns int
	deleted       []string
}

func newResultMetadataFake() *resultMetadataFake {
	return &resultMetadataFake{datasets: map[string]*storagepb.Dataset{}, viewStates: map[string]*storagepb.GetViewStateRsp{}}
}

func resultOK() *storagepb.RetInfo { return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS} }
func resultNotFound() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_NOT_FOUND}
}

func TestResultDeleteAcceptedIsIdempotent(t *testing.T) {
	for _, code := range []storagepb.ErrorCode{
		storagepb.ErrorCode_SUCCESS,
		storagepb.ErrorCode_DATASET_NOT_FOUND,
		storagepb.ErrorCode_NOT_FOUND,
	} {
		require.True(t, resultDeleteAccepted(&storagepb.RetInfo{Code: code}), "code=%s", code)
	}
	require.False(t, resultDeleteAccepted(&storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR}))
}

func (f *resultMetadataFake) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	dataset := f.datasets[req.GetDatasetId()]
	if dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: resultNotFound()}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: resultOK(), Dataset: dataset}, nil
}

func (f *resultMetadataFake) UpsertDataset(_ context.Context, req *storagepb.UpsertDatasetReq) (*storagepb.UpsertDatasetRsp, error) {
	dataset := req.GetDataset()
	if _, existed := f.datasets[dataset.GetDatasetId()]; !existed {
		f.createDatasets++
		// 和 Storage 一样：数据集建好即启用，列随数据集一起写入。
		dataset.Status = "active"
		for _, column := range req.GetColumns() {
			column.SpaceId, column.DatasetId = dataset.GetSpaceId(), dataset.GetDatasetId()
			f.columnNames = append(f.columnNames, column.GetColumnName())
			f.columns = append(f.columns, column)
		}
	}
	f.datasets[dataset.GetDatasetId()] = dataset
	return &storagepb.UpsertDatasetRsp{RetInfo: resultOK(), Dataset: dataset}, nil
}

func (f *resultMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	f.deleted = append(f.deleted, req.GetDatasetId())
	delete(f.datasets, req.GetDatasetId())
	return &storagepb.DeleteDatasetRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.upsertColumns++
	f.columnNames = append(f.columnNames, req.GetColumn().GetColumnName())
	f.columns = append(f.columns, req.GetColumn())
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) GetViewState(_ context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	if state := f.viewStates[req.GetDatasetId()]; state != nil {
		return state, nil
	}
	return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}, nil
}

func TestEnsureCreatesOneExclusiveResultAndIsIdempotent(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake)
	config := Config{Frequency: "1h", SubjectTags: []string{"binance_spot"}, OutputFields: []string{"close"}}
	first, err := manager.Ensure(context.Background(), "crypto", "task-1", "kline", "spot", config)
	require.NoError(t, err)
	second, err := manager.Ensure(context.Background(), "crypto", "task-1", "kline", "spot", config)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, fake.createDatasets)
	require.Equal(t, "collector", fake.datasets[first.DatasetID].GetAttributes()["owner_module"])
	require.Equal(t, "raw_collection", fake.datasets[first.DatasetID].GetAttributes()["dataset_role"])
	require.Equal(t, "task-1", fake.datasets[first.DatasetID].GetAttributes()["collector_task_id"])
	require.Equal(t, []string{"binance_spot"}, fake.datasets[first.DatasetID].GetSubjectTags())
	// 首次建数据集时列随 CreateDataset 一起写入；第二次走已有数据集，按同一定义补一遍列。
	require.Equal(t, []string{"close", "close"}, fake.columnNames)
	require.Equal(t, 1, fake.upsertColumns)
	close := fake.columns[0]
	require.Equal(t, "收盘价", close.GetDisplayName())
	require.Equal(t, GroupQuote, close.GetGroup())
	require.Empty(t, close.GetAttributes(), "基础字段不带属性")
}

// 改输出字段只补数据集的列：查询索引的列跟着数据集走；不支持的字段照旧拒绝。
func TestEnsureOutputFieldsOnlyAddsDatasetColumns(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("task-projection")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-projection"},
	}
	manager := NewManagerWithAPI(fake)
	require.NoError(t, manager.EnsureOutputFields(context.Background(), "crypto", "task-projection", ids.DatasetID, "kline", []string{"open", "close"}))
	require.Equal(t, []string{"open", "close"}, fake.columnNames)

	err := manager.EnsureOutputFields(context.Background(), "crypto", "task-projection", ids.DatasetID, "kline", []string{"no_such_field"})
	require.ErrorIs(t, err, ErrUnsupportedOutputFields)
}

func TestEnsurePreservesExistingSubjectTagsWhenConfigOmitsTags(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("task-existing-tags")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes:  map[string]string{"owner_module": "collector", "collector_task_id": "task-existing-tags"},
		SubjectTags: []string{"binance_spot"},
	}
	manager := NewManagerWithAPI(fake)

	_, err := manager.Ensure(context.Background(), "crypto", "task-existing-tags", "kline", "spot", Config{
		Frequency: "1h",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"binance_spot"}, fake.datasets[ids.DatasetID].GetSubjectTags())
}

func TestEnsureUpdatesExistingSubjectTagsWithRequestedScope(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("task-update-tags")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes:  map[string]string{"owner_module": "collector", "collector_task_id": "task-update-tags"},
		SubjectTags: []string{"old_tag"},
	}
	manager := NewManagerWithAPI(fake)

	_, err := manager.Ensure(context.Background(), "crypto", "task-update-tags", "kline", "spot", Config{
		Frequency: "1h", SubjectTags: []string{"new_tag"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"new_tag"}, fake.datasets[ids.DatasetID].GetSubjectTags())
}

func TestEnsureDeclaresTheTaskFrequency(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake)
	ids, err := manager.Ensure(context.Background(), "crypto", "task-5m", "kline", "spot", Config{Frequency: "5m"})
	require.NoError(t, err)
	require.Equal(t, "5m", fake.datasets[ids.DatasetID].GetFreq())

	ids, err = manager.Ensure(context.Background(), "crypto", "task-1h", "kline", "spot", Config{Frequency: "1H"})
	require.NoError(t, err)
	require.Equal(t, "1h", fake.datasets[ids.DatasetID].GetFreq(), "a frequency alias is declared in canonical form")
}

func TestEnsureWritesTheMarketCalendarAndAlignment(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake)
	stock, err := manager.Ensure(context.Background(), "stockcn", "task-stock-5m", "kline", "equity", Config{Frequency: "5m"})
	require.NoError(t, err)
	attrs := fake.datasets[stock.DatasetID].GetAttributes()
	require.Equal(t, "cn_stock", attrs["calendar"])
	require.Equal(t, "session", fake.datasets[stock.DatasetID].GetAlignment())
	require.Equal(t, "equity", attrs["market_type"])

	crypto, err := manager.Ensure(context.Background(), "crypto", "task-crypto-5m", "kline", "spot", Config{Frequency: "5m"})
	require.NoError(t, err)
	attrs = fake.datasets[crypto.DatasetID].GetAttributes()
	_, hasCalendar := attrs["calendar"]
	require.False(t, hasCalendar || fake.datasets[crypto.DatasetID].GetAlignment() != "", "crypto 不写 calendar、alignment，策略缺省按 crypto_24x7")
	require.Equal(t, "spot", attrs["market_type"])

	// 已有的结果数据集缺这两项时补上，已有的值不改。
	ids := ResultIDs("task-existing")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "stockcn", DatasetId: ids.DatasetID, Status: "active",
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-existing", "market_type": "equity"},
	}
	_, err = manager.Ensure(context.Background(), "stockcn", "task-existing", "kline", "equity", Config{Frequency: "5m"})
	require.NoError(t, err)
	attrs = fake.datasets[ids.DatasetID].GetAttributes()
	require.Equal(t, "cn_stock", attrs["calendar"])
	require.Equal(t, "session", fake.datasets[ids.DatasetID].GetAlignment())
}

// 补偿动作只删本轮新建的结果数据集，已有的不删。
func TestEnsureWithCleanupRemovesOnlyNewlyCreatedDataset(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake)
	ids, cleanup, err := manager.EnsureWithCleanup(context.Background(), "crypto", "task-new", "kline", "spot", Config{Frequency: "1h"})
	require.NoError(t, err)
	require.NoError(t, cleanup(context.Background()))
	require.Equal(t, []string{ids.DatasetID}, fake.deleted)

	fake.deleted = nil
	_, cleanup, err = manager.EnsureWithCleanup(context.Background(), "crypto", "task-new", "kline", "spot", Config{Frequency: "1h"})
	require.NoError(t, err)
	_, cleanup, err = manager.EnsureWithCleanup(context.Background(), "crypto", "task-new", "kline", "spot", Config{Frequency: "1h"})
	require.NoError(t, err)
	require.NoError(t, cleanup(context.Background()))
	require.Empty(t, fake.deleted, "已有的结果数据集不删")
}

func TestInspectReportsTaskOwnedReadyResultAndLastDataTime(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("task-inspect")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID,
		Attributes: map[string]string{
			"owner_module":      "collector",
			"collector_task_id": "task-inspect",
		},
		Status: "active",
	}
	fake.viewStates[ids.DatasetID] = &storagepb.GetViewStateRsp{RetInfo: resultOK(), IndexId: "idx", IndexedFrom: "2026-09-01T00:00:00Z", IndexedTo: "2026-09-20T04:00:00Z"}
	manager := NewManagerWithAPI(fake)

	inspection, err := manager.Inspect(context.Background(), "crypto", "task-inspect")
	require.NoError(t, err)
	require.Equal(t, ids, inspection.IDs)
	require.Equal(t, "ready", inspection.Status)
	require.Equal(t, "2026-09-20T04:00:00Z", inspection.LastDataTime)
	require.Equal(t, "2026-09-01T00:00:00Z", inspection.CoverageStart)
	require.Equal(t, "2026-09-20T04:00:00Z", inspection.CoverageEnd)
}

func TestInspectReportsPendingWhenResultDatasetIsMissing(t *testing.T) {
	manager := NewManagerWithAPI(newResultMetadataFake())

	inspection, err := manager.Inspect(context.Background(), "crypto", "task-pending")
	require.NoError(t, err)
	require.Equal(t, "pending", inspection.Status)
	require.Empty(t, inspection.LastDataTime)
	require.Nil(t, inspection.Dataset)
}

func TestInspectRejectsResultOwnedByAnotherTask(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("task-inspect")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID,
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "other-task"},
		Status:     "active",
	}
	manager := NewManagerWithAPI(fake)

	_, err := manager.Inspect(context.Background(), "crypto", "task-inspect")
	require.ErrorContains(t, err, "owned by another task")
	require.ErrorIs(t, err, ErrResultContract)
}

func TestInspectIDsRejectsMetadataIdentityMismatch(t *testing.T) {
	for name, dataset := range map[string]*storagepb.Dataset{
		"dataset space mismatch": {
			SpaceId: "other-space", DatasetId: "requested-dataset",
			Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
		},
		"dataset id mismatch": {
			SpaceId: "crypto", DatasetId: "other-dataset",
			Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newResultMetadataFake()
			ids := IDs{DatasetID: "requested-dataset"}
			fake.datasets[ids.DatasetID] = dataset
			manager := NewManagerWithAPI(fake)

			_, err := manager.InspectIDs(context.Background(), "crypto", "task-inspect", ids)
			require.ErrorIs(t, err, ErrResultContract)
			require.ErrorContains(t, err, "dataset identity does not match requested")
		})
	}
}

func TestOwnedByTaskDoesNotAcceptConflictingLegacyOwner(t *testing.T) {
	require.True(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-b"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "factor", "collector_task_id": "task-a"}, "task-a"))
}

func TestOwnedByTaskRejectsUnownedCatalogDataset(t *testing.T) {
	require.False(t, ownedByTask(map[string]string{"market_type": "spot"}, "builtin-binance-spot-kline-1m"))
	require.False(t, ownedByTask(nil, "builtin-binance-spot-kline-1m"))
}

func TestEnsureRejectsUnownedDatasetCollision(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("builtin-binance-spot-kline-1m")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active", Attributes: map[string]string{"market_type": "spot"},
	}
	manager := NewManagerWithAPI(fake)
	_, err := manager.Ensure(context.Background(), "crypto", "builtin-binance-spot-kline-1m", "kline", "spot", Config{
		Frequency: "1m",
	})
	require.ErrorContains(t, err, "owned by another task")
}

// 删除结果只删数据集一次调用；查询索引由 storage-view 随数据集摘掉。
func TestDeleteForTaskRemovesTaskOwnedDataset(t *testing.T) {
	fake := newResultMetadataFake()
	taskID := "builtin-binance-spot-kline-1m"
	ids := ResultIDs(taskID)
	attrs := map[string]string{"owner_module": "collector", "collector_task_id": taskID}
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active", Attributes: attrs}
	manager := NewManagerWithAPI(fake)
	require.NoError(t, manager.DeleteForTask(context.Background(), "crypto", taskID, ids))
	require.Equal(t, []string{ids.DatasetID}, fake.deleted)

	// 别的任务的数据集不删。
	other := ResultIDs("other-task")
	fake.datasets[other.DatasetID] = &storagepb.Dataset{SpaceId: "crypto", DatasetId: other.DatasetID, Attributes: attrs}
	require.Error(t, manager.DeleteForTask(context.Background(), "crypto", "other-task", other))
	require.Contains(t, fake.datasets, other.DatasetID)
}

// taggedResultMetadataFake also serves Tags, like the production proxy.
type taggedResultMetadataFake struct {
	*resultMetadataFake
	tagSources map[string]string
	tagMarkets map[string]string
}

func (f taggedResultMetadataFake) GetTag(_ context.Context, req *storagepb.GetTagReq) (*storagepb.GetTagRsp, error) {
	source, ok := f.tagSources[req.GetTagId()]
	if !ok {
		return &storagepb.GetTagRsp{RetInfo: resultNotFound()}, nil
	}
	attrs := map[string]string{"sync.source": source}
	if market := f.tagMarkets[req.GetTagId()]; market != "" {
		attrs["market_type"] = market
	}
	return &storagepb.GetTagRsp{RetInfo: resultOK(), Tag: &storagepb.Tag{SpaceId: req.GetSpaceId(), TagId: req.GetTagId(), Attributes: attrs}}, nil
}

func TestEnsureRecordsTheTagsSharedDataSource(t *testing.T) {
	fake := taggedResultMetadataFake{newResultMetadataFake(), map[string]string{"binance_spot": "binance", "binance_swap": "binance", "okx_spot": "okx"}, nil}
	manager := NewManagerWithAPI(fake)
	ensure := func(taskID string, tags ...string) *storagepb.Dataset {
		_, err := manager.Ensure(context.Background(), "crypto", taskID, "kline", "", Config{Frequency: "1m", SubjectTags: tags})
		require.NoError(t, err)
		return fake.datasets[ResultIDs(taskID).DatasetID]
	}
	require.Equal(t, "binance", ensure("single-source", "binance_spot").GetDataSourceId())
	require.Equal(t, "binance", ensure("same-source", "binance_spot", "binance_swap").GetDataSourceId())
	require.Empty(t, ensure("mixed-source", "binance_spot", "okx_spot").GetDataSourceId(), "a result spanning sources has no single data source")

	ids := ResultIDs("unsourced")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active", SubjectTags: []string{"binance_swap"},
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "unsourced"},
	}
	require.Equal(t, "binance", ensure("unsourced", "binance_swap").GetDataSourceId(), "an existing result without a data source gets it assigned")

	_, err := manager.Ensure(context.Background(), "crypto", "missing-tag", "kline", "", Config{Frequency: "1m", SubjectTags: []string{"unknown"}})
	require.Error(t, err)
}

// 单市场的标签写它自己的市场与数据源；市场或数据源不全相同的（建任务时已拒绝）两者都留空，不会写出 multi。
func TestEnsureWritesTheSingleMarketOfTheTags(t *testing.T) {
	fake := taggedResultMetadataFake{newResultMetadataFake(),
		map[string]string{"binance_spot": "binance", "binance_swap": "binance", "okx_swap": "okx"},
		map[string]string{"binance_spot": "spot", "binance_swap": "swap", "okx_swap": "swap"}}
	manager := NewManagerWithAPI(fake)
	ensure := func(taskID string, tags ...string) *storagepb.Dataset {
		_, err := manager.Ensure(context.Background(), "crypto", taskID, "kline", "", Config{Frequency: "1m", SubjectTags: tags})
		require.NoError(t, err)
		return fake.datasets[ResultIDs(taskID).DatasetID]
	}
	spotSwap := ensure("spot_swap", "binance_spot", "binance_swap")
	require.Empty(t, spotSwap.GetAttributes()["market_type"], "现货加永续不再是一个数据集")
	single := ensure("single", "binance_swap")
	require.Equal(t, "swap", single.GetAttributes()["market_type"], "单标签任务不变")
	require.Equal(t, "binance", single.GetDataSourceId())
	mixed := ensure("mixed", "binance_spot", "okx_swap")
	require.Empty(t, mixed.GetDataSourceId(), "数据源不同")
	require.Empty(t, mixed.GetAttributes()["market_type"])
}

// 附加字段只在显式选中时建列；列的显示名、分组取自字段规格，属性只写 fill（因子引擎没有这个属性就沿用上一根，会重复计费）。
func TestEnsureColumnsCreatesExtraFieldFromSpecOnlyWhenSelected(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake)
	spec := marketdata.FieldSpec{ID: "funding_rate", Name: "资金费率", Group: marketdata.GroupDerivatives, Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1h", "1d"}}
	manager.SetFieldOffers(marketdata.FieldOffers{{MarketID: "crypto", Instrument: "swap", ProviderID: "binance", Spec: spec}})
	require.NoError(t, manager.ensureColumns(context.Background(), "crypto", "dataset", nil))
	require.NotContains(t, fake.columnNames, "funding_rate", "默认字段不含资金费率")

	fake.columnNames, fake.columns = nil, nil
	require.NoError(t, manager.ensureColumns(context.Background(), "crypto", "dataset", []string{"close", "funding_rate"}))
	require.Equal(t, []string{"close", "funding_rate"}, fake.columnNames)
	funding := fake.columns[1]
	require.Equal(t, storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, funding.GetValueType())
	require.Equal(t, spec.Name, funding.GetDisplayName())
	require.Equal(t, spec.Group, funding.GetGroup())
	require.Equal(t, map[string]string{"fill": "zero"}, funding.GetAttributes())
}

// 未登记的市场报错，不当成连续交易的市场悄悄跳过 calendar、alignment。
func TestEnsureRejectsUnregisteredMarket(t *testing.T) {
	manager := NewManagerWithAPI(newResultMetadataFake())
	_, err := manager.Ensure(context.Background(), "no_such_space", "task-unknown-5m", "kline", "equity", Config{Frequency: "5m"})
	require.ErrorIs(t, err, barclock.ErrUnknownMarket)
}

// 覆盖范围只取已建好的索引：索引还在建时退回属性；storage-view 报别的错照常返回错误，不当成覆盖范围未知。
func TestInspectCoverageComesFromBuiltViewIndex(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("task-coverage")
	attrs := map[string]string{"owner_module": "collector", "collector_task_id": "task-coverage", "indexed_to": "2026-09-19T00:00:00Z"}
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active", Attributes: attrs}
	manager := NewManagerWithAPI(fake)

	fake.viewStates[ids.DatasetID] = &storagepb.GetViewStateRsp{RetInfo: resultOK(), IndexedTo: "2026-09-20T04:00:00Z"}
	inspection, err := manager.Inspect(context.Background(), "crypto", "task-coverage")
	require.NoError(t, err)
	require.Equal(t, "2026-09-19T00:00:00Z", inspection.LastDataTime, "索引还没建好，退回数据集属性")
	require.Equal(t, "2026-09-19T00:00:00Z", inspection.CoverageEnd)

	// storage-view 暂时不可用：覆盖范围按未知，退回属性，清单不失败。
	fake.viewStates[ids.DatasetID] = &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "storage-view 不可用"}}
	inspection, err = manager.Inspect(context.Background(), "crypto", "task-coverage")
	require.NoError(t, err)
	require.Equal(t, "2026-09-19T00:00:00Z", inspection.CoverageEnd)

	// 调用契约错误照常上报。
	fake.viewStates[ids.DatasetID] = &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_NO_PERMISSION, Msg: "无权限"}}
	_, err = manager.Inspect(context.Background(), "crypto", "task-coverage")
	require.ErrorIs(t, err, ErrResultContract)
}
