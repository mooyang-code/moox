package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

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
		"target":    map[string]any{"dataset_id": "ds-1", "job_type": "collect.symbol"},
		"schedule":  map[string]any{"interval": "1h"},
	})
	require.NoError(t, err)
	return st
}

func TestCollectorService_TaskRuleCRUD(t *testing.T) {
	svc := newCollectorRPCService(t)
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
			CollectParams: validCollectParams(t), NodePattern: "node-*",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, updateRsp.GetRetInfo().GetCode())
	assert.Equal(t, "node-*", updateRsp.GetRule().GetNodePattern())

	disableRsp, err := svc.DisableTaskRule(ctx, &pb.DisableTaskRuleReq{SpaceId: "crypto", RuleId: "rule-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, disableRsp.GetRetInfo().GetCode())
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
	rsp, err := svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCollectorService_GetTaskInstanceListRequiresSpaceID(t *testing.T) {
	svc := newCollectorRPCService(t)
	rsp, err := svc.GetTaskInstanceList(context.Background(), &pb.GetTaskInstanceListReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCollectorService_RecalculateRequiresSpaceID(t *testing.T) {
	svc := newCollectorRPCService(t)
	rsp, err := svc.RecalculateAllTaskInstances(context.Background(), &pb.RecalculateAllTaskInstancesReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestNormalizeAndValidateTaskRule(t *testing.T) {
	rule := normalizeTaskRule(domain.TaskRule{SpaceID: "crypto", DataType: "symbol", Exchange: "binance"})
	assert.NotEmpty(t, rule.RuleID)
	assert.Equal(t, "auto", rule.AssignmentType)
	assert.Equal(t, "{}", rule.CollectParams)

	err := validateTaskRule(domain.TaskRule{SpaceID: "", DataType: "symbol", Exchange: "binance", RuleID: "r1"})
	assert.Error(t, err)
}

func TestCollectorService_TaskInstanceFlow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Exchange: "binance",
		DataType: "symbol", Market: "spot", TaskParams: `{}`,
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
		SpaceId: "crypto", TaskId: "task-1", NodeId: "node-a",
		Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, reportRsp.GetRetInfo().GetCode())

	missingRsp, err := svc.ReportTaskStatus(ctx, &pb.ReportInstanceStatusReq{
		SpaceId: "crypto", TaskId: "missing", Status: pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, missingRsp.GetRetInfo().GetCode())
}

func boolPtr(v bool) *bool { return &v }
