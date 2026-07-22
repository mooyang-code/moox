package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"google.golang.org/protobuf/proto"
)

func TestDefaultRegistryHasExplicitEvents(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []EventType{MarketTradeReceived, MarketKlineClosed, StorageRowsUpserted} {
		if _, ok := r.Spec(event); !ok {
			t.Fatalf("event %s is not registered", eventKey(event))
		}
	}
}

func TestEncodeKeepsBusinessMetadataInOuterMessage(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &marketpb.TradeReceived{Exchange: "binance", TradeId: "trade-1", Symbol: "BTCUSDT", Price: 100, Quantity: 2}
	encoded, err := r.Encode(MarketTradeReceived, payload, PublishOptions{
		EventID: "binance:trade-1", OccurredAt: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), SpaceID: "crypto", SubjectID: "BTC-USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Message.GetEventName() != "market.trade.received" || encoded.Message.GetEventVersion() != 1 || encoded.Message.GetSpaceId() != "crypto" || encoded.Message.GetSubjectId() != "BTC-USDT" {
		t.Fatalf("outer metadata = %+v", encoded.Message)
	}
	if encoded.Subject != "moox.market.trade.received.v1.mnzhs4dun4.ijkeglkvkncfi" {
		t.Fatalf("subject = %q", encoded.Subject)
	}
	decoded := new(eventpb.EventMessage)
	if err := proto.Unmarshal(mustMarshal(t, encoded.Message), decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetEventName() != encoded.Message.GetEventName() {
		t.Fatalf("decoded event name = %q", decoded.GetEventName())
	}
}

func TestRegistryRejectsUnknownPayload(t *testing.T) {
	_, err := NewRegistry([]byte("version: 1\nevents:\n  - name: x.y\n    version: 1\n    payload: unknown.Payload\n    subject: moox.x.y.v1.<space>.<subject>\n    stream: MOOX_TEST\n    partition_key: subject_id\n    owner: test\n"))
	if err == nil {
		t.Fatal("NewRegistry() error = nil, want unknown payload error")
	}
}

func mustMarshal(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
