package rpc

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobhistory"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/testfixture"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
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

func TestJobItemRPCUsesActiveKVAndWritesHistory(t *testing.T) {
	ctx := context.Background()
	db := newNodeSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	if err := catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID:   "crypto",
		NodeID:    "node-1",
		PackageID: "collector-scf",
		NodeType:  "scf-event",
		Status:    "online",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	rt, jsCfg := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	historyDir := t.TempDir()
	history := jobhistory.NewStore(jobhistory.StoreOptions{Dir: historyDir, RetentionDays: 2})
	queue := jobqueue.NewJetStreamQueue(rt, jobqueue.QueueConfig{
		Naming:          jobqueue.NamingConfig{SubjectPrefix: jobqueue.DefaultSubjectPrefix},
		ExecStream:      jsCfg.ExecStream,
		AckWait:         200 * time.Millisecond,
		MaxDeliver:      3,
		FetchMaxWait:    500 * time.Millisecond,
		DefaultMaxBatch: 10,
	})
	svc := &Service{
		catalog:        catalog,
		executionQueue: queue,
		jobState:       stateStore,
		history:        history,
	}
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})

	submitRsp, err := svc.SubmitJobItems(ctx, &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-kv-rpc",
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
		JobItemId:     "ji-kv-rpc",
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
	state, err := stateStore.Get(ctx, "crypto", "ji-kv-rpc")
	if err != nil {
		t.Fatalf("state Get() error = %v", err)
	}
	if state.Status != jobstate.StatusSuccess || !state.HistorySynced {
		t.Fatalf("state = %+v", state)
	}
	dbPath := filepath.Join(historyDir, state.FinishedAt.Format("20060102")+".db")
	historyDB, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	var count int64
	if err := historyDB.Table("t_cloud_job_items").Where("c_space_id = ? AND c_job_item_id = ?", "crypto", "ji-kv-rpc").Count(&count).Error; err != nil {
		t.Fatalf("count history row: %v", err)
	}
	if count != 1 {
		t.Fatalf("history row count = %d, want 1", count)
	}
}

func TestCanceledRunningJobItemReportTerminatesQueueMessageWithActiveKV(t *testing.T) {
	ctx := context.Background()
	rt, _ := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if _, err := stateStore.CreatePending(ctx, &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-kv-cancel-report",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}, jobstate.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := stateStore.TryMarkRunning(ctx, jobstate.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-kv-cancel-report",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.cancel-report",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	if err := stateStore.MarkCanceled(ctx, "crypto", "ji-kv-cancel-report", "user canceled"); err != nil {
		t.Fatalf("MarkCanceled() error = %v", err)
	}
	queue := &recordingExecutionQueue{}
	svc := &Service{jobState: stateStore, executionQueue: queue}

	rsp, err := svc.ReportJobItemStatus(ctx, &pb.ReportJobItemStatusReq{
		SpaceId:   "crypto",
		NodeId:    "node-1",
		JobItemId: "ji-kv-cancel-report",
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
	directives, err := stateStore.ListCancelDirectives(ctx, "crypto", "node-1", 20)
	if err != nil {
		t.Fatalf("ListCancelDirectives() error = %v", err)
	}
	if len(directives) != 0 {
		t.Fatalf("directives after late report = %+v, want none", directives)
	}
}

func TestSubmitJobItemsRetriesEnqueueFailedActiveKVItem(t *testing.T) {
	ctx := context.Background()
	rt, _ := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	queue := &flakyPublishQueue{failures: 1}
	svc := &Service{jobState: stateStore, executionQueue: queue}
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	item := &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-retry-enqueue",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}

	first, err := svc.SubmitJobItems(ctx, &pb.SubmitJobItemsReq{Items: []*pb.JobItem{item}})
	if err != nil {
		t.Fatalf("first SubmitJobItems transport error = %v", err)
	}
	if first.GetRejected() != 1 || first.GetAcks()[0].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED {
		t.Fatalf("first submit rsp = %+v", first)
	}
	state, err := stateStore.Get(ctx, "crypto", "ji-retry-enqueue")
	if err != nil {
		t.Fatalf("state after first submit: %v", err)
	}
	if state.Status != jobstate.StatusEnqueueFailed {
		t.Fatalf("state status after first submit = %s, want %s", state.Status, jobstate.StatusEnqueueFailed)
	}

	second, err := svc.SubmitJobItems(ctx, &pb.SubmitJobItemsReq{Items: []*pb.JobItem{item}})
	if err != nil {
		t.Fatalf("second SubmitJobItems transport error = %v", err)
	}
	if second.GetCreated() != 1 || second.GetAcks()[0].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("second submit rsp = %+v", second)
	}
	if queue.publishes != 2 {
		t.Fatalf("publish attempts = %d, want 2", queue.publishes)
	}
	state, err = stateStore.Get(ctx, "crypto", "ji-retry-enqueue")
	if err != nil {
		t.Fatalf("state after retry submit: %v", err)
	}
	if state.Status != jobstate.StatusPending || state.Queue.StreamSeq == 0 {
		t.Fatalf("state after retry submit = %+v", state)
	}
}

func TestPollJobItemsTerminatesAndArchivesWhenMaxAttemptsExhausted(t *testing.T) {
	ctx := context.Background()
	db := newNodeSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	if err := catalog.UpsertNode(ctx, store.CloudNode{
		SpaceID:   "crypto",
		NodeID:    "node-1",
		PackageID: "collector-scf",
		NodeType:  "scf-event",
		Status:    "online",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	rt, _ := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	clock := &testClock{now: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		Clock:              clock,
		RecoverAfterMillis: 1,
		DefaultMaxAttempts: 1,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	item := &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-max-attempt",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}
	if _, err := stateStore.CreatePending(ctx, item, jobstate.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := stateStore.TryMarkRunning(ctx, jobstate.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-max-attempt",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.old",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("initial TryMarkRunning() ok=%v err=%v", ok, err)
	}
	clock.now = clock.now.Add(2 * time.Millisecond)
	historyDir := t.TempDir()
	queue := &recordingExecutionQueue{deliveries: []jobqueue.Delivery{{
		Message: jobqueue.JobItemMessage{
			SpaceID:       "crypto",
			JobID:         "job-1",
			JobItemID:     "ji-max-attempt",
			JobType:       "collect.kline",
			CodePackageID: "collector-scf",
			Params:        map[string]any{"symbol": "BTCUSDT"},
		},
		AckSubject: "$JS.ACK.max",
		StreamSeq:  2,
	}}}
	svc := &Service{
		catalog:        catalog,
		jobState:       stateStore,
		history:        jobhistory.NewStore(jobhistory.StoreOptions{Dir: historyDir, RetentionDays: 2}),
		executionQueue: queue,
	}

	rsp, err := svc.PollJobItems(ctx, &pb.PollJobItemsReq{
		SpaceId:           "crypto",
		NodeId:            "node-1",
		SupportedJobTypes: []string{"collect.kline"},
		ProtocolVersion:   supportedJobItemProtocolVersion,
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("PollJobItems transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetItems()) != 0 {
		t.Fatalf("poll rsp = %+v", rsp)
	}
	if queue.termed != "$JS.ACK.max" || queue.nacked != "" {
		t.Fatalf("queue termed=%q nacked=%q", queue.termed, queue.nacked)
	}
	state, err := stateStore.Get(ctx, "crypto", "ji-max-attempt")
	if err != nil {
		t.Fatalf("state Get() error = %v", err)
	}
	if state.Status != jobstate.StatusFailed || !state.HistorySynced {
		t.Fatalf("state = %+v", state)
	}
	assertHistoryItemCount(t, historyDir, state.UpdatedAt, "crypto", "ji-max-attempt", 1)
}

func TestCancelJobItemWritesTerminalHistory(t *testing.T) {
	ctx := context.Background()
	rt, _ := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	params, _ := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if _, err := stateStore.CreatePending(ctx, &pb.JobItem{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-cancel-history",
		JobType:       "collect.kline",
		CodePackageId: "collector-scf",
		Params:        params,
	}, jobstate.QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := stateStore.TryMarkRunning(ctx, jobstate.RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-cancel-history",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.cancel-history",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}
	historyDir := t.TempDir()
	svc := &Service{
		jobState: stateStore,
		history:  jobhistory.NewStore(jobhistory.StoreOptions{Dir: historyDir, RetentionDays: 2}),
	}

	rsp, err := svc.CancelJobItem(ctx, &pb.CancelJobItemReq{SpaceId: "crypto", JobItemId: "ji-cancel-history"})
	if err != nil {
		t.Fatalf("CancelJobItem transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("cancel rsp = %+v", rsp)
	}
	state, err := stateStore.Get(ctx, "crypto", "ji-cancel-history")
	if err != nil {
		t.Fatalf("state Get() error = %v", err)
	}
	if state.Status != jobstate.StatusCanceled || !state.HistorySynced {
		t.Fatalf("state = %+v", state)
	}
	assertHistoryItemCount(t, historyDir, state.UpdatedAt, "crypto", "ji-cancel-history", 1)
}

func TestSubmitJobItemsRejectsInvalidItemWithoutActiveKVSideEffect(t *testing.T) {
	ctx := context.Background()
	rt, jsCfg := startRPCQueueRuntime(t)
	kv, err := rt.JetStream().KeyValue(config.Default().JobItem.ActiveKVBucket)
	if err != nil {
		t.Fatalf("KeyValue() error = %v", err)
	}
	stateStore := jobstate.NewKVStore(kv, jobstate.Options{
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
	svc := &Service{jobState: stateStore, executionQueue: queue}

	rsp, err := svc.SubmitJobItems(ctx, &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId:       "crypto",
		JobId:         "job-1",
		JobItemId:     "ji-invalid",
		CodePackageId: "collector-scf",
	}}})
	if err != nil {
		t.Fatalf("SubmitJobItems transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret = %+v, want INVALID_PARAM", rsp.GetRetInfo())
	}
	if _, err := stateStore.Get(ctx, "crypto", "ji-invalid"); !errors.Is(err, jobstate.ErrNotFound) {
		t.Fatalf("state Get() error = %v, want ErrNotFound", err)
	}
}

func startRPCQueueRuntime(t *testing.T) (*jobqueue.Runtime, config.JetStreamConfig) {
	t.Helper()
	port := freeRPCQueuePort(t)
	cfg := config.JetStreamConfig{
		Enabled:       true,
		NATSURL:       "nats://127.0.0.1:" + port,
		SubjectPrefix: jobqueue.DefaultSubjectPrefix,
		ExecStream:    jobqueue.DefaultExecStream,
		Embedded: config.EmbeddedJetStreamConfig{
			Enabled:          true,
			Host:             "127.0.0.1",
			Port:             mustRPCQueueAtoi(t, port),
			StoreDir:         t.TempDir(),
			StartupTimeoutMS: 5000,
		},
	}
	rt := testfixture.StartRuntime(t, cfg)
	t.Cleanup(func() { _ = rt.Close() })
	if err := rt.EnsureStreams(cfg, config.Default().JobItem); err != nil {
		t.Fatalf("EnsureStreams() error = %v", err)
	}
	return rt, cfg
}

type recordingExecutionQueue struct {
	deliveries []jobqueue.Delivery
	termed     string
	nacked     string
}

func (q *recordingExecutionQueue) Publish(context.Context, *pb.JobItem) (*jobqueue.PublishResult, error) {
	return nil, nil
}

func (q *recordingExecutionQueue) Fetch(context.Context, jobqueue.FetchRequest) ([]jobqueue.Delivery, error) {
	out := q.deliveries
	q.deliveries = nil
	return out, nil
}

func (q *recordingExecutionQueue) Ack(context.Context, string) error { return nil }

func (q *recordingExecutionQueue) Nak(_ context.Context, ackSubject string, _ time.Duration) error {
	q.nacked = ackSubject
	return nil
}

func (q *recordingExecutionQueue) Term(_ context.Context, ackSubject string) error {
	q.termed = ackSubject
	return nil
}

func (q *recordingExecutionQueue) InProgress(context.Context, string) error { return nil }

func (q *recordingExecutionQueue) Close() error { return nil }

type flakyPublishQueue struct {
	failures  int
	publishes int
}

func (q *flakyPublishQueue) Publish(_ context.Context, item *pb.JobItem) (*jobqueue.PublishResult, error) {
	q.publishes++
	if q.failures > 0 {
		q.failures--
		return nil, errors.New("publish unavailable")
	}
	return &jobqueue.PublishResult{
		Created:  true,
		Subject:  "moox.cloudnode.exec.v1.jobitem.s.crypto.pkg.collector-scf.type.collect_kline",
		Stream:   jobqueue.DefaultExecStream,
		Sequence: uint64(q.publishes),
	}, nil
}

func (q *flakyPublishQueue) Fetch(context.Context, jobqueue.FetchRequest) ([]jobqueue.Delivery, error) {
	return nil, nil
}

func (q *flakyPublishQueue) Ack(context.Context, string) error { return nil }

func (q *flakyPublishQueue) Nak(context.Context, string, time.Duration) error { return nil }

func (q *flakyPublishQueue) Term(context.Context, string) error { return nil }

func (q *flakyPublishQueue) InProgress(context.Context, string) error { return nil }

func (q *flakyPublishQueue) Close() error { return nil }

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time {
	return c.now
}

func assertHistoryItemCount(t *testing.T, dir string, day time.Time, spaceID string, jobItemID string, want int64) {
	t.Helper()
	dbPath := filepath.Join(dir, day.Format("20060102")+".db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	var count int64
	if err := db.Table("t_cloud_job_items").Where("c_space_id = ? AND c_job_item_id = ?", spaceID, jobItemID).Count(&count).Error; err != nil {
		t.Fatalf("count history row: %v", err)
	}
	if count != want {
		t.Fatalf("history row count = %d, want %d", count, want)
	}
}

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
