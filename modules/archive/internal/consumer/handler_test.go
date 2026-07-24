package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/assert"
)

func TestHandlerAcknowledgesOnlyAfterJournalSync(t *testing.T) {
	order := make([]string, 0, 2)
	store := &fakeJournal{appendFn: func(domain.EventBatch) (journal.AppendResult, error) {
		order = append(order, "sync")
		return journal.AppendResult{Seq: 1}, nil
	}}
	delivery := &fakeDelivery{raw: []byte("raw"), id: "m1", subject: "subject", ackFn: func() error { order = append(order, "ack"); return nil }}
	handler := NewHandler(&fakeDecoder{batch: domain.EventBatch{MessageID: "m1"}, decision: DecisionArchive}, store, &fakeNotifier{})
	if err := handler.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, []string{"sync", "ack"}, order)
}

func TestHandlerQuarantinesInvalidEventAndTerminates(t *testing.T) {
	store := &fakeJournal{quarantineFn: func(journal.QuarantineRecord) error { return nil }}
	delivery := &fakeDelivery{raw: []byte("bad"), id: "bad", subject: "subject"}
	handler := NewHandler(&fakeDecoder{decision: DecisionReject, decodeErr: errors.New("invalid row")}, store, &fakeNotifier{})
	assert.ErrorContains(t, handler.Handle(context.Background(), delivery), "invalid row")
	assert.True(t, delivery.terminated)
}

func TestHandlerRetriesWhenQuarantineFails(t *testing.T) {
	store := &fakeJournal{quarantineFn: func(journal.QuarantineRecord) error {
		return errors.New("disk unavailable")
	}}
	delivery := &fakeDelivery{raw: []byte("bad"), id: "bad", subject: "subject"}
	handler := NewHandler(&fakeDecoder{decision: DecisionReject, decodeErr: errors.New("invalid row")}, store, nil)
	result := handler.HandleDecision(context.Background(), delivery)
	assert.Equal(t, jetstream.RETRY, result.Decision)
	assert.ErrorContains(t, result.Err, "disk unavailable")
	assert.False(t, delivery.terminated)
}

type fakeDecoder struct {
	batch     domain.EventBatch
	decision  Decision
	decodeErr error
}

func (f *fakeDecoder) DecodeEvent([]byte, string, string) (domain.EventBatch, Decision, error) {
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
	raw         []byte
	id, subject string
	ackFn       func() error
	terminated  bool
}

func (f *fakeDelivery) RawEnvelope() []byte    { return f.raw }
func (f *fakeDelivery) MessageID() string      { return f.id }
func (f *fakeDelivery) Subject() string        { return f.subject }
func (f *fakeDelivery) ContentType() string    { return events.ContentType }
func (f *fakeDelivery) StreamSequence() uint64 { return 1 }
func (f *fakeDelivery) DeliveryCount() uint64  { return 1 }
func (f *fakeDelivery) Ack(context.Context) error {
	if f.ackFn != nil {
		return f.ackFn()
	}
	return nil
}
func (f *fakeDelivery) Nak(context.Context, time.Duration) error { return nil }
func (f *fakeDelivery) InProgress(context.Context) error         { return nil }
func (f *fakeDelivery) Term(context.Context) error               { f.terminated = true; return nil }

func TestRetryDelay(t *testing.T) {
	assert.Equal(t, time.Second, retryDelay(0))
	assert.Equal(t, 2*time.Second, retryDelay(2))
	assert.Equal(t, 30*time.Second, retryDelay(100))
}
func TestRetryScheduledError(t *testing.T) {
	assert.Contains(t, (&RetryScheduledError{Delay: 2 * time.Second}).Error(), "2s")
}
