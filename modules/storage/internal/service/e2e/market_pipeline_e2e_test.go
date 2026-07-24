//go:build cgo

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestMarketKlineToStorageOutboxE2E covers the market event path from the
// streamcalc process through PrimaryStore and the DataNode outbox to the
// storage fanout. Replaying the same source event after the write is committed
// must not create another DatasetRowsUpserted event.
func TestMarketKlineToStorageOutboxE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	defer server.Shutdown()
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_MARKET", Subjects: []string{"moox.market.>"}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{"moox.storage.>"}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddConsumer("MOOX_MARKET", &nats.ConsumerConfig{Name: "streamcalc_kline_v1", Durable: "streamcalc_kline_v1", FilterSubject: "moox.market.tick.received.v1.>", AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddConsumer("MOOX_MARKET", &nats.ConsumerConfig{Name: "streamcalc_kline_output_v1", Durable: "streamcalc_kline_output_v1", FilterSubject: "moox.market.kline.closed.v1.>", AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddConsumer("MOOX_MARKET", &nats.ConsumerConfig{Name: "storage_primary_kline_v1", Durable: "storage_primary_kline_v1", FilterSubject: "moox.market.kline.closed.v1.>", AckPolicy: nats.AckExplicitPolicy, AckWait: 100 * time.Millisecond, MaxDeliver: 3, MaxAckPending: 8, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
		t.Fatal(err)
	}
	for _, durable := range []string{"storage_view", "factor_calc", "moox_archive_kline_v1"} {
		if _, err := js.AddConsumer("MOOX_STORAGE", &nats.ConsumerConfig{Name: durable, Durable: durable, FilterSubject: "moox.storage.dataset.rows.upserted.v1.>", AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
			t.Fatal(err)
		}
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "storage-market-pipeline-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	inputClient, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "storage-market-input-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer inputClient.Close()
	inputConsumer, err := events.NewConsumer(inputClient, jetstream.ConsumerBindRef{Stream: "MOOX_MARKET", Durable: "storage_primary_kline_v1", FetchMaxWait: 50 * time.Millisecond}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer inputConsumer.Close()
	streamcalcOutputConsumer, err := events.NewConsumer(client, jetstream.ConsumerBindRef{Stream: "MOOX_MARKET", Durable: "streamcalc_kline_output_v1", FetchMaxWait: 50 * time.Millisecond}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer streamcalcOutputConsumer.Close()
	outputs := make(map[string]*events.Consumer)
	for _, durable := range []string{"storage_view", "factor_calc", "moox_archive_kline_v1"} {
		consumer, err := events.NewConsumer(client, jetstream.ConsumerBindRef{Stream: "MOOX_STORAGE", Durable: durable, FetchMaxWait: 50 * time.Millisecond}, registry)
		if err != nil {
			t.Fatal(err)
		}
		defer consumer.Close()
		outputs[durable] = consumer
	}

	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "node"), NodeID: "market-node"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := datanode.NewService(datanode.Options{NodeID: "market-node", AuthSecret: "market-secret", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	ackFailureNode := &ackFailureNode{DataNodeRuntimeService: node, closeInput: inputClient.Close}
	primary, err := primarystore.New(primarystore.Options{Node: ackFailureNode})
	if err != nil {
		t.Fatal(err)
	}
	klineConsumer, err := primarystore.NewKlineConsumer(inputConsumer, primary, publisher, "market_kline_5m", &pb.AuthInfo{AppId: "storage-streamcalc", AppKey: datanode.ServiceAuthKey("market-secret", "storage-streamcalc")})
	if err != nil {
		t.Fatal(err)
	}
	consumerCtx, cancelConsumer := context.WithCancel(ctx)
	defer cancelConsumer()
	consumerDone := make(chan error, 1)
	go func() { consumerDone <- klineConsumer.Run(consumerCtx, 1) }()

	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	streamcalcLog := startStreamcalc(t, server, server.ClientURL())
	for i := 0; i < 5; i++ {
		tradeTime := base.Add(time.Duration(i)*time.Minute + 10*time.Second)
		payload := &marketpb.Tick{Exchange: "binance", TradeId: fmt.Sprintf("trade-%d", i), Symbol: "BTC-USDT", Price: float64(100 + i), Quantity: 1, TradeTime: timestamppb.New(tradeTime)}
		if _, err := publisher.Publish(ctx, events.TickReceived, payload, events.PublishOptions{EventID: fmt.Sprintf("binance:BTC-USDT:%d", i), OccurredAt: tradeTime, SpaceID: "crypto", SubjectID: payload.GetSymbol()}); err != nil {
			t.Fatal(err)
		}
	}
	outputDeliveries := fetchOneEventually(t, ctx, streamcalcOutputConsumer, streamcalcLog)
	output, ok := outputDeliveries[0].Payload.(*marketpb.KlineClosed)
	if !ok || output.GetFrequency() != "5m" || output.GetTradeCount() != 5 || output.GetVolume() != 5 {
		t.Fatalf("streamcalc output payload=%#v", outputDeliveries[0].Payload)
	}
	if err := outputDeliveries[0].Delivery.Ack(ctx); err != nil {
		t.Fatal(err)
	}
	waitForOutbox(t, store, 1)
	entries, err := store.ListOutbox(ctx, 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}

	select {
	case <-consumerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("first kline consumer did not stop after forced ACK failure")
	}

	// The first consumer wrote the row and outbox, then its connection was
	// closed before ACK. A fresh bind to the same durable must receive the
	// redelivery and turn it into a marker-only no-op.
	recoveryClient, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "storage-market-recovery-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryClient.Close()
	recoveryConsumer, err := events.NewConsumer(recoveryClient, jetstream.ConsumerBindRef{Stream: "MOOX_MARKET", Durable: "storage_primary_kline_v1", FetchMaxWait: 50 * time.Millisecond}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryConsumer.Close()
	recoveredKlineConsumer, err := primarystore.NewKlineConsumer(recoveryConsumer, primary, publisher, "market_kline_5m", &pb.AuthInfo{AppId: "storage-streamcalc", AppKey: datanode.ServiceAuthKey("market-secret", "storage-streamcalc")})
	if err != nil {
		t.Fatal(err)
	}
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- recoveredKlineConsumer.Run(consumerCtx, 1) }()
	waitForKlineRedelivery(t, js)
	entries, err = store.ListOutbox(ctx, 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("redelivery created duplicate outbox entries=%v err=%v", entries, err)
	}
	cancelConsumer()
	select {
	case <-recoveryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery kline consumer did not stop")
	}

	relay := mustOutboxRelay(t, store, eventconsumer.NewDatasetPublisher(client, "market-node"))
	if err := relay.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	for durable, consumer := range outputs {
		deliveries := fetchOne(t, ctx, consumer)
		if _, ok := deliveries[0].Payload.(*storagepb.DatasetRowsUpserted); !ok {
			t.Fatalf("%s payload=%T", durable, deliveries[0].Payload)
		}
		if err := deliveries[0].Delivery.Ack(ctx); err != nil {
			t.Fatalf("ack %s: %v", durable, err)
		}
	}
}

type ackFailureNode struct {
	pb.DataNodeRuntimeService
	closeOnce  sync.Once
	closeInput func() error
}

func (n *ackFailureNode) WriteFields(ctx context.Context, req *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
	rsp, err := n.DataNodeRuntimeService.WriteFields(ctx, req)
	if err == nil && rsp != nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		n.closeOnce.Do(func() { _ = n.closeInput() })
	}
	return rsp, err
}

func waitForKlineRedelivery(t *testing.T, js nats.JetStreamContext) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := js.ConsumerInfo("MOOX_MARKET", "storage_primary_kline_v1")
		if err == nil && info.Delivered.Consumer >= 2 && info.AckFloor.Consumer >= 2 && info.NumAckPending == 0 && info.NumPending == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := js.ConsumerInfo("MOOX_MARKET", "storage_primary_kline_v1")
	t.Fatalf("kline redelivery was not ACKed without duplicate outbox: info=%+v err=%v", info, err)
}

func startStreamcalc(t *testing.T, server *natsserver.Server, natsURL string) func() string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate market pipeline test")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../../.."))
	streamcalcDir := filepath.Join(repoRoot, "modules", "streamcalc")
	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binaryPath := filepath.Join(t.TempDir(), "streamcalc")
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binaryPath, "./cmd/server")
	build.Dir = streamcalcDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build streamcalc: %v\n%s", err, output)
	}
	configPath := filepath.Join(t.TempDir(), "streamcalc.yaml")
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	config := fmt.Sprintf(`eventbus:
  urls: [%q]
  stream: MOOX_MARKET
  durable: streamcalc_kline_v1
  fetch_batch: 32
  fetch_max_wait: 50ms

aggregation:
  input_frequency: 1m
  target_frequency: 5m
  allowed_lateness: 30s

state:
  checkpoint_path: %q
`, natsURL, checkpointPath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binaryPath)
	cmd.Dir = streamcalcDir
	env := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "MOOX_EVENTBUS_") || strings.HasPrefix(value, "MOOX_STREAMCALC_CONFIG=") || strings.HasPrefix(value, "NATS_URL=") {
			continue
		}
		env = append(env, value)
	}
	cmd.Env = append(env, "MOOX_STREAMCALC_CONFIG="+configPath, "MOOX_EVENTBUS_NATS_URL="+natsURL)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(waitDone)
	}()
	stop := func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-waitDone
		}
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-waitDone:
			t.Fatalf("streamcalc exited during startup: %v\n%s", waitErr, output.String())
		default:
		}
		if server.NumClients() >= 3 {
			return output.String
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("streamcalc did not connect to embedded NATS; clients=%d log=%s", server.NumClients(), output.String())
	return output.String
}

func waitForOutbox(t *testing.T, store *pebble.Store, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := store.ListOutbox(context.Background(), 0, want+1)
		if err == nil && len(entries) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, err := store.ListOutbox(context.Background(), 0, want+1)
	t.Fatalf("outbox did not reach %d: entries=%v err=%v", want, entries, err)
}

func mustOutboxRelay(t *testing.T, store *pebble.Store, publisher *eventconsumer.DatasetPublisher) *datanode.OutboxRelay {
	t.Helper()
	relay, err := datanode.NewOutboxRelay(store, publisher, datanode.OutboxRelayOptions{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func fetchOne(t *testing.T, parent context.Context, consumer *events.Consumer) []*events.EventDelivery {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	deliveries, err := consumer.Fetch(ctx, 1)
	if errors.Is(err, nats.ErrTimeout) && len(deliveries) == 0 {
		t.Fatal("timed out fetching downstream event")
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("downstream deliveries=%d", len(deliveries))
	}
	return deliveries
}

func fetchOneEventually(t *testing.T, parent context.Context, consumer *events.Consumer, processLog func() string) []*events.EventDelivery {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		deliveries, err := consumer.Fetch(parent, 1)
		if len(deliveries) == 1 {
			return deliveries
		}
		if err != nil && !errors.Is(err, nats.ErrTimeout) {
			t.Fatalf("fetch event: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out fetching event; process log=%s", processLog())
	return nil
}
