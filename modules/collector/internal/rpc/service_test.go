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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"path/filepath"
	"testing"
)

type fakeRuleDatasetSource struct {
	datasets map[string]storagesource.DatasetInfo
}

func (f *fakeRuleDatasetSource) GetDataset(_ context.Context, _, datasetID string) (storagesource.DatasetInfo, error) {
	info, ok := f.datasets[datasetID]
	if !ok {
		return storagesource.DatasetInfo{}, errors.New("dataset not found")
	}
	return info, nil
}

func (*fakeRuleDatasetSource) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	return nil, nil
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
				"schedule":{"interval":"30m"}
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
	currentNow := time.Date(2026, 7, 26, 10, 17, 42, 0, time.UTC)
	nowCalls := 0
	svc.now = func() time.Time {
		nowCalls++
		return currentNow
	}

	for _, invocationNow := range []time.Time{
		currentNow,
		time.Date(2026, 7, 26, 10, 25, 0, 0, time.UTC),
	} {
		currentNow = invocationNow
		rsp, scheduleErr := svc.ScheduleTasks(context.Background(), &pb.ScheduleTasksReq{SpaceId: "crypto"})
		require.NoError(t, scheduleErr)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}

	assert.Equal(t, 2, nowCalls, "Schedule must capture now once per invocation")
	observedMu.Lock()
	defer observedMu.Unlock()
	require.Len(t, observedExecuteAt, 4)
	for _, executeAt := range observedExecuteAt {
		assert.Equal(t, time.Date(2026, 7, 26, 10, 30, 0, 0, time.UTC), executeAt)
	}
	for taskID, ids := range observedIDs {
		require.Len(t, ids, 2, taskID)
		assert.Equal(t, ids[0], ids[1], taskID)
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
