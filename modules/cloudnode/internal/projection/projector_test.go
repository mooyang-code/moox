package projection

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

func TestProjectorBatchesReportedEvents(t *testing.T) {
	ctx := context.Background()
	rt, cfg := startProjectionRuntime(t)
	repo := NewRepository(newProjectionTestDB(t), RepositoryOptions{
		Clock:              fixedClock{now: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)},
		RecoverAfterMillis: int64(time.Minute / time.Millisecond),
		DefaultMaxAttempts: 3,
	})
	if _, err := repo.CreatePending(ctx, testProjectionJobItem(t, "crypto", "job-1", "ji-projected"), QueueMeta{}); err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if ok, _, err := repo.TryMarkRunning(ctx, RunningRequest{
		SpaceID:    "crypto",
		JobItemID:  "ji-projected",
		NodeID:     "node-1",
		AckSubject: "$JS.ACK.demo",
		StreamSeq:  1,
	}); err != nil || !ok {
		t.Fatalf("TryMarkRunning() ok=%v err=%v", ok, err)
	}

	projector := NewProjector(rt.JetStream(), repo, ProjectorOptions{
		Naming:           jobqueue.NamingConfig{SubjectPrefix: jobqueue.DefaultSubjectPrefix},
		ProjectionStream: cfg.ProjectionStream,
		BatchSize:        100,
		MaxWait:          500 * time.Millisecond,
	})
	if err := projector.PublishReported(ctx, ReportEvent{
		SpaceID:       "crypto",
		JobItemID:     "ji-projected",
		NodeID:        "node-1",
		AttemptNo:     1,
		Status:        ReportStatusSuccess,
		ResultSummary: map[string]any{"rows": float64(8)},
		DurationMS:    10,
		Time:          time.Date(2026, 7, 5, 10, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("PublishReported() error = %v", err)
	}
	if err := projector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := repo.Get(ctx, &pb.GetJobItemReq{SpaceId: "crypto", JobItemId: "ji-projected"})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if detail.GetStatus() != pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS {
		t.Fatalf("status = %v, want success", detail.GetStatus())
	}
}

func TestProjectorRunOnceReusesPullSubscription(t *testing.T) {
	ctx := context.Background()
	rt, cfg := startProjectionRuntime(t)
	repo := NewRepository(newProjectionTestDB(t), RepositoryOptions{})
	projector := NewProjector(rt.JetStream(), repo, ProjectorOptions{
		Naming:           jobqueue.NamingConfig{SubjectPrefix: jobqueue.DefaultSubjectPrefix},
		ProjectionStream: cfg.ProjectionStream,
		BatchSize:        100,
		MaxWait:          10 * time.Millisecond,
	})

	if err := projector.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	warmed := rt.Conn().NumSubscriptions()
	for i := 0; i < 5; i++ {
		if err := projector.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce() error = %v", err)
		}
	}
	if got := rt.Conn().NumSubscriptions(); got > warmed {
		t.Fatalf("subscriptions after repeated RunOnce = %d, want <= warmed %d", got, warmed)
	}
}

func startProjectionRuntime(t *testing.T) (*jobqueue.Runtime, config.JetStreamConfig) {
	t.Helper()
	port := freeProjectionTCPPort(t)
	cfg := config.JetStreamConfig{
		Enabled:          true,
		NATSURL:          "nats://127.0.0.1:" + port,
		SubjectPrefix:    jobqueue.DefaultSubjectPrefix,
		ExecStream:       jobqueue.DefaultExecStream,
		ProjectionStream: jobqueue.DefaultProjectionStream,
		Embedded: config.EmbeddedJetStreamConfig{
			Enabled:          true,
			Host:             "127.0.0.1",
			Port:             mustProjectionAtoi(t, port),
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

func freeProjectionTCPPort(t *testing.T) string {
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

func mustProjectionAtoi(t *testing.T, raw string) int {
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
