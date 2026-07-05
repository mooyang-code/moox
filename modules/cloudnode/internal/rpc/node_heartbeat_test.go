package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestReportHeartbeatEnqueuesHeartbeatSink(t *testing.T) {
	sink := &fakeHeartbeatSink{}
	svc := &Service{heartbeatSink: sink}

	rsp, err := svc.ReportHeartbeat(context.Background(), &pb.ReportHeartbeatReq{
		SpaceId:        "crypto",
		NodeId:         "node-1",
		NodeType:       "scf-event",
		RunningVersion: "v1",
	})
	if err != nil {
		t.Fatalf("ReportHeartbeat transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	if len(sink.items) != 1 || sink.items[0].GetNodeId() != "node-1" {
		t.Fatalf("sink items = %+v", sink.items)
	}
}

func TestReportHeartbeatReturnsCancelDirective(t *testing.T) {
	ctx := context.Background()
	db := newNodeSCFTestDB(t)
	repo := projection.NewRepository(db, projection.RepositoryOptions{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if _, err := repo.CreatePending(ctx, &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-directive",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}, projection.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := repo.TryMarkRunning(ctx, projection.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-directive",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.directive",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	if err := repo.MarkCanceled(ctx, "crypto", "ji-directive", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	svc := &Service{projectionRepo: repo, heartbeatSink: &fakeHeartbeatSink{}}

	rsp, err := svc.ReportHeartbeat(ctx, &pb.ReportHeartbeatReq{
		SpaceId: "crypto",
		NodeId:  "node-1",
	})
	if err != nil {
		t.Fatalf("ReportHeartbeat transport error = %v", err)
	}
	if len(rsp.GetDirectives()) != 1 || rsp.GetDirectives()[0].GetType() != pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL {
		t.Fatalf("directives = %+v", rsp.GetDirectives())
	}
	if rsp.GetDirectives()[0].GetJobItemId() != "ji-directive" || rsp.GetDirectives()[0].GetAttemptNo() != 1 {
		t.Fatalf("directive = %+v", rsp.GetDirectives()[0])
	}
}

type fakeHeartbeatSink struct {
	items []*pb.ReportHeartbeatReq
}

func (s *fakeHeartbeatSink) Enqueue(req *pb.ReportHeartbeatReq) error {
	s.items = append(s.items, req)
	return nil
}

func (s *fakeHeartbeatSink) Flush(context.Context) error { return nil }

func (s *fakeHeartbeatSink) Close(context.Context) error { return nil }
