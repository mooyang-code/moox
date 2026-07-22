package trigger

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/testkit"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStorageEventEnvelopeRequiresExactContract(t *testing.T) {
	base := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       "storage-mzxw6-1",
		Topic:           "moox.storage.fields_changed.v1.mzxw6.mjqxe",
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "storage-node", InstanceId: "foo", NodeId: "foo"},
		OccurredAt:      timestamppb.New(time.Now().UTC()),
		PublishedAt:     timestamppb.New(time.Now().UTC()),
		ContentType:     jetstream.StorageFieldsChangedContentType,
		MessageType:     jetstream.StorageFieldsChangedMessageType,
		Payload:         []byte("payload"),
	}
	for _, test := range []struct {
		name   string
		mutate func(*messagepb.MooxMessage)
		wantOK bool
	}{
		{name: "exact content type", wantOK: true},
		{name: "bare protobuf content type", mutate: func(message *messagepb.MooxMessage) { message.ContentType = "application/x-protobuf" }},
		{name: "wrong protobuf message", mutate: func(message *messagepb.MooxMessage) {
			message.ContentType = "application/x-protobuf; message=other.Message"
		}},
		{name: "wrong message type", mutate: func(message *messagepb.MooxMessage) { message.MessageType = "moox.storage.other.v1" }},
		{name: "one topic token", mutate: func(message *messagepb.MooxMessage) { message.Topic = "moox.storage.fields_changed.v1.mzxw6" }},
		{name: "wildcard topic", mutate: func(message *messagepb.MooxMessage) { message.Topic = "moox.storage.fields_changed.v1.mzxw6.>" }},
		{name: "wrong protocol", mutate: func(message *messagepb.MooxMessage) { message.ProtocolVersion = 99 }},
		{name: "wrong kind", mutate: func(message *messagepb.MooxMessage) { message.Kind = messagepb.MessageKind_MESSAGE_KIND_COMMAND }},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := proto.Clone(base).(*messagepb.MooxMessage)
			if test.mutate != nil {
				test.mutate(message)
			}
			if got := isStorageFieldsChangedEnvelope(message); got != test.wantOK {
				t.Fatalf("isStorageFieldsChangedEnvelope() = %t, want %t", got, test.wantOK)
			}
		})
	}
}

func TestStorageFieldsChangedPayloadMatchesSubject(t *testing.T) {
	message := &messagepb.MooxMessage{Topic: "moox.storage.fields_changed.v1.mzxw6.mjqxe", ProtocolVersion: jetstream.ProtocolVersion, Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, ContentType: jetstream.StorageFieldsChangedContentType, MessageType: jetstream.StorageFieldsChangedMessageType}
	if !storageFieldsChangedPayloadMatches(message, &storagepb.DatasetFieldsChanged{SpaceId: "foo", DatasetId: "bar"}) {
		t.Fatal("valid subject/payload was rejected")
	}
	if storageFieldsChangedPayloadMatches(message, &storagepb.DatasetFieldsChanged{SpaceId: "other", DatasetId: "bar"}) {
		t.Fatal("mismatched space was accepted")
	}
}

func TestEventStormEmitsOneTaskPerSubject(t *testing.T) {
	symbols := testkit.Symbols(500)
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(time.Second, []domain.FactorBinding{{
		BindingID:     "b1",
		FactorID:      "bias",
		SpaceID:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		SubjectMode:   domain.SubjectModeAll,
		SubjectsJSON:  "[]",
		TargetDataset: "binance_spot_factor",
		Status:        domain.BindingStatusEnabled,
	}})

	d.Ingest(testkit.RowsChangedEvent("crypto", "binance_spot_kline", "1m", now, symbols), now)
	tasks := d.Flush(now.Add(time.Second))
	if len(tasks) != len(symbols) {
		t.Fatalf("tasks = %d, want %d", len(tasks), len(symbols))
	}
	seen := map[string]struct{}{}
	for _, task := range tasks {
		if len(task.FactorIDs) != 1 || task.FactorIDs[0] != "bias" {
			t.Fatalf("factor ids = %#v", task.FactorIDs)
		}
		seen[task.SubjectID] = struct{}{}
	}
	if len(seen) != len(symbols) {
		t.Fatalf("unique subjects = %d, want %d", len(seen), len(symbols))
	}
}

func TestEventBatcherSplitsTasksByTargetDataset(t *testing.T) {
	now := time.Date(2026, 7, 6, 9, 15, 0, 0, time.UTC)
	d := NewEventBatcher(time.Second, []domain.FactorBinding{
		{
			BindingID:     "b1",
			FactorID:      "bias",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			Freq:          "1m",
			SubjectMode:   domain.SubjectModeAll,
			SubjectsJSON:  "[]",
			TargetDataset: "binance_spot_factor",
			Status:        domain.BindingStatusEnabled,
		},
		{
			BindingID:     "b2",
			FactorID:      "volume",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			Freq:          "1m",
			SubjectMode:   domain.SubjectModeAll,
			SubjectsJSON:  "[]",
			TargetDataset: "binance_spot_volume_factor",
			Status:        domain.BindingStatusEnabled,
		},
	})

	d.Ingest(testkit.RowsChangedEvent("crypto", "binance_spot_kline", "1m", now, []string{"BTC-USDT"}), now)
	tasks := d.Flush(now.Add(time.Second))
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2: %+v", len(tasks), tasks)
	}
	byTarget := map[string][]string{}
	for _, task := range tasks {
		byTarget[task.TargetDataset] = task.FactorIDs
	}
	if got := byTarget["binance_spot_factor"]; len(got) != 1 || got[0] != "bias" {
		t.Fatalf("binance_spot_factor ids = %#v", got)
	}
	if got := byTarget["binance_spot_volume_factor"]; len(got) != 1 || got[0] != "volume" {
		t.Fatalf("binance_spot_volume_factor ids = %#v", got)
	}
}
