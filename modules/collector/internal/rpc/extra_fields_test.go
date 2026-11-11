package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// testFieldOffers：funding_rate 只有永续有来源；basis 现货和永续都有来源，只支持 1h。
func testFieldOffers() marketdata.FieldOffers {
	funding := marketdata.FieldSpec{ID: "funding_rate", Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1h", "1d"}}
	basis := marketdata.FieldSpec{ID: "basis", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: []string{"1h"}}
	return marketdata.FieldOffers{
		{MarketID: "crypto", Instrument: "swap", ProviderID: "binance", Spec: funding},
		{MarketID: "crypto", Instrument: "spot", ProviderID: "fake_basis", Spec: basis},
		{MarketID: "crypto", Instrument: "swap", ProviderID: "fake_basis", Spec: basis},
	}
}

// 附加字段的通用校验：任务频率要在字段适用的频率里；任务每个标签的（市场, 品种）下都要有来源；重采样不能选。
func TestValidateCollectionTaskTagRoutesExtraFields(t *testing.T) {
	tags := map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
		"binance_swap": {TagId: "binance_swap", Attributes: map[string]string{"sync.source": "binance", "market_type": "swap"}},
	}
	service := &Service{datasetSrc: tagValidationSource{tags: tags}, fieldOffers: testFieldOffers()}
	validate := func(frequency, fields string, tagIDs ...string) error {
		return service.validateCollectionTaskTagRoutes(context.Background(), domain.CollectionTask{SpaceID: "crypto", DataType: "kline", TagIDs: tagIDs,
			CollectParams: `{"target_dataset_id":"bars","frequency":"` + frequency + `","output_fields":[` + fields + `]}`})
	}
	// 现货和永续都有来源的字段，统一数据集可以选。
	require.NoError(t, validate("1h", `"close","basis"`, "binance_spot", "binance_swap"))
	require.NoError(t, validate("1h", `"close","basis","funding_rate"`, "binance_swap"))
	// 频率不在字段适用的频率里。
	require.ErrorContains(t, validate("1d", `"close","basis"`, "binance_spot"), "输出字段 basis 只支持 1h 频率")
	require.ErrorContains(t, validate("1m", `"close","funding_rate"`, "binance_swap"), "输出字段 funding_rate 只支持 1h、1d 频率")
	// 有一个标签的市场下没有来源。
	require.ErrorContains(t, validate("1h", `"close","basis","funding_rate"`, "binance_spot", "binance_swap"), "输出字段 funding_rate 在标签 binance_spot 的市场 spot 下没有来源")
	// 不是附加字段的输出字段不受这条规则约束。
	require.NoError(t, validate("1m", `"close","volume"`, "binance_spot", "binance_swap"))

	// 字段只在自己的市场里是附加字段：别的空间选它不走这条规则（建列时按不认识的字段拒绝）。
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), domain.CollectionTask{SpaceID: "stockcn", DataType: "kline_resample",
		CollectParams: `{"source_dataset_id":"a","target_dataset_id":"b","source_frequency":"1m","target_frequency":"1h","source_series_tag":"default","output_fields":["close","basis"]}`}))
	// 重采样不合成附加字段。
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), domain.CollectionTask{SpaceID: "crypto", DataType: "kline_resample",
		CollectParams: `{"source_dataset_id":"a","target_dataset_id":"b","source_frequency":"1m","target_frequency":"1h","source_series_tag":"venue:binance","output_fields":["close","basis"]}`}),
		"重采样不能合成附加字段 basis")
}

// 改任务只能减少输出字段：新增基础字段或附加字段都拒绝，提示新建任务。
func TestAddedOutputFields(t *testing.T) {
	service := &Service{fieldOffers: testFieldOffers()}
	require.Empty(t, service.addedOutputFields("crypto", []string{"close", "volume", "funding_rate"}, []string{"close"}), "减少可以")
	require.Empty(t, service.addedOutputFields("crypto", []string{"close", "volume"}, []string{"volume", "close"}))
	require.Equal(t, []string{"open"}, service.addedOutputFields("crypto", []string{"close", "volume"}, []string{"close", "open"}), "新增基础字段")
	require.Equal(t, []string{"funding_rate"}, service.addedOutputFields("crypto", []string{"close"}, []string{"close", "funding_rate"}), "新增附加字段")
	// 原来没有显式选择（全部默认字段）：收窄到基础字段是减少，选附加字段是新增。
	require.Empty(t, service.addedOutputFields("crypto", nil, []string{"close", "volume"}))
	require.Equal(t, []string{"basis"}, service.addedOutputFields("crypto", nil, []string{"close", "basis"}))
}

func TestUpdateTaskRejectsAddedOutputFields(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", TaskName: "swap 1h",
		DataType: "kline", CollectParams: `{"target_dataset_id":"dataset-1","frequency":"1h","output_fields":["close","volume"]}`,
		Enabled: true, PrepareState: domain.PrepareStateReady, ResultDatasetID: "dataset-1", ResultViewID: "view-1",
	}))
	service := &Service{taskRepo: db.Tasks(), fieldOffers: testFieldOffers(), datasetSrc: validationDatasetSource{
		"dataset-1": storagesource.DatasetInfo{DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1h", SubjectTags: []string{"binance_swap"}, Attributes: map[string]string{"market_type": "swap"}},
	}}
	update := func(fields ...any) *pb.UpdateTaskRsp {
		params, structErr := structpb.NewStruct(map[string]any{"provider": "binance", "market_type": "swap", "frequency": "1h", "output_fields": fields})
		require.NoError(t, structErr)
		rsp, callErr := service.UpdateTask(context.Background(), &pb.UpdateTaskReq{SpaceId: "crypto", TaskId: "task-1",
			Task: &pb.CollectionTask{SpaceId: "crypto", TaskId: "task-1", TaskName: "swap 1h", DataType: "kline", CollectParams: params}})
		require.NoError(t, callErr)
		return rsp
	}
	for _, added := range []string{"open", "funding_rate"} {
		rsp := update("close", "volume", added)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), added)
		require.Contains(t, rsp.GetRetInfo().GetMsg(), "输出字段只能减少，不能新增（"+added+"）")
		require.Contains(t, rsp.GetRetInfo().GetMsg(), "请创建新任务")
	}
	// 减少不拦：走到后面的结果数据集改列（这个测试没有配结果管理器）。
	rsp := update("close")
	require.NotContains(t, rsp.GetRetInfo().GetMsg(), "只能减少")
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "task result manager is not configured")
	// 字段不变时照常更新。
	require.Equal(t, pb.ErrorCode_SUCCESS, update("volume", "close").GetRetInfo().GetCode())
}

// 任务表单接口列出 K 线任务可选的附加字段：字段 ID、适用频率、适用的市场和品种、来源提供方。
func TestGetDataTypeConfigWithFieldsListsExtraFields(t *testing.T) {
	service := &Service{fieldOffers: testFieldOffers()}
	rsp, err := service.GetDataTypeConfigWithFields(context.Background(), &pb.GetDataTypeConfigWithFieldsReq{DataType: "kline"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	options := rsp.GetDetail().GetExtraFields()
	require.Len(t, options, 3)
	require.Equal(t, "funding_rate", options[0].GetFieldId())
	require.Equal(t, "crypto", options[0].GetMarketId())
	require.Equal(t, "swap", options[0].GetInstrumentType())
	require.Equal(t, []string{"1h", "1d"}, options[0].GetFrequencies())
	require.Equal(t, "binance", options[0].GetProviderId())

	rsp, err = service.GetDataTypeConfigWithFields(context.Background(), &pb.GetDataTypeConfigWithFieldsReq{DataType: "kline_resample"})
	require.NoError(t, err)
	require.Empty(t, rsp.GetDetail().GetExtraFields(), "重采样没有附加字段")
}
