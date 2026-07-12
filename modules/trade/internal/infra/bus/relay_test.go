package bus

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"path/filepath"
	"testing"
)

type fakePublisher struct{ msgs []*messagepb.MooxMessage }

func (f *fakePublisher) Publish(_ context.Context, m *messagepb.MooxMessage, _ ...jetstream.PublishOption) (*jetstream.PublishAck, error) {
	f.msgs = append(f.msgs, m)
	return &jetstream.PublishAck{Stream: "MOOX_TRADE", Sequence: uint64(len(f.msgs))}, nil
}
func TestRelayUsesStablePublicMessageContract(t *testing.T) {
	s, e := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := telemetry.WithTrace(context.Background(), telemetry.Trace{TraceID: "trace-1", RequestID: "request-1"})
	if e = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.AddOutbox("m1", "moox.trade.order.state_changed.v1", []byte(`{"x":1}`))
	}); e != nil {
		t.Fatal(e)
	}
	p := &fakePublisher{}
	r := Relay{Store: s, Publisher: p, InstanceID: "i", BootID: "b"}
	if e = r.RunOnce(context.Background(), 10); e != nil {
		t.Fatal(e)
	}
	if len(p.msgs) != 1 || p.msgs[0].MessageId != "m1" || p.msgs[0].ProtocolVersion != 1 || p.msgs[0].GetTrace().GetTraceId() != "trace-1" || p.msgs[0].GetTrace().GetRequestId() != "request-1" {
		t.Fatalf("%+v", p.msgs)
	}
}
