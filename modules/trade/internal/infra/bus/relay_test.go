package bus

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

type fakePublisher struct {
	ids      []string
	subjects []string
	bodies   [][]byte
}

func (f *fakePublisher) Publish(_ context.Context, event events.EventType, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	encoded, err := registry.Encode(event, payload, opts)
	if err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
	if err != nil {
		return nil, err
	}
	f.ids = append(f.ids, opts.EventID)
	f.subjects = append(f.subjects, encoded.Subject)
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
		return tx.AddOutbox("m1", "moox.trade.order.state.changed.v1", []byte(`{"space_id":"space","order_id":"order-1","state":"OPEN"}`))
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

func TestEventForTopicRejectsLegacyAliases(t *testing.T) {
	for _, topic := range []string{
		"moox.trade.order.intent_created.v1",
		"moox.trade.order.state_changed.v1",
		"moox.trade.execution.slice_ready.v1",
	} {
		if _, err := eventForTopic(topic); err == nil {
			t.Fatalf("eventForTopic(%q) accepted a legacy alias", topic)
		}
	}
}
