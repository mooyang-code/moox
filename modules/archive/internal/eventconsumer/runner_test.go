package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeConsumer struct {
	batches [][]*jetstream.Delivery
	err     error
	closed  bool
}

func (f *fakeConsumer) Fetch(_ context.Context, _ int) ([]*jetstream.Delivery, error) {
	if len(f.batches) == 0 {
		return nil, f.err
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, f.err
}

func (f *fakeConsumer) Close() error {
	f.closed = true
	return nil
}

func TestRunnerStopsNormallyOnCancel(t *testing.T) {
	delivery := &jetstream.Delivery{RawMessageID: "m1", RawData: []byte("raw"), Subject: "topic"}
	consumer := &fakeConsumer{
		batches: [][]*jetstream.Delivery{{delivery}},
		err:     nats.ErrTimeout,
	}
	store := &fakeJournal{appendFn: func(domain.EventBatch) (journal.AppendResult, error) {
		return journal.AppendResult{Seq: 1}, nil
	}}
	handler := NewHandler(&fakeDecoder{
		batch:    domain.EventBatch{MessageID: "m1", Rows: []domain.RowPatch{{Partition: domain.PartitionKey{SpaceID: "crypto", DatasetID: "spot_kline_1m", SubjectID: "BTC", Freq: "1m", Month: "202601"}}}},
		decision: DecisionArchive,
	}, store, nil)
	runner := NewRunner(consumer, handler, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runner.Run(ctx))
}

func TestRunnerRetriesAfterScheduledRetry(t *testing.T) {
	delivery := &jetstream.Delivery{RawMessageID: "m1", RawData: []byte("raw"), Subject: "topic", DeliveryCount: 2}
	consumer := &fakeConsumer{
		batches: [][]*jetstream.Delivery{{delivery}, {}},
		err:     nats.ErrTimeout,
	}
	store := &fakeJournal{appendFn: func(domain.EventBatch) (journal.AppendResult, error) {
		return journal.AppendResult{}, errors.New("journal unavailable")
	}}
	handler := NewHandler(&fakeDecoder{
		batch:    domain.EventBatch{MessageID: "m1", Rows: []domain.RowPatch{{Partition: domain.PartitionKey{SpaceID: "crypto", DatasetID: "spot_kline_1m", SubjectID: "BTC", Freq: "1m", Month: "202601"}}}},
		decision: DecisionArchive,
	}, store, nil)
	runner := NewRunner(consumer, handler, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := runner.Run(ctx)
	require.Error(t, err)
}

func TestDeliveryAdapter(t *testing.T) {
	d := deliveryAdapter{Delivery: &jetstream.Delivery{
		RawMessageID: "mid-1", RawData: []byte("raw"), Subject: "topic", StreamSeq: 9, DeliveryCount: 3,
	}}
	assert.Equal(t, []byte("raw"), d.RawEnvelope())
	assert.Equal(t, "mid-1", d.MessageID())
	assert.Equal(t, "topic", d.Subject())
	assert.Equal(t, uint64(9), d.StreamSequence())
	assert.Equal(t, uint64(3), d.DeliveryCount())
}

func TestNewRunnerDefaultsBatchSize(t *testing.T) {
	runner := NewRunner(&fakeConsumer{}, NewHandler(&fakeDecoder{}, &fakeJournal{}, nil), 0)
	require.NotNil(t, runner)
	assert.Equal(t, 1, runner.batch)
}
