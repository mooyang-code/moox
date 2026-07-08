package jobstate

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestKVStoreCreatePendingDeduplicates(t *testing.T) {
	ctx := context.Background()
	store := newTestKVStore(t, 48*time.Hour)
	item := testJobItem(t, "crypto", "ji-1")

	first, err := store.CreatePending(ctx, item, QueueMeta{})
	if err != nil {
		t.Fatalf("CreatePending first error = %v", err)
	}
	if !first.Created || first.Status != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("first = %+v", first)
	}

	second, err := store.CreatePending(ctx, item, QueueMeta{})
	if err != nil {
		t.Fatalf("CreatePending second error = %v", err)
	}
	if !second.Deduplicated || second.Status != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED {
		t.Fatalf("second = %+v", second)
	}
}

func TestKVStoreRunningAndTerminalReport(t *testing.T) {
	ctx := context.Background()
	store := newTestKVStore(t, 48*time.Hour)
	_, err := store.CreatePending(ctx, testJobItem(t, "crypto", "ji-2"), QueueMeta{Subject: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPublished(ctx, "crypto", "ji-2", QueueMeta{Subject: "sub", Stream: "EXEC", StreamSeq: 42}); err != nil {
		t.Fatal(err)
	}
	ok, running, err := store.TryMarkRunning(ctx, RunningRequest{
		SpaceID: "crypto", JobItemID: "ji-2", NodeID: "node-1", AckSubject: "ack", StreamSeq: 42,
	})
	if err != nil || !ok {
		t.Fatalf("TryMarkRunning ok=%v state=%+v err=%v", ok, running, err)
	}
	if running.AttemptNo != 1 {
		t.Fatalf("attempt = %d, want 1", running.AttemptNo)
	}
	updated, err := store.MarkReported(ctx, ReportEvent{
		SpaceID: "crypto", JobItemID: "ji-2", NodeID: "node-1", AttemptNo: 1,
		Status: StatusSuccess, ResultSummary: map[string]any{"rows": float64(3)}, Time: time.Now(),
	})
	if err != nil {
		t.Fatalf("MarkReported error = %v", err)
	}
	if updated.Status != StatusSuccess || !updated.IsTerminal() || len(updated.Attempts) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestKVStoreRetryableFailureReturnsPending(t *testing.T) {
	ctx := context.Background()
	store := newTestKVStore(t, 48*time.Hour)
	_, err := store.CreatePending(ctx, testJobItem(t, "crypto", "ji-retry"), QueueMeta{})
	if err != nil {
		t.Fatal(err)
	}
	ok, _, err := store.TryMarkRunning(ctx, RunningRequest{SpaceID: "crypto", JobItemID: "ji-retry", NodeID: "node-1", AckSubject: "ack"})
	if err != nil || !ok {
		t.Fatalf("TryMarkRunning ok=%v err=%v", ok, err)
	}

	updated, err := store.MarkReported(ctx, ReportEvent{
		SpaceID: "crypto", JobItemID: "ji-retry", NodeID: "node-1", AttemptNo: 1,
		Status: StatusFailed, ErrorKind: ErrorRetryable, ErrorMessage: "try again", Time: time.Now(),
	})
	if err != nil {
		t.Fatalf("MarkReported error = %v", err)
	}
	if updated.Status != StatusPending || updated.RunningNode != "" || updated.Attempts[0].Status != AttemptFailed {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestKVStoreCancelDirectiveForRunningNode(t *testing.T) {
	ctx := context.Background()
	store := newTestKVStore(t, 48*time.Hour)
	_, err := store.CreatePending(ctx, testJobItem(t, "crypto", "ji-cancel"), QueueMeta{})
	if err != nil {
		t.Fatal(err)
	}
	ok, _, err := store.TryMarkRunning(ctx, RunningRequest{SpaceID: "crypto", JobItemID: "ji-cancel", NodeID: "node-1", AckSubject: "ack"})
	if err != nil || !ok {
		t.Fatalf("TryMarkRunning ok=%v err=%v", ok, err)
	}
	if err := store.MarkCanceled(ctx, "crypto", "ji-cancel", "operator cancel"); err != nil {
		t.Fatalf("MarkCanceled error = %v", err)
	}
	directives, err := store.ListCancelDirectives(ctx, "crypto", "node-1", 20)
	if err != nil {
		t.Fatalf("ListCancelDirectives error = %v", err)
	}
	if len(directives) != 1 || directives[0].GetJobItemId() != "ji-cancel" || directives[0].GetAttemptNo() != 1 {
		t.Fatalf("directives = %+v", directives)
	}
	if err := store.ClearCancelDirective(ctx, "crypto", "ji-cancel", 1); err != nil {
		t.Fatalf("ClearCancelDirective error = %v", err)
	}
	directives, err = store.ListCancelDirectives(ctx, "crypto", "node-1", 20)
	if err != nil {
		t.Fatalf("ListCancelDirectives after clear error = %v", err)
	}
	if len(directives) != 0 {
		t.Fatalf("directives after clear = %+v", directives)
	}
}

func newTestKVStore(t *testing.T, ttl time.Duration) *KVStore {
	t.Helper()
	port := freeTCPPort(t)
	cfg := config.Default().JetStream
	cfg.NATSURL = "nats://127.0.0.1:" + port
	cfg.Embedded = config.EmbeddedJetStreamConfig{
		Enabled:          true,
		Host:             "127.0.0.1",
		Port:             mustAtoi(t, port),
		StoreDir:         t.TempDir(),
		StartupTimeoutMS: 5000,
	}
	rt, err := jobqueue.StartEmbedded(context.Background(), cfg.Embedded)
	if err != nil {
		t.Fatalf("StartEmbedded() error = %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	bucket := fmt.Sprintf("TEST_JOB_ACTIVE_%d", time.Now().UnixNano())
	kv, err := rt.JetStream().CreateKeyValue(&nats.KeyValueConfig{
		Bucket:  bucket,
		Storage: nats.FileStorage,
		History: 1,
		TTL:     ttl,
	})
	if err != nil {
		t.Fatalf("CreateKeyValue() error = %v", err)
	}
	return NewKVStore(kv, Options{RecoverAfterMillis: 600000, DefaultMaxAttempts: 3})
}

func testJobItem(t *testing.T, spaceID, jobItemID string) *pb.JobItem {
	t.Helper()
	params, err := structpb.NewStruct(map[string]any{"symbol": "BTC/USDT"})
	if err != nil {
		t.Fatal(err)
	}
	return &pb.JobItem{
		SpaceId:       spaceID,
		JobId:         "job-1",
		JobItemId:     jobItemID,
		JobType:       "collector.kline",
		CodePackageId: "pkg-1",
		Params:        params,
		Priority:      5,
	}
}

func freeTCPPort(t *testing.T) string {
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

func mustAtoi(t *testing.T, raw string) int {
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
