package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSourcePositionSurvivesDataNodeRestart(t *testing.T) {
	ctx := context.Background()
	options := Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"}
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 1}}}}}
	var first []byte
	for attempt := 0; attempt < 2; attempt++ {
		s, err := Open(options)
		require.NoError(t, err)
		_, err = upsertTestRowsEntries(s, ctx, []*pb.RowFieldUpsert{row})
		require.NoError(t, err)
		entries, err := s.ListOutbox(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, entries, attempt+1)
		if attempt == 0 {
			first = append([]byte(nil), entries[0].Data...)
		} else {
			require.Equal(t, first, entries[0].Data)
		}
		for _, entry := range entries {
			message := new(eventpb.EventMessage)
			require.NoError(t, proto.Unmarshal(entry.Data, message))
			payload := new(storagepb.DatasetRowsUpserted)
			require.NoError(t, proto.Unmarshal(message.Payload, payload))
			require.Equal(t, "node", payload.NodeId)
			require.Equal(t, s.sourceStoreID, payload.StoreId)
			require.Contains(t, message.EventId, s.sourceStoreID)
			require.Equal(t, entry.ID, payload.Sequence)
		}
		if attempt == 1 {
			require.Greater(t, entries[1].ID, entries[0].ID)
		}
		require.NoError(t, s.Close())
	}
}

func TestBuildDatasetRowsUpsertedMessageUsesExplicitOuterContract(t *testing.T) {
	data, err := BuildDatasetRowsUpsertedMessage("crypto", "spot_kline", []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "spot_kline", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r1", Version: "v1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err = BindOutboxID(data, "node-1", "store", 7)
	if err != nil {
		t.Fatal(err)
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	if message.GetEventId() == "" || message.GetEventName() != events.DatasetRowsUpserted.Name() || message.GetEventVersion() != events.DatasetRowsUpserted.Version() || message.GetSpaceId() != "crypto" || message.GetSubjectId() != "spot_kline" {
		t.Fatalf("outer message = %v", message)
	}
	payload := &storagepb.DatasetRowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	if payload.GetSpaceId() != "crypto" || payload.GetDatasetId() != "spot_kline" || len(payload.GetRows()) != 1 {
		t.Fatalf("rows.upserted payload = %v", payload)
	}
	if payload.GetNodeId() != "node-1" || payload.GetSequence() != 7 {
		t.Fatalf("source position = %s/%d", payload.GetNodeId(), payload.GetSequence())
	}
	replayed, err := BindOutboxID(data, "node-1", "store", 7)
	if err != nil || string(replayed) != string(data) {
		t.Fatalf("same outbox position changed on retry: %v", err)
	}
}

func TestBuildCollectorPeriodCompletedMessageChangesIDWhenPayloadChanges(t *testing.T) {
	base := &pb.CollectorPeriodCompletedMarker{
		DatasetId: "spot_kline", Frequency: "1m", PeriodTime: 1_725_000_000,
		Status: "complete", BatchId: "batch-1", ConfigSnapshotId: "cfg-1", ExpectedScopeRef: "universe:spot_kline:1m",
		UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"},
		CommittedPositions: []*pb.CommittedPosition{{NodeId: "node-1", StoreId: "store", Sequence: 1}},
		CollectedAt:        timestamppb.New(time.Unix(1_725_000_001, 0)),
	}
	_, firstID, err := BuildCollectorPeriodCompletedMessage("crypto", base)
	if err != nil {
		t.Fatal(err)
	}
	_, retryID, err := BuildCollectorPeriodCompletedMessage("crypto", proto.Clone(base).(*pb.CollectorPeriodCompletedMarker))
	if err != nil {
		t.Fatal(err)
	}
	if firstID != retryID {
		t.Fatalf("identical marker IDs differ: %q != %q", firstID, retryID)
	}
	changed := proto.Clone(base).(*pb.CollectorPeriodCompletedMarker)
	changed.Status = "degraded"
	changed.FailedSubjects = []string{"ETH-USDT"}
	_, changedID, err := BuildCollectorPeriodCompletedMessage("crypto", changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedID == firstID {
		t.Fatalf("changed marker reused ID %q", changedID)
	}
}

func TestPrepareOutboxPublicationIsByteStable(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := upsertTestRowsEntries(store, context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 1)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListOutbox() = %v, %v", entries, err)
	}
	first, err := store.PrepareOutboxPublication(context.Background(), entries[0].ID, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PrepareOutboxPublication(context.Background(), entries[0].ID, nowForTest())
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(mustEvent(t, first), mustEvent(t, second)) {
		t.Fatal("retry changed persisted EventMessage")
	}
}

func TestBindOutboxIDRejectsInvalidEnvelope(t *testing.T) {
	if _, err := BindOutboxID([]byte("not-protobuf"), "foo", "store", 42); err == nil {
		t.Fatal("BindOutboxID() error = nil, want invalid envelope rejection")
	}
}

func TestValidateNewEventIDRejectsPlaceholder(t *testing.T) {
	if err := validateNewEventID(&eventpb.EventMessage{EventId: "outbox-pending"}); err == nil {
		t.Fatal("validateNewEventID() error = nil, want placeholder rejection")
	}
}

func mustEvent(t *testing.T, raw []byte) *eventpb.EventMessage {
	t.Helper()
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		t.Fatal(err)
	}
	return message
}

func nowForTest() time.Time { return time.Now().UTC() }
