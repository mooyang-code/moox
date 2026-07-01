package nats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	natsserver "github.com/nats-io/nats-server/v2/server"
	natslib "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestEnsureStreamUpdatesWhenStreamAlreadyExists(t *testing.T) {
	manager := &fakeStreamManager{addErr: natslib.ErrStreamNameAlreadyInUse}
	cfg := &natslib.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.storage.>"}}

	err := ensureStream(manager, cfg)

	require.NoError(t, err)
	require.Equal(t, 1, manager.adds)
	require.Equal(t, 1, manager.updates)
	require.Equal(t, cfg, manager.updatedConfig)
}

func TestEnsureStreamReturnsAddError(t *testing.T) {
	wantErr := errors.New("bad subjects")
	manager := &fakeStreamManager{addErr: wantErr}

	err := ensureStream(manager, &natslib.StreamConfig{Name: "MOOX_STORAGE"})

	require.ErrorIs(t, err, wantErr)
	require.Zero(t, manager.updates)
}

func TestDurableConsumerNameDerivesSubjectKind(t *testing.T) {
	require.Equal(t,
		"storage_deriver_time_series_rows_changed_v1",
		durableConsumerName("storage_deriver", "moox.storage.time_series.rows_changed.v1"),
	)
	require.Equal(t,
		"storage_deriver_record_rows_changed_v1",
		durableConsumerName("storage_deriver", "moox.storage.record.rows_changed.v1"),
	)
}

func TestDurableConsumerNameSanitizesBaseAndSubject(t *testing.T) {
	require.Equal(t,
		"storage_deriver_record_rows_changed_v1",
		durableConsumerName("", "moox.storage.record.rows_changed.v1"),
	)
	require.Equal(t,
		"storage_deriver_us_east_time_series_rows_changed_v1",
		durableConsumerName(" storage-deriver.us/east ", "moox.storage.time_series.rows_changed.v1"),
	)
}

func TestSubscriberConsumesOnlyEventsCreatedAfterSubscribe(t *testing.T) {
	ctx := context.Background()
	srv := startTestNATSServer(t)
	producer, err := NewProducer(transport.ProducerOptions{
		ServerURL:      srv.ClientURL(),
		StreamName:     "MOOX_STORAGE",
		StreamSubjects: []string{"moox.storage.>"},
		ConsumerName:   "storage_view",
	})
	require.NoError(t, err)
	require.NoError(t, producer.Connect(ctx))
	t.Cleanup(func() {
		require.NoError(t, producer.Close())
	})

	subject := "moox.storage.time_series.rows_changed.v1"
	require.NoError(t, producer.Send(ctx, &transport.Message{Subject: subject, Data: []byte("historical")}))

	received := make(chan string, 2)
	sub, err := producer.(transport.Subscriber).Subscribe(ctx, subject, func(_ context.Context, msg *transport.Message) error {
		received <- string(msg.Data)
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sub.Close())
	})

	require.NoError(t, producer.Send(ctx, &transport.Message{Subject: subject, Data: []byte("new")}))

	select {
	case got := <-received:
		require.Equal(t, "new", got)
	case <-time.After(3 * time.Second):
		t.Fatal("expected new event after subscription")
	}
	select {
	case got := <-received:
		t.Fatalf("unexpected extra event: %s", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func startTestNATSServer(t *testing.T) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoSigs:    true,
		NoLog:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(3*time.Second))
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	return srv
}

// fakeStreamManager 是 NATS 生产者测试使用的流管理桩。
type fakeStreamManager struct {
	adds          int
	updates       int
	addErr        error
	updatedConfig *natslib.StreamConfig
}

func (m *fakeStreamManager) AddStream(cfg *natslib.StreamConfig, opts ...natslib.JSOpt) (*natslib.StreamInfo, error) {
	_ = cfg
	_ = opts
	m.adds++
	if m.addErr != nil {
		return nil, m.addErr
	}
	return &natslib.StreamInfo{}, nil
}

func (m *fakeStreamManager) UpdateStream(cfg *natslib.StreamConfig, opts ...natslib.JSOpt) (*natslib.StreamInfo, error) {
	_ = opts
	m.updates++
	m.updatedConfig = cfg
	return &natslib.StreamInfo{}, nil
}
