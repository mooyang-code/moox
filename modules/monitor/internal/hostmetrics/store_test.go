package hostmetrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeSnapshotWriter struct {
	err       error
	calls     int
	lastID    string
	lastAgent string
	lastSnap  *hostmetricpb.HostSnapshot
}

func (w *fakeSnapshotWriter) WriteSnapshot(_ context.Context, snapshot *hostmetricpb.HostSnapshot, agentID string, _ time.Time, messageID string) error {
	w.calls++
	w.lastID, w.lastAgent, w.lastSnap = messageID, agentID, proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
	return w.err
}

func validHostDelivery(t *testing.T) *jetstream.Delivery {
	t.Helper()
	now := timestamppb.Now()
	metric := &hostmetricpb.HostMetric{Snapshot: &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4, UsageAvailable: true, UsagePercent: 25}, Memory: &hostmetricpb.MemoryMetric{TotalBytes: 100, UsedBytes: 50, AvailableBytes: 50, UsagePercent: 50}}}
	payload, err := proto.Marshal(metric)
	if err != nil {
		t.Fatal(err)
	}
	return &jetstream.Delivery{Message: &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: "0190f4d0-7b1c-7f45-9a3e-7c28f6479a73", Topic: Topic, Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, Producer: &messagepb.Producer{ServiceName: "moox-host-agent", InstanceId: "0190f4d0-7b1c-4f45-9a3e-7c28f6479a73", NodeId: "host", BootId: "boot"}, SpaceId: SpaceID, OccurredAt: now, PublishedAt: now, ContentType: ContentType, Payload: payload}}
}

func TestStorePersistsToStorageBeforeUpdatingLatest(t *testing.T) {
	writer := &fakeSnapshotWriter{}
	store := NewStoreWithWriter(writer)
	delivery := validHostDelivery(t)
	metric, err := ValidateMessage(delivery.Message)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.persist(context.Background(), delivery, metric); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || writer.lastAgent == "" || writer.lastID == "" {
		t.Fatalf("writer calls=%d agent=%q id=%q", writer.calls, writer.lastAgent, writer.lastID)
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil || len(agents) != 1 {
		t.Fatalf("agents=%+v err=%v", agents, err)
	}
	delivery.Message.GetProducer().NodeId = "mutated"
	if agents[0].Hostname != "host" {
		t.Fatalf("latest view was not captured immutably: %+v", agents[0])
	}
}

func TestStoreLeavesLatestUnchangedWhenStorageFails(t *testing.T) {
	writer := &fakeSnapshotWriter{err: errors.New("storage unavailable")}
	store := NewStoreWithWriter(writer)
	delivery := validHostDelivery(t)
	metric, _ := ValidateMessage(delivery.Message)
	if err := store.persist(context.Background(), delivery, metric); err == nil {
		t.Fatal("storage failure unexpectedly succeeded")
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("latest registry updated after failed storage write: %+v", agents)
	}
}

func TestStoreHistoryIsOwnedByStorage(t *testing.T) {
	store := NewStoreWithWriter(&fakeSnapshotWriter{})
	history, err := store.History(context.Background(), "agent", time.Time{}, time.Now(), 100)
	if err != nil || len(history) != 0 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}
