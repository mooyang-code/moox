package rpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestPollJobItemsRequiresSupportedProtocolVersion(t *testing.T) {
	svc := &Service{}
	for _, protocolVersion := range []string{"", "unknown"} {
		rsp, err := svc.PollJobItems(context.Background(), &pb.PollJobItemsReq{
			SpaceId:           "space-a",
			NodeId:            "node-a",
			SupportedJobTypes: []string{"collect.kline"},
			ProtocolVersion:   protocolVersion,
		})
		if err != nil {
			t.Fatalf("PollJobItems() transport error = %v", err)
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
			t.Fatalf("protocol_version %q ret code = %v, want INVALID_PARAM", protocolVersion, rsp.GetRetInfo().GetCode())
		}
	}
}

func TestJobItemRPCUsesJetStreamAndProjection(t *testing.T) {
	ctx := context.Background()
	db := newNodeSCFTestDB(t)
	catalog := repository.NewCatalogRepository(db)
	if err := catalog.UpsertNode(ctx, repository.CloudNode{
		SpaceID:   "crypto",
		NodeID:    "node-1",
		PackageID: "collector-scf",
		NodeType:  "scf-event",
		Status:    "online",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	rt, jsCfg := startRPCQueueRuntime(t)
	projectionRepo := projection.NewRepository(db, projection.RepositoryOptions{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	queue := jobqueue.NewJetStreamQueue(rt, jobqueue.QueueConfig{
		Naming:          jobqueue.NamingConfig{SubjectPrefix: jobqueue.DefaultSubjectPrefix},
		ExecStream:      jsCfg.ExecStream,
		AckWait:         200 * time.Millisecond,
		MaxDeliver:      3,
		FetchMaxWait:    500 * time.Millisecond,
		DefaultMaxBatch: 10,
	})
	projector := projection.NewProjector(rt.JetStream(), projectionRepo, projection.ProjectorOptions{
		Naming:           jobqueue.NamingConfig{SubjectPrefix: jobqueue.DefaultSubjectPrefix},
		ProjectionStream: jsCfg.ProjectionStream,
		BatchSize:        100,
		MaxWait:          500 * time.Millisecond,
	})
	svc := &Service{
		catalog:        catalog,
		executionQueue: queue,
		projectionRepo: projectionRepo,
		projector:      projector,
	}
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})

	submitRsp, err := svc.SubmitJobItems(ctx, &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-rpc",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
		Priority:      10,
	}}})
	if err != nil {
		t.Fatalf("SubmitJobItems transport error = %v", err)
	}
	if submitRsp.GetCreated() != 1 || submitRsp.GetAcks()[0].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("submit rsp = %+v", submitRsp)
	}

	pollRsp, err := svc.PollJobItems(ctx, &pb.PollJobItemsReq{
		SpaceId:           "crypto",
		NodeId:            "node-1",
		SupportedJobTypes: []string{"collect.kline"},
		ProtocolVersion:   supportedJobItemProtocolVersion,
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("PollJobItems transport error = %v", err)
	}
	if len(pollRsp.GetItems()) != 1 || pollRsp.GetItems()[0].GetAttemptNo() != 1 {
		t.Fatalf("poll rsp = %+v", pollRsp)
	}

	reportRsp, err := svc.ReportJobItemStatus(ctx, &pb.ReportJobItemStatusReq{
		SpaceId:       "crypto",
		NodeId:        "node-1",
		JobItemId:     "ji-rpc",
		AttemptNo:     1,
		Status:        pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS,
		ResultSummary: params,
		DurationMs:    5,
	})
	if err != nil {
		t.Fatalf("ReportJobItemStatus transport error = %v", err)
	}
	if reportRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("report rsp = %+v", reportRsp)
	}
	if err := projector.RunOnce(ctx); err != nil {
		t.Fatalf("projector RunOnce() error = %v", err)
	}
	detail, err := projectionRepo.Get(ctx, &pb.GetJobItemReq{SpaceId: "crypto", JobItemId: "ji-rpc"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.GetStatus() != pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS {
		t.Fatalf("detail status = %v", detail.GetStatus())
	}
}

func TestCanceledRunningJobItemReportTerminatesQueueMessage(t *testing.T) {
	ctx := context.Background()
	db := newNodeSCFTestDB(t)
	projectionRepo := projection.NewRepository(db, projection.RepositoryOptions{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if _, err := projectionRepo.CreatePending(ctx, &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-cancel-report",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}, projection.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := projectionRepo.TryMarkRunning(ctx, projection.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-cancel-report",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.cancel-report",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	if err := projectionRepo.MarkCanceled(ctx, "crypto", "ji-cancel-report", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	queue := &recordingExecutionQueue{}
	svc := &Service{projectionRepo: projectionRepo, executionQueue: queue}

	rsp, err := svc.ReportJobItemStatus(ctx, &pb.ReportJobItemStatusReq{
		SpaceId:   "crypto",
		NodeId:    "node-1",
		JobItemId: "ji-cancel-report",
		AttemptNo: 1,
		Status:    pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS,
	})
	if err != nil {
		t.Fatalf("ReportJobItemStatus transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp = %+v", rsp)
	}
	if queue.termed != "$JS.ACK.cancel-report" {
		t.Fatalf("termed ack subject = %q", queue.termed)
	}
	directives, err := projectionRepo.ListCancelDirectives(ctx, "crypto", "node-1", 20)
	if err != nil {
		t.Fatalf("ListCancelDirectives() error = %v", err)
	}
	if len(directives) != 0 {
		t.Fatalf("directives after late report = %+v, want none", directives)
	}
}

func startRPCQueueRuntime(t *testing.T) (*jobqueue.Runtime, config.JetStreamConfig) {
	t.Helper()
	port := freeRPCQueuePort(t)
	cfg := config.JetStreamConfig{
		Enabled:          true,
		NATSURL:          "nats://127.0.0.1:" + port,
		SubjectPrefix:    jobqueue.DefaultSubjectPrefix,
		ExecStream:       jobqueue.DefaultExecStream,
		ProjectionStream: jobqueue.DefaultProjectionStream,
		Embedded: config.EmbeddedJetStreamConfig{
			Enabled:          true,
			Host:             "127.0.0.1",
			Port:             mustRPCQueueAtoi(t, port),
			StoreDir:         t.TempDir(),
			StartupTimeoutMS: 5000,
		},
	}
	rt, err := jobqueue.StartEmbedded(context.Background(), cfg.Embedded)
	if err != nil {
		t.Fatalf("StartEmbedded() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	if err := rt.EnsureStreams(cfg); err != nil {
		t.Fatalf("EnsureStreams() error = %v", err)
	}
	return rt, cfg
}

type recordingExecutionQueue struct {
	termed string
}

func (q *recordingExecutionQueue) Publish(context.Context, *pb.JobItem) (*jobqueue.PublishResult, error) {
	return nil, nil
}

func (q *recordingExecutionQueue) Fetch(context.Context, jobqueue.FetchRequest) ([]jobqueue.Delivery, error) {
	return nil, nil
}

func (q *recordingExecutionQueue) Ack(context.Context, string) error { return nil }

func (q *recordingExecutionQueue) Nak(context.Context, string, time.Duration) error { return nil }

func (q *recordingExecutionQueue) Term(_ context.Context, ackSubject string) error {
	q.termed = ackSubject
	return nil
}

func (q *recordingExecutionQueue) InProgress(context.Context, string) error { return nil }

func (q *recordingExecutionQueue) Close() error { return nil }

func freeRPCQueuePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	return port
}

func mustRPCQueueAtoi(t *testing.T, raw string) int {
	t.Helper()
	var out int
	for _, r := range raw {
		if r < '0' || r > '9' {
			t.Fatalf("invalid port %q", raw)
		}
		out = out*10 + int(r-'0')
	}
	return out
}
