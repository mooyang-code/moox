package rpc

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"gorm.io/gorm"
)

func TestServiceRequiresSpaceIDForListRPCs(t *testing.T) {
	svc := New(newTestDB(t), Dependencies{})
	ctx := context.Background()

	ruleRsp, err := svc.GetTaskRuleList(ctx, &pb.GetTaskRuleListReq{})
	if err != nil {
		t.Fatalf("GetTaskRuleList returned error: %v", err)
	}
	requireRetCode(t, ruleRsp.GetRetInfo(), pb.ErrorCode_INVALID_PARAM)

	instanceRsp, err := svc.GetTaskInstanceList(ctx, &pb.GetTaskInstanceListReq{
		Filter: &pb.TaskInstanceFilter{},
	})
	if err != nil {
		t.Fatalf("GetTaskInstanceList returned error: %v", err)
	}
	requireRetCode(t, instanceRsp.GetRetInfo(), pb.ErrorCode_INVALID_PARAM)
}

func TestReportTaskStatusReturnsNotFoundForMissingTask(t *testing.T) {
	svc := New(newTestDB(t), Dependencies{})

	rsp, err := svc.ReportTaskStatus(context.Background(), &pb.ReportInstanceStatusReq{
		SpaceId: "crypto",
		TaskId:  "missing-task",
		NodeId:  "node-a",
		Status:  pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS,
	})
	if err != nil {
		t.Fatalf("ReportTaskStatus returned error: %v", err)
	}
	requireRetCode(t, rsp.GetRetInfo(), pb.ErrorCode_NOT_FOUND)
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&domain.TaskRule{}, &domain.TaskInstance{}, &repository.ExecutionLog{}); err != nil {
		t.Fatalf("migrate collector tables: %v", err)
	}
	return db
}

func requireRetCode(t *testing.T, ret *pb.RetInfo, want pb.ErrorCode) {
	t.Helper()
	if ret == nil {
		t.Fatalf("ret_info is nil, want code %s", want)
	}
	if ret.GetCode() != want {
		t.Fatalf("ret_info.code = %s, want %s; msg=%q", ret.GetCode(), want, ret.GetMsg())
	}
}
