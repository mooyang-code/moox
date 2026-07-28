package rpc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/symbol"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	mooxreport "github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"path/filepath"
	"testing"
)

type fakeRuleDatasetSource struct {
	datasets  map[string]storagesource.DatasetInfo
	subjects  []domain.DatasetSubject
	listCalls int
}

func (f *fakeRuleDatasetSource) GetDataset(_ context.Context, _, datasetID string) (storagesource.DatasetInfo, error) {
	info, ok := f.datasets[datasetID]
	if !ok {
		return storagesource.DatasetInfo{}, errors.New("dataset not found")
	}
	return info, nil
}

func (f *fakeRuleDatasetSource) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	f.listCalls++
	return append([]domain.DatasetSubject(nil), f.subjects...), nil
}

func newCollectorRPCService(t *testing.T) *Service {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	return New(mgr, Dependencies{})
}

func validCollectParams(t *testing.T) *structpb.Struct {
	t.Helper()
	st, err := structpb.NewStruct(map[string]any{
		"source":    map[string]any{"kind": "none"},
		"collector": map[string]any{"exchange": "binance", "market": "spot", "data_type": "symbol"},
		"target":    map[string]any{"dataset_id": "ds-1"},
		"schedule":  map[string]any{"interval": "1h"},
	})
	require.NoError(t, err)
	return st
}

func validKlineCollectParams(t *testing.T) *structpb.Struct {
	t.Helper()
	st, err := structpb.NewStruct(map[string]any{
		"source": map[string]any{"kind": "dataset_subjects", "dataset_id": "symbols"},
		"collector": map[string]any{
			"exchange":  "binance",
			"market":    "spot",
			"data_type": "kline",
			"intervals": []any{"1m"},
		},
		"target":   map[string]any{"dataset_id": "kline_1m"},
		"schedule": map[string]any{"interval": "1m"},
	})
	require.NoError(t, err)
	return st
}

func TestCollectorService_TaskRuleCRUD(t *testing.T) {
	svc := newCollectorRPCService(t)
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"ds-1": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_RECORD,
			Status:       "active",
		},
	}}
	ctx := context.Background()

	listRsp, err := svc.GetTaskRuleList(ctx, &pb.GetTaskRuleListReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, listRsp.GetRetInfo().GetCode())

	createRsp, err := svc.CreateTaskRule(ctx, &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "rule-1", DataType: "symbol", Exchange: "binance",
		CollectParams: validCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	assert.Equal(t, "rule-1", createRsp.GetRuleId())

	listRsp, err = svc.GetTaskRuleList(ctx, &pb.GetTaskRuleListReq{SpaceId: "crypto"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, listRsp.GetRetInfo().GetCode())
	assert.Len(t, listRsp.GetRules(), 1)

	detailRsp, err := svc.GetTaskRuleDetail(ctx, &pb.GetTaskRuleDetailReq{SpaceId: "crypto", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, detailRsp.GetRetInfo().GetCode())
	assert.Equal(t, "rule-1", detailRsp.GetRule().GetRuleId())

	updateRsp, err := svc.UpdateTaskRule(ctx, &pb.UpdateTaskRuleReq{
		SpaceId: "crypto", RuleId: "rule-1", Rule: &pb.TaskRule{
			SpaceId: "crypto", RuleId: "rule-1", DataType: "symbol", Exchange: "binance",
			CollectParams: validCollectParams(t), Creator: "updated",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
	assert.Equal(t, "updated", updateRsp.GetRule().GetCreator())

	disableRsp, err := svc.DisableTaskRule(ctx, &pb.DisableTaskRuleReq{SpaceId: "crypto", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, disableRsp.GetRetInfo().GetCode())
}

func TestCollectorServiceRejectsDatasetThatDoesNotMatchCollector(t *testing.T) {
	svc := newCollectorRPCService(t)
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"ds-1": {
			DataSourceID: "okx",
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Status:       "active",
		},
	}}
	rsp, err := svc.CreateTaskRule(context.Background(), &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "rule-invalid-dataset", DataType: "symbol", Exchange: "binance",
		CollectParams: validCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "data_source_id")
}

func TestCreateKlineRuleAcceptsRecordSymbolSourceAndTimeSeriesTarget(t *testing.T) {
	svc := newCollectorRPCService(t)
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"symbols": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_RECORD,
			Status:       "active",
		},
		"kline_1m": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Status:       "active",
		},
	}}

	rsp, err := svc.CreateTaskRule(context.Background(), &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "kline-record-source", DataType: "kline", Exchange: "binance",
		CollectParams: validKlineCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
}

func TestCreateKlineRuleRejectsTimeSeriesSource(t *testing.T) {
	svc := newCollectorRPCService(t)
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"symbols": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Status:       "active",
		},
		"kline_1m": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Status:       "active",
		},
	}}

	rsp, err := svc.CreateTaskRule(context.Background(), &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "kline-timeseries-source", DataType: "kline", Exchange: "binance",
		CollectParams: validKlineCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "source Dataset symbols data_kind")
}

func TestCreateKlineRuleRejectsRecordTarget(t *testing.T) {
	svc := newCollectorRPCService(t)
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"symbols": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_RECORD,
			Status:       "active",
		},
		"kline_1m": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_RECORD,
			Status:       "active",
		},
	}}

	rsp, err := svc.CreateTaskRule(context.Background(), &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "kline-record-target", DataType: "kline", Exchange: "binance",
		CollectParams: validKlineCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "target Dataset kline_1m data_kind")
}

func TestCreateSymbolRuleRequiresRecordTarget(t *testing.T) {
	svc := newCollectorRPCService(t)
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"ds-1": {
			DataSourceID: "binance",
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Status:       "active",
		},
	}}

	rsp, err := svc.CreateTaskRule(context.Background(), &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "symbol-timeseries-target", DataType: "symbol", Exchange: "binance",
		CollectParams: validCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "target Dataset ds-1 data_kind")
}

func TestValidateTaskRuleRejectsUnsupportedOrLegacyContracts(t *testing.T) {
	base := domain.TaskRule{
		SpaceID: "crypto", RuleID: "rule-1", Exchange: "binance", DataType: "kline",
		CollectParams: `{
			"source":{"kind":"dataset_subjects","dataset_id":"kline"},
			"collector":{"exchange":"binance","market":"spot","data_type":"kline","intervals":["1m"]},
			"target":{"dataset_id":"kline"},
			"schedule":{"interval":"1m"}
		}`,
	}
	require.NoError(t, validateTaskRule(base))

	unsupported := base
	unsupported.Exchange = "okx"
	unsupported.CollectParams = strings.ReplaceAll(base.CollectParams, "binance", "okx")
	require.ErrorContains(t, validateTaskRule(unsupported), "unsupported collector")

	legacy := base
	legacy.CollectParams = strings.Replace(
		base.CollectParams,
		`"target":{"dataset_id":"kline"}`,
		`"target":{"dataset_id":"kline","job_type":"collect.kline"}`,
		1,
	)
	require.ErrorContains(t, validateTaskRule(legacy), "unknown field")
}

func TestCollectorService_GetDataTypeConfigs(t *testing.T) {
	svc := newCollectorRPCService(t)
	rsp, err := svc.GetDataTypeConfigs(context.Background(), &pb.GetDataTypeConfigsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetConfigs())
}

func TestCollectorService_ReportTaskStatusValidatesInput(t *testing.T) {
	svc := newCollectorRPCService(t)
	for _, test := range []struct {
		name string
		req  *pb.ReportInstanceStatusReq
	}{
		{name: "space", req: &pb.ReportInstanceStatusReq{}},
		{name: "task", req: &pb.ReportInstanceStatusReq{SpaceId: "crypto"}},
		{name: "job item", req: &pb.ReportInstanceStatusReq{SpaceId: "crypto", TaskId: "task-1"}},
		{name: "status", req: &pb.ReportInstanceStatusReq{SpaceId: "crypto", TaskId: "task-1", JobItemId: "item-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rsp, err := svc.ReportTaskStatus(context.Background(), test.req)
			require.NoError(t, err)
			assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
		})
	}
}

func TestCollectorService_GetTaskInstanceListRequiresSpaceID(t *testing.T) {
	svc := newCollectorRPCService(t)
	rsp, err := svc.GetTaskInstanceList(context.Background(), &pb.GetTaskInstanceListReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCollectorService_ScheduleTasksRequiresSpaceID(t *testing.T) {
	svc := newCollectorRPCService(t)
	rsp, err := svc.ScheduleTasks(context.Background(), &pb.ScheduleTasksReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestScheduleTasksSkipsRuleBeforeDatasetScanWhenNotDue(t *testing.T) {
	svc := newCollectorRPCService(t)
	require.NoError(t, svc.ruleRepo.Create(context.Background(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "hourly-kline", Exchange: "binance", DataType: "kline",
		CollectParams: `{
			"source":{"kind":"dataset_subjects","dataset_id":"symbols"},
			"collector":{"exchange":"binance","market":"spot","data_type":"kline","intervals":["1m"]},
			"target":{"dataset_id":"klines"},
			"schedule":{"interval":"1h"}
		}`,
		Enabled: true,
	}))
	datasetSource := &fakeRuleDatasetSource{subjects: []domain.DatasetSubject{{
		SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active",
	}}}
	svc.datasetSrc = datasetSource
	svc.now = func() time.Time {
		return time.Date(2026, 7, 27, 12, 34, 20, 0, time.UTC)
	}

	rsp, err := svc.ScheduleTasks(context.Background(), &pb.ScheduleTasksReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.Zero(t, datasetSource.listCalls)
	instances, total, err := svc.instanceRepo.List(context.Background(), store.TaskInstanceFilter{SpaceID: "crypto"})
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, instances)
}

func TestScheduleTasksBuildsHourlyRuleOnlyAtPreviousMinuteWithOneExecuteAt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.TaskRules().Create(context.Background(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "hourly-kline", Exchange: "binance", DataType: "kline",
		CollectParams: `{
			"source":{"kind":"dataset_subjects","dataset_id":"symbols"},
			"collector":{"exchange":"binance","market":"spot","data_type":"kline","intervals":["1m"]},
			"target":{"dataset_id":"klines"},
			"schedule":{"interval":"1h"}
		}`,
		Enabled: true,
	}))

	var submitted []*cloudnodepb.JobItem
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/cloudnode/SubmitJobItems", r.URL.Path)
		var req cloudnodepb.SubmitJobItemsReq
		raw, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		require.NoError(t, protojson.Unmarshal(raw, &req))
		submitted = append(submitted, req.GetItems()...)
		acks := make([]*cloudnodepb.JobItemAck, 0, len(req.GetItems()))
		for _, item := range req.GetItems() {
			acks = append(acks, &cloudnodepb.JobItemAck{
				JobItemId: item.GetJobItemId(),
				Status:    cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
			})
		}
		body, marshalErr := protojson.Marshal(&cloudnodepb.SubmitJobItemsRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Acks:    acks,
		})
		require.NoError(t, marshalErr)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	svc := New(mgr, Dependencies{
		AdminGatewayURL: server.URL,
		ServiceAuth:     taskpublisher.AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway"},
	})
	datasetSource := &fakeRuleDatasetSource{subjects: []domain.DatasetSubject{
		{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
		{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT", Status: "active"},
	}}
	svc.datasetSrc = datasetSource
	currentNow := time.Date(2026, 7, 27, 12, 58, 20, 0, time.UTC)
	svc.now = func() time.Time { return currentNow }

	rsp, err := svc.ScheduleTasks(context.Background(), &pb.ScheduleTasksReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.Zero(t, datasetSource.listCalls)
	assert.Empty(t, submitted)

	currentNow = time.Date(2026, 7, 27, 12, 59, 20, 0, time.UTC)
	rsp, err = svc.ScheduleTasks(context.Background(), &pb.ScheduleTasksReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.Equal(t, 1, datasetSource.listCalls)
	require.Len(t, submitted, 2)
	for _, item := range submitted {
		require.NotNil(t, item.GetExecuteAt())
		assert.Equal(t, time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC), item.GetExecuteAt().AsTime())
	}
}

func TestCollectorService_SchedulePrebindsStableJobIDsBeforePublishingWithoutWake(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })

	for _, ruleID := range []string{"rule-a", "rule-b"} {
		require.NoError(t, mgr.TaskRules().Create(context.Background(), domain.TaskRule{
			SpaceID: "crypto", RuleID: ruleID, Exchange: "binance", DataType: "symbol",
			CollectParams: `{
				"source":{"kind":"none"},
				"collector":{"exchange":"binance","market":"spot","data_type":"symbol"},
				"target":{"dataset_id":"symbols"},
				"schedule":{"interval":"1m"}
			}`,
			Enabled: true,
		}))
	}

	var observedMu sync.Mutex
	observedIDs := map[string][]string{}
	observedExecuteAt := []time.Time{}
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedMu.Lock()
		paths = append(paths, r.URL.Path)
		observedMu.Unlock()
		if r.URL.Path == "/api/service/cloudnode/GetJobItem" {
			body, marshalErr := protojson.Marshal(&cloudnodepb.GetJobItemRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Item: &cloudnodepb.JobItemDetail{
					SpaceId: "crypto",
					Status:  cloudnodepb.JobItemStatus_JOB_ITEM_STATUS_PENDING,
				},
			})
			require.NoError(t, marshalErr)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		if r.URL.Path != "/api/service/cloudnode/SubmitJobItems" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		raw, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, readErr.Error(), http.StatusBadRequest)
			return
		}
		var req cloudnodepb.SubmitJobItemsReq
		if unmarshalErr := protojson.Unmarshal(raw, &req); unmarshalErr != nil {
			http.Error(w, unmarshalErr.Error(), http.StatusBadRequest)
			return
		}
		acks := make([]*cloudnodepb.JobItemAck, 0, len(req.GetItems()))
		for _, item := range req.GetItems() {
			taskID := item.GetParams().GetFields()["task_id"].GetStringValue()
			persisted, _, listErr := mgr.TaskInstances().List(context.Background(), store.TaskInstanceFilter{
				SpaceID: "crypto", TaskID: taskID,
			})
			if listErr != nil || len(persisted) != 1 || persisted[0].CloudJobItemID != item.GetJobItemId() {
				http.Error(w, "job item id was not persisted before publish", http.StatusConflict)
				return
			}
			if item.GetExecuteAt() == nil {
				http.Error(w, "execute_at is required", http.StatusBadRequest)
				return
			}
			observedMu.Lock()
			observedIDs[taskID] = append(observedIDs[taskID], item.GetJobItemId())
			observedExecuteAt = append(observedExecuteAt, item.GetExecuteAt().AsTime())
			observedMu.Unlock()
			acks = append(acks, &cloudnodepb.JobItemAck{
				JobItemId: item.GetJobItemId(),
				Status:    cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
			})
		}
		body, marshalErr := protojson.Marshal(&cloudnodepb.SubmitJobItemsRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Acks:    acks,
		})
		if marshalErr != nil {
			http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	svc := New(mgr, Dependencies{
		AdminGatewayURL: server.URL,
		ServiceAuth:     taskpublisher.AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway"},
	})
	currentNow := time.Date(2026, 7, 26, 10, 17, 20, 0, time.UTC)
	nowCalls := 0
	svc.now = func() time.Time {
		nowCalls++
		return currentNow
	}

	for _, invocationNow := range []time.Time{
		currentNow,
		time.Date(2026, 7, 26, 10, 17, 50, 0, time.UTC),
		time.Date(2026, 7, 26, 10, 18, 20, 0, time.UTC),
	} {
		currentNow = invocationNow
		rsp, scheduleErr := svc.ScheduleTasks(context.Background(), &pb.ScheduleTasksReq{SpaceId: "crypto"})
		require.NoError(t, scheduleErr)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}

	assert.Equal(t, 3, nowCalls, "Schedule must capture now once per invocation")
	observedMu.Lock()
	defer observedMu.Unlock()
	require.Len(t, observedExecuteAt, 6)
	for _, executeAt := range observedExecuteAt[:4] {
		assert.Equal(t, time.Date(2026, 7, 26, 10, 18, 0, 0, time.UTC), executeAt)
	}
	for _, executeAt := range observedExecuteAt[4:] {
		assert.Equal(t, time.Date(2026, 7, 26, 10, 19, 0, 0, time.UTC), executeAt)
	}
	for taskID, ids := range observedIDs {
		require.Len(t, ids, 3, taskID)
		assert.Equal(t, ids[0], ids[1], taskID)
		assert.NotEqual(t, ids[1], ids[2], taskID)
		assert.Contains(t, ids[2], "2026-07-26T10:19:00Z")
	}
	for _, path := range paths {
		assert.False(t, strings.Contains(path, "GetNodeList"), paths)
		assert.False(t, strings.Contains(path, "InvokeFunction"), paths)
	}
}

func TestCollectorService_ReconcilesPendingInstanceOnlyFromCloudNodeTerminalState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })

	current := domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1",
		Exchange: "binance", Market: "spot", DataType: "symbol",
		TaskParams: `{}`, CloudJobItemID: "task-1:2026-07-27T10:30:00Z",
		LastExecStatus: domain.InstanceStatusPending,
	}
	_, err = mgr.TaskInstances().ReserveMany(context.Background(), []domain.TaskInstance{current})
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/service/cloudnode/GetJobItem", r.URL.Path)
		raw, marshalErr := protojson.Marshal(&cloudnodepb.GetJobItemRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Item: &cloudnodepb.JobItemDetail{
				SpaceId: "crypto", JobItemId: current.CloudJobItemID,
				Status:        cloudnodepb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS,
				ResultSummary: mustStructPB(t, map[string]any{"rows": float64(2)}),
				ExecutionNode: "scf-a",
			},
		})
		require.NoError(t, marshalErr)
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	svc := New(mgr, Dependencies{
		AdminGatewayURL: server.URL,
		ServiceAuth: taskpublisher.AuthConfig{
			AccessKey: "ak", SecretKey: "sk", TargetNode: "control",
		},
	})
	next := current
	next.CloudJobItemID = "task-1:2026-07-27T11:00:00Z"
	require.NoError(t, svc.reconcilePendingInstances(context.Background(), []domain.TaskInstance{next}))

	reserved, err := mgr.TaskInstances().ReserveMany(context.Background(), []domain.TaskInstance{next})
	require.NoError(t, err)
	require.Len(t, reserved, 1)
	assert.Equal(t, next.CloudJobItemID, reserved[0].CloudJobItemID)
}

func TestNormalizeAndValidateTaskRule(t *testing.T) {
	rule := normalizeTaskRule(domain.TaskRule{SpaceID: "crypto", DataType: "symbol", Exchange: "binance"})
	assert.NotEmpty(t, rule.RuleID)
	assert.Equal(t, "{}", rule.CollectParams)

	err := validateTaskRule(domain.TaskRule{SpaceID: "", DataType: "symbol", Exchange: "binance", RuleID: "r1"})
	assert.Error(t, err)
}

func mustStructPB(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	value, err := structpb.NewStruct(values)
	require.NoError(t, err)
	return value
}

func TestCollectorService_TaskInstanceFlow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Exchange: "binance",
		DataType: "symbol", Market: "spot", TaskParams: `{}`, CloudJobItemID: "item-new",
		LastExecStatus: domain.InstanceStatusPending,
	}}))

	svc := New(mgr, Dependencies{})
	ctx := context.Background()

	listRsp, err := svc.GetTaskInstanceList(ctx, &pb.GetTaskInstanceListReq{
		Filter: &pb.TaskInstanceFilter{SpaceId: "crypto", Exchange: "binance"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, listRsp.GetRetInfo().GetCode())
	assert.Len(t, listRsp.GetInstances(), 1)

	reportRsp, err := svc.ReportTaskStatus(ctx, &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-1", JobItemId: "item-new", NodeId: "node-new",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
		Result: mustStruct(t, map[string]any{"state": "new"}),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, reportRsp.GetRetInfo().GetCode())

	instances, _, err := mgr.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "node-new", instances[0].LastExecNode)
	assert.Equal(t, domain.InstanceStatusSuccess, instances[0].LastExecStatus)
	assert.JSONEq(t, `{"state":"new"}`, instances[0].Result)

	reportRsp, err = svc.ReportTaskStatus(ctx, &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-1", JobItemId: "item-old", NodeId: "node-old",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_FAILED,
		Result: mustStruct(t, map[string]any{"state": "old"}),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, reportRsp.GetRetInfo().GetCode())

	instances, _, err = mgr.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto", TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, instances, 1)
	assert.Equal(t, "node-new", instances[0].LastExecNode)
	assert.Equal(t, domain.InstanceStatusSuccess, instances[0].LastExecStatus)
	assert.JSONEq(t, `{"state":"new"}`, instances[0].Result)

	missingRsp, err := svc.ReportTaskStatus(ctx, &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "missing", JobItemId: "item-missing",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, missingRsp.GetRetInfo().GetCode())
}

type recordingDatasetObserver struct {
	observations []mooxreport.DatasetObservation
}

type inventoryStub struct {
	dirty     int
	refreshes int
	err       error
}

func (s *inventoryStub) MarkDirty() { s.dirty++ }
func (s *inventoryStub) Refresh(context.Context) error {
	s.refreshes++
	return s.err
}

func TestTaskRuleMutationRefreshFailureDoesNotRollback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	inventory := &inventoryStub{err: errors.New("inventory unavailable")}
	svc := New(mgr, Dependencies{RealtimeInventory: inventory})
	svc.datasetSrc = &fakeRuleDatasetSource{datasets: map[string]storagesource.DatasetInfo{
		"ds-1": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active"},
	}}

	rsp, err := svc.CreateTaskRule(context.Background(), &pb.CreateTaskRuleReq{Rule: &pb.TaskRule{
		SpaceId: "crypto", RuleId: "inventory-rule", DataType: "symbol", Exchange: "binance",
		CollectParams: validCollectParams(t), Enabled: boolPtr(true),
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	_, err = mgr.TaskRules().GetByRuleID(context.Background(), "crypto", "inventory-rule")
	require.NoError(t, err)
	require.Equal(t, 1, inventory.dirty)
	require.Equal(t, 1, inventory.refreshes)
}

func (o *recordingDatasetObserver) ObserveRun(observation mooxreport.DatasetObservation) error {
	o.observations = append(o.observations, observation)
	return nil
}

func TestReportTaskStatusObservesOnlyAcceptedCurrentJobItem(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-1", CloudJobItemID: "item-current",
		DataType: "kline", DatasetID: "market_kline", Interval: "1m",
		LastExecStatus: domain.InstanceStatusPending,
	}}))
	observer := &recordingDatasetObserver{}
	svc := New(mgr, Dependencies{DatasetMetrics: observer})
	watermark := "2026-07-28T01:02:03Z"

	current, err := svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-1", JobItemId: "item-current", NodeId: "scf-1",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
		Result: mustStruct(t, map[string]any{"tasks": []any{map[string]any{
			"data_type": "kline", "dataset_id": "market_kline", "freq": "1m",
			"rows_written": 2, "output_watermark": watermark,
		}}}),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, current.GetRetInfo().GetCode())
	require.Len(t, observer.observations, 1)
	assert.Equal(t, "success", observer.observations[0].Result)
	assert.Equal(t, uint64(2), observer.observations[0].Rows)
	assert.Equal(t, watermark, observer.observations[0].OutputWatermark.Format(time.RFC3339))

	duplicate, err := svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-1", JobItemId: "item-current", NodeId: "scf-duplicate",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
		Result: mustStruct(t, map[string]any{"tasks": []any{map[string]any{
			"data_type": "kline", "dataset_id": "market_kline", "freq": "1m",
			"rows_written": 50, "output_watermark": "2026-07-30T00:00:00Z",
		}}}),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, duplicate.GetRetInfo().GetCode())
	assert.Len(t, observer.observations, 1)

	stale, err := svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-1", JobItemId: "item-stale", NodeId: "scf-old",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
		Result: mustStruct(t, map[string]any{"tasks": []any{map[string]any{
			"data_type": "kline", "dataset_id": "market_kline", "freq": "1m",
			"rows_written": 100, "output_watermark": "2026-07-29T00:00:00Z",
		}}}),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, stale.GetRetInfo().GetCode())
	assert.Len(t, observer.observations, 1)
}

func TestReportTaskStatusDerivesEmptyFromZeroRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-empty", CloudJobItemID: "item-empty",
		DataType: "kline", DatasetID: "market_kline", Interval: "1m",
		LastExecStatus: domain.InstanceStatusPending,
	}}))
	observer := &recordingDatasetObserver{}
	svc := New(mgr, Dependencies{DatasetMetrics: observer})

	_, err = svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-empty", JobItemId: "item-empty",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
		Result: mustStruct(t, map[string]any{"tasks": []any{map[string]any{
			"data_type": "kline", "dataset_id": "market_kline", "freq": "1m", "rows_written": 0,
		}}}),
	})
	require.NoError(t, err)
	require.Len(t, observer.observations, 1)
	assert.Equal(t, "empty", observer.observations[0].Result)
	assert.True(t, observer.observations[0].OutputWatermark.IsZero())
}

func TestReportTaskStatusObservesAcceptedFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-failed", CloudJobItemID: "item-failed",
		DataType: "kline", DatasetID: "market_kline", Interval: "1m",
		LastExecStatus: domain.InstanceStatusPending,
	}}))
	observer := &recordingDatasetObserver{}
	svc := New(mgr, Dependencies{DatasetMetrics: observer})

	_, err = svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "task-failed", JobItemId: "item-failed",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_FAILED,
		Result: mustStruct(t, map[string]any{
			"error_code": "COLLECTION_FAILED", "error_summary": "upstream timeout",
		}),
	})
	require.NoError(t, err)
	require.Len(t, observer.observations, 1)
	assert.Equal(t, "error", observer.observations[0].Result)
	assert.Equal(t, mooxreport.DatasetKey{
		SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m",
	}, observer.observations[0].Key)
	assert.True(t, observer.observations[0].OutputWatermark.IsZero())
}

func boolPtr(v bool) *bool { return &v }

func mustStruct(t *testing.T, value map[string]any) *structpb.Struct {
	t.Helper()
	result, err := structpb.NewStruct(value)
	require.NoError(t, err)
	return result
}

func TestGetDataTypeConfigWithFieldsNormalizesDataType(t *testing.T) {
	svc := &Service{}

	rsp, err := svc.GetDataTypeConfigWithFields(context.Background(), &pb.GetDataTypeConfigWithFieldsReq{
		DataType: " KLINE ",
	})
	if err != nil {
		t.Fatalf("GetDataTypeConfigWithFields() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret code = %v, want SUCCESS, msg=%s", rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetDetail().GetConfig().GetDataType() != "kline" {
		t.Fatalf("data_type = %q, want kline", rsp.GetDetail().GetConfig().GetDataType())
	}
	if len(rsp.GetDetail().GetFields()) != 2 {
		t.Fatalf("len(fields) = %d, want 2", len(rsp.GetDetail().GetFields()))
	}
}

func TestDataTypeConfigFromDefinition(t *testing.T) {
	def := symbol.NewJobDefinition()
	cfg := dataTypeConfigFromDefinition(def)
	assert.Equal(t, "symbol", cfg.GetDataType())
	assert.Equal(t, "标的", cfg.GetTypeName())
	fields := dataTypeFieldsFromDefinition(def)
	require.NotEmpty(t, fields)
	assert.NotEmpty(t, fields[0].GetFieldName())
}

func TestStructFromAnyAndValueFromAny(t *testing.T) {
	st := structFromAny(map[string]any{"k": "v"})
	assert.Equal(t, "v", st.GetFields()["k"].GetStringValue())
	assert.Empty(t, structFromAny(make(chan int)).GetFields())

	val := valueFromAny("hello")
	assert.Equal(t, "hello", val.GetStringValue())
	assert.Equal(t, "", valueFromAny(make(chan int)).GetStringValue())
}

func TestJobsListJobDefinitionsNotEmpty(t *testing.T) {
	assert.NotEmpty(t, jobs.ListJobDefinitions())
}
