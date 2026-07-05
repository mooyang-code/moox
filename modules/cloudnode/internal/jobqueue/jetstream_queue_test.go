package jobqueue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestJetStreamQueuePublishesDeduplicatesFetchesAndAcks(t *testing.T) {
	ctx := context.Background()
	rt, cfg := startQueueTestRuntime(t)
	queue := NewJetStreamQueue(rt, QueueConfig{
		Naming:          NamingConfig{SubjectPrefix: DefaultSubjectPrefix},
		ExecStream:      DefaultExecStream,
		AckWait:         200 * time.Millisecond,
		MaxDeliver:      3,
		FetchMaxWait:    500 * time.Millisecond,
		DefaultMaxBatch: 10,
	})

	item := testQueueJobItem(t, "crypto", "job-1", "ji-1", "collect.kline", "collector-scf")
	first, err := queue.Publish(ctx, item)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !first.Created || first.Subject != ExecSubject(NamingConfig{SubjectPrefix: cfg.SubjectPrefix}, "crypto", "collector-scf", "collect.kline") {
		t.Fatalf("first publish = %+v", first)
	}

	duplicate, err := queue.Publish(ctx, item)
	if err != nil {
		t.Fatalf("duplicate Publish() error = %v", err)
	}
	if duplicate.Created {
		t.Fatalf("duplicate publish should be deduplicated: %+v", duplicate)
	}

	deliveries, err := queue.Fetch(ctx, FetchRequest{
		SpaceID:           "crypto",
		CodePackageID:     "collector-scf",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("Fetch() len = %d, want 1", len(deliveries))
	}
	if deliveries[0].Message.JobItemID != "ji-1" || deliveries[0].AckSubject == "" || deliveries[0].StreamSeq == 0 {
		t.Fatalf("unexpected delivery: %+v", deliveries[0])
	}
	if err := queue.Ack(ctx, deliveries[0].AckSubject); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}

	empty, err := queue.Fetch(ctx, FetchRequest{
		SpaceID:           "crypto",
		CodePackageID:     "collector-scf",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("Fetch after ack error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Fetch after ack len = %d, want 0", len(empty))
	}
}

func TestJetStreamQueueNakRedelivers(t *testing.T) {
	ctx := context.Background()
	rt, _ := startQueueTestRuntime(t)
	queue := NewJetStreamQueue(rt, QueueConfig{
		Naming:          NamingConfig{SubjectPrefix: DefaultSubjectPrefix},
		ExecStream:      DefaultExecStream,
		AckWait:         200 * time.Millisecond,
		MaxDeliver:      3,
		FetchMaxWait:    500 * time.Millisecond,
		DefaultMaxBatch: 10,
	})

	item := testQueueJobItem(t, "crypto", "job-1", "ji-retry", "collect.kline", "collector-scf")
	if _, err := queue.Publish(ctx, item); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	first := mustFetchOne(t, queue)
	if first.AttemptNo != 1 {
		t.Fatalf("first attempt = %d, want 1", first.AttemptNo)
	}
	if err := queue.Nak(ctx, first.AckSubject, 10*time.Millisecond); err != nil {
		t.Fatalf("Nak() error = %v", err)
	}
	second := mustFetchOne(t, queue)
	if second.Message.JobItemID != "ji-retry" || second.AttemptNo < 2 {
		t.Fatalf("redelivery = %+v", second)
	}
	_ = queue.Term(ctx, second.AckSubject)
}

func TestJetStreamQueueFetchReleasesPullSubscriptions(t *testing.T) {
	ctx := context.Background()
	rt, _ := startQueueTestRuntime(t)
	queue := NewJetStreamQueue(rt, QueueConfig{
		Naming:          NamingConfig{SubjectPrefix: DefaultSubjectPrefix},
		ExecStream:      DefaultExecStream,
		AckWait:         200 * time.Millisecond,
		MaxDeliver:      3,
		FetchMaxWait:    500 * time.Millisecond,
		DefaultMaxBatch: 10,
	})

	baseline := rt.Conn().NumSubscriptions()
	item := testQueueJobItem(t, "crypto", "job-1", "ji-sub-warmup", "collect.kline", "collector-scf")
	if _, err := queue.Publish(ctx, item); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	deliveries, err := queue.Fetch(ctx, FetchRequest{
		SpaceID:           "crypto",
		CodePackageID:     "collector-scf",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("Fetch() len = %d, want 1", len(deliveries))
	}
	if err := queue.Ack(ctx, deliveries[0].AckSubject); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	warmed := rt.Conn().NumSubscriptions()
	if warmed <= baseline {
		t.Fatalf("subscriptions after first fetch = %d, want > baseline %d", warmed, baseline)
	}

	for i := 0; i < 5; i++ {
		item := testQueueJobItem(t, "crypto", "job-1", fmt.Sprintf("ji-sub-%d", i), "collect.kline", "collector-scf")
		if _, err := queue.Publish(ctx, item); err != nil {
			t.Fatalf("Publish() error = %v", err)
		}
		deliveries, err = queue.Fetch(ctx, FetchRequest{
			SpaceID:           "crypto",
			CodePackageID:     "collector-scf",
			SupportedJobTypes: []string{"collect.kline"},
			Limit:             1,
		})
		if err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if len(deliveries) != 1 {
			t.Fatalf("Fetch() len = %d, want 1", len(deliveries))
		}
		if err := queue.Ack(ctx, deliveries[0].AckSubject); err != nil {
			t.Fatalf("Ack() error = %v", err)
		}
	}

	if got := rt.Conn().NumSubscriptions(); got > warmed {
		t.Fatalf("subscriptions after repeated fetch = %d, want <= warmed %d", got, warmed)
	}
}

func startQueueTestRuntime(t *testing.T) (*Runtime, config.JetStreamConfig) {
	t.Helper()
	port := freeTCPPort(t)
	cfg := config.JetStreamConfig{
		Enabled:          true,
		NATSURL:          "nats://127.0.0.1:" + port,
		SubjectPrefix:    DefaultSubjectPrefix,
		ExecStream:       DefaultExecStream,
		ProjectionStream: DefaultProjectionStream,
		AckWaitMillis:    200,
		MaxDeliver:       3,
		Embedded: config.EmbeddedJetStreamConfig{
			Enabled:          true,
			Host:             "127.0.0.1",
			Port:             mustAtoi(t, port),
			StoreDir:         t.TempDir(),
			StartupTimeoutMS: 5000,
		},
	}
	rt, err := StartEmbedded(context.Background(), cfg.Embedded)
	if err != nil {
		t.Fatalf("StartEmbedded() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	if err := rt.EnsureStreams(cfg); err != nil {
		t.Fatalf("EnsureStreams() error = %v", err)
	}
	return rt, cfg
}

func testQueueJobItem(t *testing.T, spaceID, jobID, jobItemID, jobType, packageID string) *pb.JobItem {
	t.Helper()
	params, err := structpb.NewStruct(map[string]any{"symbol": "BTCUSDT"})
	if err != nil {
		t.Fatalf("NewStruct() error = %v", err)
	}
	return &pb.JobItem{
		SpaceId:       spaceID,
		JobId:         jobID,
		JobItemId:     jobItemID,
		JobType:       jobType,
		CodePackageId: packageID,
		Params:        params,
		Priority:      5,
	}
}

func mustFetchOne(t *testing.T, queue ExecutionQueue) Delivery {
	t.Helper()
	deliveries, err := queue.Fetch(context.Background(), FetchRequest{
		SpaceID:           "crypto",
		CodePackageID:     "collector-scf",
		SupportedJobTypes: []string{"collect.kline"},
		Limit:             1,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("Fetch() len = %d, want 1", len(deliveries))
	}
	return deliveries[0]
}
