package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// testFieldOffers：funding_rate 只有永续有来源；basis 现货和永续都有来源，只支持 1h。
func testFieldOffers() marketdata.FieldOffers {
	funding := marketdata.FieldSpec{ID: "funding_rate", Name: "资金费率", Group: marketdata.GroupDerivatives, Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1h", "1d"}}
	basis := marketdata.FieldSpec{ID: "basis", Name: "基差", Group: marketdata.GroupDerivatives, Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: []string{"1h"}}
	return marketdata.FieldOffers{
		{MarketID: "crypto", Instrument: "swap", ProviderID: "binance", ProviderName: "Binance", Spec: funding},
		{MarketID: "crypto", Instrument: "spot", ProviderID: "fake_basis", ProviderName: "模拟基差", Spec: basis},
		{MarketID: "crypto", Instrument: "swap", ProviderID: "fake_basis", ProviderName: "模拟基差", Spec: basis},
	}
}

// 附加字段的通用校验：任务频率要在字段适用的频率里；任务的（市场, 品种）下要有来源；重采样不能选。
func TestValidateCollectionTaskTagRoutesExtraFields(t *testing.T) {
	tags := map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
		"binance_swap": {TagId: "binance_swap", Attributes: map[string]string{"sync.source": "binance", "market_type": "swap"}},
	}
	service := &Service{datasetSrc: tagValidationSource{tags: tags}, fieldOffers: testFieldOffers()}
	validate := func(frequency, fields string, tagIDs ...string) error {
		return service.validateCollectionTaskTagRoutes(context.Background(), domain.CollectionTask{SpaceID: "crypto", DataType: "kline", TagIDs: tagIDs,
			CollectParams: `{"frequency":"` + frequency + `","output_fields":[` + fields + `]}`})
	}
	require.NoError(t, validate("1h", `"close","basis"`, "binance_spot"))
	require.NoError(t, validate("1h", `"close","basis","funding_rate"`, "binance_swap"))
	// 频率不在字段适用的频率里。
	require.ErrorContains(t, validate("1d", `"close","basis"`, "binance_spot"), "输出字段 basis 只支持 1h 频率")
	require.ErrorContains(t, validate("1m", `"close","funding_rate"`, "binance_swap"), "输出字段 funding_rate 只支持 1h、1d 频率")
	// 标签的市场下没有来源。
	require.ErrorContains(t, validate("1h", `"close","basis","funding_rate"`, "binance_spot"), "输出字段 funding_rate 在标签 binance_spot 的市场 spot 下没有来源")
	// 不是附加字段的输出字段不受这条规则约束。
	require.NoError(t, validate("1m", `"close","volume"`, "binance_spot"))

	// 字段只在自己的市场里是附加字段：别的空间选它不走这条规则（建列时按不认识的字段拒绝）。
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), domain.CollectionTask{SpaceID: "stockcn", DataType: "kline_resample",
		CollectParams: `{"source_dataset_id":"a","source_frequency":"1m","target_frequency":"1h","source_series_tag":"default","output_fields":["close","basis"]}`}))
	// 重采样不合成附加字段。
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), domain.CollectionTask{SpaceID: "crypto", DataType: "kline_resample",
		CollectParams: `{"source_dataset_id":"a","source_frequency":"1m","target_frequency":"1h","source_series_tag":"venue:binance","output_fields":["close","basis"]}`}),
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
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", TaskName: "swap 1h",
		DataType: "kline", CollectParams: `{"frequency":"1h","output_fields":["close","volume"]}`,
		Enabled: true, PrepareState: domain.PrepareStateReady, ResultDatasetID: "dataset-1",
	}))
	service := &Service{taskRepo: db.Tasks(), fieldOffers: testFieldOffers(), datasetSrc: validationDatasetSource{
		"dataset-1": storagesource.DatasetInfo{DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1h", SubjectTags: []string{"binance_swap"}, Attributes: map[string]string{"market_type": "swap"}},
	}}
	update := func(fields ...any) *pb.UpdateTaskRsp {
		params, structErr := structpb.NewStruct(map[string]any{"frequency": "1h", "output_fields": fields})
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

// GetDataTypeConfigs 一次返回所有任务类型的频率选项、基础输出字段与附加字段。
func TestGetDataTypeConfigsListsFrequencyAndFields(t *testing.T) {
	find := func(configs []*pb.DataTypeConfig, dataType string) *pb.DataTypeConfig {
		for _, cfg := range configs {
			if cfg.GetDataType() == dataType {
				return cfg
			}
		}
		return nil
	}
	service := &Service{fieldOffers: testFieldOffers()}
	rsp, err := service.GetDataTypeConfigs(context.Background(), &pb.GetDataTypeConfigsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	kline := find(rsp.GetConfigs(), "kline")
	require.NotNil(t, kline)
	require.NotEmpty(t, kline.GetFrequencyOptions())
	options := kline.GetExtraFields()
	require.Len(t, options, 3)
	require.Equal(t, "funding_rate", options[0].GetFieldId())
	require.Equal(t, "crypto", options[0].GetMarketId())
	require.Equal(t, "swap", options[0].GetInstrumentType())
	require.Equal(t, []string{"1h", "1d"}, options[0].GetFrequencies())
	require.Equal(t, "binance", options[0].GetProviderId())
	require.Equal(t, "Binance", options[0].GetProviderName())
	require.Equal(t, "模拟基差", options[1].GetProviderName())
	// 显示名、分组、值类型与填充策略取自字段规格。
	require.Equal(t, "资金费率", options[0].GetDisplayName())
	require.Equal(t, marketdata.GroupDerivatives, options[0].GetGroup())
	require.Equal(t, "double", options[0].GetValueType())
	require.Equal(t, "zero", options[0].GetFill())
	require.Equal(t, "double", options[1].GetValueType())
	require.Equal(t, "prev", options[1].GetFill())

	// 验证整型字段与空填充策略原样透传。
	intService := &Service{fieldOffers: marketdata.FieldOffers{
		{MarketID: "stockcn", Instrument: "equity", ProviderID: "swsresearch", Spec: marketdata.FieldSpec{
			ID: "sw_l1_code", Name: "申万一级代码", Group: marketdata.GroupIndustry, Type: marketdata.FieldTypeInt, Fill: "", Frequencies: []string{"1d"},
		}},
	}}
	intRsp, err := intService.GetDataTypeConfigs(context.Background(), &pb.GetDataTypeConfigsReq{})
	require.NoError(t, err)
	intOptions := find(intRsp.GetConfigs(), "kline").GetExtraFields()
	require.Len(t, intOptions, 1)
	require.Equal(t, "int", intOptions[0].GetValueType())
	require.Empty(t, intOptions[0].GetFill())

	// 基础输出字段与建结果数据集列是同一份定义，管理台不另抄。
	base := kline.GetBaseOutputFields()
	require.Len(t, base, len(taskresult.KlineOutputFields))
	require.Equal(t, "open", base[0].GetFieldId())
	require.Equal(t, "开盘价", base[0].GetDisplayName())
	require.Equal(t, taskresult.GroupQuote, base[0].GetGroup())
	require.Equal(t, "double", base[0].GetValueType())
	for _, field := range base {
		require.NotEmpty(t, field.GetDisplayName(), field.GetFieldId())
		require.NotEmpty(t, field.GetGroup(), field.GetFieldId())
		require.NotEmpty(t, field.GetValueType(), field.GetFieldId())
	}
	ids := make([]string, 0, len(base))
	for _, field := range base {
		ids = append(ids, field.GetFieldId())
	}
	require.Contains(t, ids, "amount_quality")
	require.NotContains(t, ids, "funding_rate", "附加字段不在基础字段里")

	resample := find(rsp.GetConfigs(), "kline_resample")
	require.NotNil(t, resample)
	require.Empty(t, resample.GetFrequencyOptions(), "重采样的目标频率由源数据集预览接口动态决定")
	require.Empty(t, resample.GetExtraFields(), "重采样没有附加字段")
	resampleBase := resample.GetBaseOutputFields()
	require.NotEmpty(t, resampleBase, "重采样必须返回可合成的基础输出字段供管理台选择")
	resampleIDs := make([]string, 0, len(resampleBase))
	for _, field := range resampleBase {
		require.NotEmpty(t, field.GetDisplayName(), field.GetFieldId())
		require.NotEmpty(t, field.GetGroup(), field.GetFieldId())
		require.NotEmpty(t, field.GetValueType(), field.GetFieldId())
		resampleIDs = append(resampleIDs, field.GetFieldId())
	}
	require.Equal(t, []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "amount"}, resampleIDs)
	require.NotContains(t, resampleIDs, "close_time", "重采样基础字段不含原始采集元信息列")
	require.NotContains(t, resampleIDs, "amount_quality")
	require.NotContains(t, resampleIDs, "funding_rate")
}

// UpdateTask 更新重采样任务时必须校验重采样参数与输出字段，防止写入 close_time 或跨市场字段把任务打进 error。
func TestUpdateTaskValidatesResampleParamsAndOutputFields(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)

	cryptoIDs := taskresult.ResultIDs("resample-crypto")
	stockIDs := taskresult.ResultIDs("resample-stock")
	cryptoCollectParams := `{"source_dataset_id":"dataset_src_crypto","source_frequency":"1m","source_series_tag":"venue:binance","target_frequency":"5m"}`
	stockCollectParams := `{"source_dataset_id":"dataset_src_stock","source_frequency":"1m","source_series_tag":"default","target_frequency":"5m"}`

	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-crypto", TaskName: "crypto 5m", DataType: "kline_resample",
		TagIDs: []string{"binance_spot"}, CollectParams: cryptoCollectParams,
		Enabled: true, PrepareState: domain.PrepareStateReady, ResultDatasetID: cryptoIDs.DatasetID,
	}))
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "resample-stock", TaskName: "stockcn 5m", DataType: "kline_resample",
		TagIDs: []string{"stockcn_all"}, CollectParams: stockCollectParams,
		Enabled: true, PrepareState: domain.PrepareStateReady, ResultDatasetID: stockIDs.DatasetID,
	}))

	metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{
		cryptoIDs.DatasetID: newTaskResultMetadataFake(cryptoIDs, "resample-crypto").datasets[cryptoIDs.DatasetID],
		stockIDs.DatasetID:  newTaskResultMetadataFake(stockIDs, "resample-stock").datasets[stockIDs.DatasetID],
	}}
	service := &Service{
		persistence:   db,
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
		datasetSrc: validationDatasetSource{
			"dataset_src_crypto": storagesource.DatasetInfo{
				DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m",
				Alignment: "epoch_utc", SubjectTags: []string{"binance_spot"}, Attributes: map[string]string{"market_type": "spot"},
				ColumnTypes: map[string]storagepb.FieldValueType{
					"open": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "high": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"low": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "close": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "quote_volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"trade_num": storagepb.FieldValueType_FIELD_VALUE_TYPE_INT,
				},
			},
			"dataset_src_stock": storagesource.DatasetInfo{
				DataSourceID: "stockcn", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m",
				Alignment: "session", SubjectTags: []string{"stockcn_all"}, Attributes: map[string]string{"market_type": "equity", "calendar": "cn_stock"},
				ColumnTypes: map[string]storagepb.FieldValueType{
					"open": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "high": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"low": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "close": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "amount": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		},
	}

	updateResample := func(spaceID, taskID, taskName, sourceDataset, seriesTag string, outputFields []any) *pb.UpdateTaskRsp {
		body := map[string]any{
			"source_dataset_id": sourceDataset, "source_frequency": "1m", "source_series_tag": seriesTag,
			"target_frequency": "5m", "output_fields": outputFields,
		}
		params, err := structpb.NewStruct(body)
		require.NoError(t, err)
		rsp, err := service.UpdateTask(ctx, &pb.UpdateTaskReq{
			SpaceId: spaceID, TaskId: taskID,
			Task: &pb.CollectionTask{SpaceId: spaceID, TaskId: taskID, TaskName: taskName, DataType: "kline_resample", CollectParams: params},
		})
		require.NoError(t, err)
		return rsp
	}

	// crypto 合成任务拒绝 close_time、A 股专属字段 amount 与 funding_rate。
	for _, bad := range []string{"close_time", "amount", "funding_rate"} {
		rsp := updateResample("crypto", "resample-crypto", "crypto 5m", "dataset_src_crypto", "venue:binance", []any{"close", bad})
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "crypto 应拒绝 %s", bad)
		require.Contains(t, rsp.GetRetInfo().GetMsg(), bad)
	}
	// stockcn 合成任务拒绝 close_time 与 crypto 专属字段 quote_volume / trade_num。
	for _, bad := range []string{"close_time", "quote_volume", "trade_num"} {
		rsp := updateResample("stockcn", "resample-stock", "stockcn 5m", "dataset_src_stock", "default", []any{"close", bad})
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "stockcn 应拒绝 %s", bad)
		require.Contains(t, rsp.GetRetInfo().GetMsg(), bad)
	}

	// 被拒绝的更新不能修改数据库记录，也不能破坏 PrepareStateReady。
	storedCrypto, err := db.Tasks().GetByTaskID(ctx, "crypto", "resample-crypto")
	require.NoError(t, err)
	require.Equal(t, domain.PrepareStateReady, storedCrypto.PrepareState)
	require.NotContains(t, storedCrypto.CollectParams, "close_time")

	// 合法缩减到当前源数据集可合成字段子集时更新成功。
	rsp := updateResample("crypto", "resample-crypto", "crypto 5m", "dataset_src_crypto", "venue:binance", []any{"open", "close", "quote_volume"})
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	updatedCrypto, err := db.Tasks().GetByTaskID(ctx, "crypto", "resample-crypto")
	require.NoError(t, err)
	require.NotContains(t, updatedCrypto.CollectParams, `"provider"`)
	require.NotContains(t, updatedCrypto.CollectParams, `"market_type"`)
	require.NotContains(t, updatedCrypto.CollectParams, `"alignment"`)

	rsp = updateResample("stockcn", "resample-stock", "stockcn 5m", "dataset_src_stock", "default", []any{"open", "close", "amount"})
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
}

// PreviewKlineResampleSource 从源数据集元数据派生 alignment/calendar_id/allowed_output_fields，
// 优先读源数据集属性中的序列标签，并支持自定义空间下的 5 列 OHLCV 数据集。
func TestPreviewKlineResampleSourceUsesDatasetMetadataAndColumns(t *testing.T) {
	ctx := context.Background()
	db := openCollectorTestStore(t)
	srcIDs := taskresult.ResultIDs("src-custom")
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "sandbox", TaskID: "src-custom", TaskName: "外部导入 5m", DataType: "kline", Ingest: domain.IngestLocal,
		TagIDs: []string{"ext_tag"}, CollectParams: `{"frequency":"5m"}`, Enabled: true, ResultDatasetID: srcIDs.DatasetID,
	}))
	service := &Service{
		persistence: db,
		taskRepo:    db.Tasks(),
		datasetSrc: validationDatasetSource{
			srcIDs.DatasetID: storagesource.DatasetInfo{
				DataSourceID: "external_csv", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "5m",
				Alignment: "session", SubjectTags: []string{"ext_tag"},
				Attributes: map[string]string{"calendar": "cn_stock", "series_tag": "ext:csv"},
				ColumnTypes: map[string]storagepb.FieldValueType{
					"open": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "high": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"low": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "close": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		},
	}

	rsp, err := service.PreviewKlineResampleSource(ctx, &pb.PreviewKlineResampleSourceReq{
		SpaceId: "sandbox", SourceTaskId: "src-custom", TargetFrequency: "15m",
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, "ext:csv", rsp.GetSourceSeriesTag())
	require.Equal(t, "session", rsp.GetAlignment())
	require.Equal(t, "cn_stock", rsp.GetCalendarId())
	require.Empty(t, rsp.GetValidationError())
	var fieldIDs []string
	for _, f := range rsp.GetAllowedOutputFields() {
		fieldIDs = append(fieldIDs, f.GetFieldId())
	}
	require.Equal(t, []string{"open", "high", "low", "close", "volume"}, fieldIDs)

	// 45m 不能整除 cn_stock 交易时段，预览报错。
	badRsp, err := service.PreviewKlineResampleSource(ctx, &pb.PreviewKlineResampleSourceReq{
		SpaceId: "sandbox", SourceTaskId: "src-custom", TargetFrequency: "45m",
	})
	require.NoError(t, err)
	require.Contains(t, badRsp.GetValidationError(), "不能整除")
}
