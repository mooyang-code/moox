package consumer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/packages/messagepb"
)

func TestHandlerAcknowledgesOnlyAfterJournalSync(t *testing.T) {
	order := make([]string, 0, 2)
	store := &fakeJournal{appendFn: func(domain.EventBatch) (journal.AppendResult, error) {
		order = append(order, "sync")
		return journal.AppendResult{Seq: 1}, nil
	}}
	delivery := &fakeDelivery{message: fixtureEnvelope(nil, "m1"), ackFn: func() error { order = append(order, "ack"); return nil }}
	decoder := &fakeDecoder{batch: domain.EventBatch{MessageID: "m1", Rows: []domain.RowPatch{{Partition: domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}}}}, decision: DecisionArchive}
	handler := NewHandler(decoder, store, &fakeNotifier{})
	if err := handler.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"sync", "ack"}) {
		t.Fatalf("order = %v", order)
	}
}

func TestHandlerQuarantinesInvalidEventAndTerminates(t *testing.T) {
	store := &fakeJournal{quarantineFn: func(journal.QuarantineRecord) error { return nil }}
	delivery := &fakeDelivery{message: fixtureEnvelope(nil, "bad")}
	decoder := &fakeDecoder{decision: DecisionReject, decodeErr: errors.New("invalid row")}
	handler := NewHandler(decoder, store, &fakeNotifier{})
	if err := handler.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if !delivery.terminated {
		t.Fatal("delivery was not terminated")
	}
}

type fakeDecoder struct {
	batch     domain.EventBatch
	decision  Decision
	decodeErr error
}

func (f *fakeDecoder) Decode(*messagepb.MooxMessage) (domain.EventBatch, Decision, error) {
	return f.batch, f.decision, f.decodeErr
}

type fakeNotifier struct{ partitions []domain.PartitionKey }

func (f *fakeNotifier) Notify(p []domain.PartitionKey) { f.partitions = append(f.partitions, p...) }

type fakeJournal struct {
	appendFn     func(domain.EventBatch) (journal.AppendResult, error)
	quarantineFn func(journal.QuarantineRecord) error
}

func (f *fakeJournal) Append(_ context.Context, b domain.EventBatch) (journal.AppendResult, error) {
	if f.appendFn != nil {
		return f.appendFn(b)
	}
	return journal.AppendResult{}, nil
}
func (f *fakeJournal) Quarantine(_ context.Context, q journal.QuarantineRecord) error {
	if f.quarantineFn != nil {
		return f.quarantineFn(q)
	}
	return nil
}

type fakeDelivery struct {
	message    *messagepb.MooxMessage
	ackFn      func() error
	terminated bool
}

func (f *fakeDelivery) Envelope() *messagepb.MooxMessage { return f.message }
func (f *fakeDelivery) RawEnvelope() []byte              { return nil }
func (f *fakeDelivery) MessageID() string                { return f.message.GetMessageId() }
func (f *fakeDelivery) Subject() string                  { return f.message.GetTopic() }
func (f *fakeDelivery) StreamSequence() uint64           { return 1 }
func (f *fakeDelivery) DeliveryCount() uint64            { return 1 }
func (f *fakeDelivery) DecodeError() error               { return nil }
func (f *fakeDelivery) Ack(context.Context) error {
	if f.ackFn != nil {
		return f.ackFn()
	}
	return nil
}
func (f *fakeDelivery) Nak(context.Context, time.Duration) error { return nil }
func (f *fakeDelivery) InProgress(context.Context) error         { return nil }
func (f *fakeDelivery) Term(context.Context) error               { f.terminated = true; return nil }
