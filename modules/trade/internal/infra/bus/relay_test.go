package bus

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
)

type fakePublisher struct {
	ids      []string
	subjects []string
	bodies   [][]byte
}

func (f *fakePublisher) PublishMessage(_ context.Context, message *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	subject, err := registry.SubjectForMessage(message)
	if err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, err
	}
	f.ids = append(f.ids, message.GetEventId())
	f.subjects = append(f.subjects, subject)
	f.bodies = append(f.bodies, body)
	return &jetstream.PublishAck{Stream: "MOOX_TRADE", Sequence: uint64(len(f.ids))}, nil
}

func TestRelayPublishesGovernedEventMessage(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		data, encodeErr := registryOrderEvent("m1")
		if encodeErr != nil {
			return encodeErr
		}
		return tx.AddOutbox("m1", data)
	}); err != nil {
		t.Fatal(err)
	}
	p := &fakePublisher{}
	if err = (Relay{Store: s, Publisher: p}).RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(p.ids) != 1 || p.ids[0] != "m1" || p.subjects[0] == "" {
		t.Fatalf("published ids=%v subjects=%v", p.ids, p.subjects)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, payload, err := events.DecodeRaw(registry, p.bodies[0], p.subjects[0], p.ids[0], events.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	if message.GetEventName() != events.TradeOrderStateChanged.Name || payload == nil {
		t.Fatalf("message=%v payload=%T", message, payload)
	}
}

func registryOrderEvent(id string) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return registry.MarshalMessage(events.TradeOrderStateChanged, &tradeeventpb.OrderSnapshot{OrderId: "order-1", State: "OPEN"}, events.PublishOptions{EventID: id, OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "order-1"})
}
