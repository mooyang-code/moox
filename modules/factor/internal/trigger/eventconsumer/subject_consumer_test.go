package eventconsumer

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSubjectConsumerUsesIndependentBoundedDeliveries(t *testing.T) {
	cfg := trigger.DefaultSubjectBatchConfig()
	ref := subjectConsumerConfig(Config{FetchMaxWait: time.Second}, cfg)
	require.Equal(t, events.ViewSourceSubjectReady.Name(), ref.Event.Name())
	require.Equal(t, nats.DeliverNewPolicy, ref.DeliverPolicy)
	require.Equal(t, -1, ref.MaxDeliver)
	require.Equal(t, cfg.QueueCapacity+cfg.MaxBatch, ref.MaxAckPending)
	_, err := NewSubject(Config{}, trigger.SubjectBatchConfig{}, nil)
	require.Error(t, err)
	c, err := NewSubject(Config{}, cfg, func(context.Context, []trigger.SubjectEvent) error { return nil })
	require.NoError(t, err)
	session := &fakeNATSSession{run: func(ctx context.Context) error { <-ctx.Done(); return nil }}
	c.openSession = func(context.Context) (natsConsumerSession, error) { return session, nil }
	require.NoError(t, c.Start(context.Background()), "subject consumer must not require a period executor")
	require.NoError(t, c.Close())
	require.EqualValues(t, 1, session.closeCalls.Load())
}

func TestSubjectConsumerJetStreamMicrobatchAcknowledgesAfterExecution(t *testing.T) {
	server := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nc, err := nats.Connect(server.URL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.event.storage.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	entered := make(chan []trigger.SubjectEvent, 1)
	release := make(chan struct{})
	c, err := NewSubject(Config{URLs: []string{server.URL()}}, trigger.SubjectBatchConfig{Window: time.Second, MaxBatch: 2, QueueCapacity: 4, ExecutionTimeout: 5 * time.Second}, func(ctx context.Context, batch []trigger.SubjectEvent) error {
		entered <- batch
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	require.NoError(t, err)
	require.NoError(t, c.Start(ctx))
	defer c.Close()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	for _, subject := range []string{"BTC", "ETH"} {
		now := time.Now()
		payload := &storagepb.ViewSourceSubjectReady{SourceViewId: "prices", SourceDatasetId: "bars", SubjectId: subject, Frequency: "1m", PeriodTime: 60, ActiveIndexId: "index", InputContractVersion: "schema:1", SourceEventId: subject, SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1, ReadyAt: timestamppb.New(now)}
		encoded, err := registry.Encode(events.ViewSourceSubjectReady, payload, events.PublishOptions{EventID: subject, OccurredAt: now, SpaceID: "space", SubjectID: "prices"})
		require.NoError(t, err)
		raw, err := proto.Marshal(encoded.Message)
		require.NoError(t, err)
		msg := &nats.Msg{Subject: encoded.Subject, Data: raw, Header: nats.Header{}}
		msg.Header.Set("Content-Type", events.ContentType)
		msg.Header.Set(nats.MsgIdHdr, subject)
		_, err = js.PublishMsg(msg)
		require.NoError(t, err)
		if subject == "BTC" {
			require.Eventually(t, func() bool {
				info, err := js.ConsumerInfo("MOOX_STORAGE", ViewSourceSubjectConsumerName)
				return err == nil && info.NumAckPending == 1
			}, 500*time.Millisecond, time.Millisecond)
		}
	}
	select {
	case batch := <-entered:
		require.Len(t, batch, 2, "durable handlers must run concurrently to form a microbatch")
	case <-ctx.Done():
		t.Fatal("subject batch never arrived")
	}
	info, err := js.ConsumerInfo("MOOX_STORAGE", ViewSourceSubjectConsumerName)
	require.NoError(t, err)
	require.Equal(t, 2, info.NumAckPending)
	close(release)
	require.Eventually(t, func() bool {
		info, err := js.ConsumerInfo("MOOX_STORAGE", ViewSourceSubjectConsumerName)
		return err == nil && info.NumAckPending == 0
	}, 3*time.Second, 10*time.Millisecond)
}
