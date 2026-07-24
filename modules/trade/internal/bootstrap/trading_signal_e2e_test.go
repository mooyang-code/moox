package bootstrap

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/tradingpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	nserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTradingSignalConsumerPersistsGovernedEventE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server, err := nserver.NewServer(&nserver.Options{JetStream: true, Port: -1, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go server.Start()
	require.True(t, server.ReadyForConnections(5*time.Second))
	defer server.Shutdown()
	clientURL := strings.Replace(server.ClientURL(), "0.0.0.0", "127.0.0.1", 1)
	nc, err := nats.Connect(clientURL)
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "MOOX_TRADE", Subjects: []string{"moox.trading.>"}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	_, err = js.AddConsumer("MOOX_TRADE", &nats.ConsumerConfig{
		Durable: "trade_trading_signal_v1", FilterSubject: "moox.trading.signal.v1.>",
		AckPolicy: nats.AckExplicitPolicy, AckWait: time.Minute, MaxDeliver: -1,
		MaxAckPending: 256, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.NoError(t, err)

	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{clientURL}, Name: "trade-signal-e2e"})
	require.NoError(t, err)
	defer client.Close()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	workerCtx, workerCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		runTradingSignalConsumer(workerCtx, client, config.EventBusConfig{Enabled: true, Stream: "MOOX_TRADE", TradingSignalDurable: "trade_trading_signal_v1"}, s)
		close(done)
	}()

	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	signal := &tradingpb.TradingSignal{
		StrategyId: "strategy-e2e", SignalId: "signal-e2e", Symbol: "BTC-USDT",
		Side: tradingpb.SignalSide_SIGNAL_SIDE_BUY, Action: tradingpb.SignalAction_SIGNAL_ACTION_OPEN,
		SignalTime: timestamppb.Now(),
	}
	_, err = publisher.PublishTradingSignal(ctx, signal, events.PublishOptions{
		EventID: "event-signal-e2e", OccurredAt: time.Now().UTC(), SpaceID: "space-e2e",
	})
	require.NoError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		err = s.DBForTest().Raw("SELECT COUNT(1) FROM t_trade_signal_recommendations WHERE c_event_id=?", "event-signal-e2e").Scan(&count).Error
		require.NoError(t, err)
		if count == 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	var count int64
	require.NoError(t, s.DBForTest().Raw("SELECT COUNT(1) FROM t_trade_signal_recommendations WHERE c_event_id=?", "event-signal-e2e").Scan(&count).Error)
	require.Equal(t, int64(1), count)

	workerCancel()
	<-done
}
