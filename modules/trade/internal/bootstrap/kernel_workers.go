package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	tradebus "github.com/mooyang-code/moox/modules/trade/internal/infra/bus"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"trpc.group/trpc-go/trpc-go/log"
)

func startKernelWorkers(ctx context.Context, cfg config.EventBusConfig, s *store.Store, e *command.Engine) error {
	if !cfg.Enabled {
		return nil
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(cfg.URLs, "moox-trade"))
	if err != nil {
		return err
	}
	setKernelEventBusClient(client)
	relay := tradebus.Relay{Store: s, Publisher: client, InstanceID: "trade", BootID: time.Now().UTC().Format(time.RFC3339Nano)}
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = client.Close()
				return
			case <-ticker.C:
				if err := relay.RunOnce(ctx, 100); err != nil {
					log.WarnContextf(ctx, "trade outbox relay: %v", err)
				}
			}
		}
	}()
	go runExecutionConsumer(ctx, client, cfg, s, e)
	go runRebalanceConsumer(ctx, client, cfg, s, e)
	go runProgressConsumer(ctx, client, cfg, s, e)
	go runReconciliationConsumer(ctx, client, cfg, s, e)
	go runPrivateStreamSupervisor(ctx, s, e)
	go runRecoveryLoop(ctx, s, e)
	go runFillReconciliation(ctx, s, e)
	return nil
}

func runPrivateStreamSupervisor(ctx context.Context, s *store.Store, e *command.Engine) {
	type streamEntry struct {
		cancel     context.CancelFunc
		generation uint64
	}
	active := map[string]streamEntry{}
	var generation uint64
	var mu sync.Mutex
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			orders, err := s.ListOpenOrders(ctx, 500)
			if err != nil {
				log.WarnContextf(ctx, "discover trade private streams: %v", err)
				continue
			}
			expected := map[string]bool{}
			for _, row := range orders {
				expected[row.SpaceID+":"+row.ChannelID] = true
			}
			telemetry.SetPrivateExpected(expected)
			mu.Lock()
			for key, entry := range active {
				if !expected[key] {
					entry.cancel()
					delete(active, key)
					telemetry.SetPrivateConnected(key, false)
				}
			}
			mu.Unlock()
			for _, row := range orders {
				key := row.SpaceID + ":" + row.ChannelID
				mu.Lock()
				if _, exists := active[key]; exists {
					mu.Unlock()
					continue
				}
				streamCtx, cancel := context.WithCancel(ctx)
				generation++
				myGeneration := generation
				active[key] = streamEntry{cancel: cancel, generation: myGeneration}
				mu.Unlock()
				row := row
				go func() {
					defer func() {
						mu.Lock()
						if current, ok := active[key]; ok && current.generation == myGeneration {
							delete(active, key)
							telemetry.SetPrivateConnected(key, false)
						}
						mu.Unlock()
					}()
					adapter, err := e.AdapterFor(ctx, row)
					if err != nil {
						log.WarnContextf(ctx, "resolve private stream %s: %v", key, err)
						return
					}
					streamCtx = exchange.WithPrivateStreamState(streamCtx, func(ready bool) {
						mu.Lock()
						current, ok := active[key]
						if ok && current.generation == myGeneration {
							telemetry.SetPrivateConnected(key, ready)
						}
						mu.Unlock()
					})
					exchangeLabel := "configured"
					if named, ok := adapter.(interface{ ExchangeName() string }); ok {
						exchangeLabel = named.ExchangeName()
					}
					err = adapter.SubscribePrivate(streamCtx, func(eventCtx context.Context, fill exchange.FillEvent) error {
						orderRow, lookupErr := s.GetOrderForPrivateFill(eventCtx, row.SpaceID, row.ChannelID, fill.Symbol, fill.ExchangeOrderID)
						if lookupErr != nil {
							return lookupErr
						}
						fill.BaseAsset, fill.QuoteAsset = orderRow.BaseAsset, orderRow.QuoteAsset
						telemetry.MarkPrivateEvent(exchangeLabel, time.Now())
						_, handleErr := (consumer.FillHandler{Store: s}).HandleSource(eventCtx, orderRow.SpaceID, orderRow.AccountID, orderRow.OrderID, fill.ExchangeTradeID, fill, "private_stream")
						return handleErr
					})
					if err != nil && ctx.Err() == nil {
						log.WarnContextf(ctx, "private stream %s disconnected: %v", key, err)
					}
				}()
			}
		}
	}
}

func advanceActiveRebalances(ctx context.Context, s *store.Store, e *command.Engine) error {
	runs, err := s.ListActiveRebalanceRuns(ctx, 100)
	if err != nil {
		return err
	}
	service := rebalanceapp.Service{Store: s, Engine: e}
	for _, run := range runs {
		if _, err := service.Advance(ctx, run.SpaceID, run.RunID, run.AccountID, run.ChannelID); err != nil {
			return err
		}
	}
	return nil
}

func runProgressConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	var pull *jetstream.PullConsumer
	for ctx.Err() == nil {
		if pull == nil {
			p, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.ProgressDurable, FilterSubject: "moox.trade.fill.received.v1", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy})
			if err != nil {
				log.WarnContextf(ctx, "bind trade progress consumer: %v", err)
				time.Sleep(time.Second)
				continue
			}
			pull = p
		}
		deliveries, err := pull.Fetch(ctx, 16)
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			log.WarnContextf(ctx, "fetch trade progress: %v", err)
			time.Sleep(time.Second)
			continue
		}
		for _, delivery := range deliveries {
			if err := advanceActiveRebalances(ctx, s, e); err != nil {
				_ = delivery.Nak(ctx, time.Second)
				continue
			}
			if _, err := s.RecordInbox(ctx, cfg.ProgressDurable, delivery.Message.MessageId, delivery.Message.Topic); err != nil {
				_ = delivery.Nak(ctx, time.Second)
				continue
			}
			_ = delivery.Ack(ctx)
		}
	}
}

func runReconciliationConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	var pull *jetstream.PullConsumer
	for ctx.Err() == nil {
		if pull == nil {
			p, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.ReconciliationDurable, FilterSubject: "moox.trade.reconciliation.requested.v1", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy})
			if err != nil {
				log.WarnContextf(ctx, "bind trade reconciliation consumer: %v", err)
				time.Sleep(time.Second)
				continue
			}
			pull = p
		}
		deliveries, err := pull.Fetch(ctx, 8)
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			log.WarnContextf(ctx, "fetch trade reconciliation: %v", err)
			time.Sleep(time.Second)
			continue
		}
		for _, delivery := range deliveries {
			deliveryCtx := deliveryTraceContext(ctx, delivery)
			started := time.Now()
			var wrapped wrapperspb.BytesValue
			var scope struct {
				SpaceID   string `json:"space_id"`
				AccountID string `json:"account_id"`
				ChannelID string `json:"channel_id"`
			}
			err := proto.Unmarshal(delivery.Message.Payload, &wrapped)
			if err == nil {
				err = json.Unmarshal(wrapped.Value, &scope)
			}
			if err == nil && scope.SpaceID == "" {
				err = errors.New("trade: reconciliation space is required")
			}
			if err == nil {
				err = reconcileOrdersOnce(deliveryCtx, s, e, scope.SpaceID, scope.AccountID, scope.ChannelID)
			}
			telemetry.OperationLatency.WithLabelValues("reconcile").Observe(time.Since(started).Seconds())
			if err != nil {
				_ = delivery.Nak(ctx, time.Second)
				continue
			}
			if _, err = s.RecordInbox(deliveryCtx, cfg.ReconciliationDurable, delivery.Message.MessageId, delivery.Message.Topic); err != nil {
				_ = delivery.Nak(ctx, time.Second)
				continue
			}
			_ = delivery.Ack(ctx)
		}
	}
}

func runRebalanceConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	var pull *jetstream.PullConsumer
	for ctx.Err() == nil {
		if pull == nil {
			p, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.RebalanceDurable, FilterSubject: "moox.trade.rebalance.requested.v1", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy})
			if err != nil {
				log.WarnContextf(ctx, "bind trade rebalance consumer: %v", err)
				time.Sleep(time.Second)
				continue
			}
			pull = p
		}
		deliveries, err := pull.Fetch(ctx, 16)
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			log.WarnContextf(ctx, "fetch trade rebalance: %v", err)
			time.Sleep(time.Second)
			continue
		}
		for _, delivery := range deliveries {
			if err := handleRebalanceDelivery(ctx, delivery, s, e, cfg.RebalanceDurable); err != nil {
				_ = delivery.Nak(ctx, time.Second)
				continue
			}
			_ = delivery.Ack(ctx)
		}
	}
}

func handleRebalanceDelivery(ctx context.Context, delivery *jetstream.Delivery, s *store.Store, e *command.Engine, consumerName string) error {
	if delivery == nil || delivery.Message == nil {
		return jetstream.ErrInvalidDelivery
	}
	ctx = deliveryTraceContext(ctx, delivery)
	var wrapped wrapperspb.BytesValue
	if err := proto.Unmarshal(delivery.Message.Payload, &wrapped); err != nil {
		return err
	}
	var run store.RebalanceRunRecord
	if err := json.Unmarshal(wrapped.Value, &run); err != nil {
		return err
	}
	if _, err := (rebalanceapp.Service{Store: s, Engine: e}).Advance(ctx, run.SpaceID, run.RunID, run.AccountID, run.ChannelID); err != nil {
		return err
	}
	_, err := s.RecordInbox(ctx, consumerName, delivery.Message.MessageId, delivery.Message.Topic)
	return err
}

func runFillReconciliation(ctx context.Context, s *store.Store, e *command.Engine) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcileOrdersOnce(ctx, s, e, "", "", ""); err != nil {
				log.WarnContextf(ctx, "trade fill reconciliation scan: %v", err)
			}
		}
	}
}

func reconcileOrdersOnce(ctx context.Context, s *store.Store, e *command.Engine, space, account, channel string) error {
	handler := consumer.FillHandler{Store: s}
	orders, err := s.ListOpenOrdersScoped(ctx, space, account, channel, 200)
	if err != nil {
		return err
	}
	for _, o := range orders {
		adapter, resolveErr := e.AdapterFor(ctx, o)
		if resolveErr != nil {
			return resolveErr
		}
		fills, listErr := adapter.ListFills(ctx, o.Symbol, o.ExchangeOrderID)
		if listErr != nil {
			return listErr
		}
		for _, f := range fills {
			if f.ExchangeTradeID == "" {
				continue
			}
			if _, err := handler.HandleSource(ctx, o.SpaceID, o.AccountID, o.OrderID, f.ExchangeTradeID, f, "reconciliation"); err != nil {
				return err
			}
		}
		state, stateErr := adapter.QueryByClientOrderID(ctx, o.Symbol, o.ClientOrderID)
		if stateErr == nil && (state.Status == "CANCELED" || state.Status == "REJECTED" || state.Status == "EXPIRED") {
			if _, err := e.ReconcileExchangeTerminal(ctx, o.SpaceID, o.OrderID, state.Status); err != nil {
				return err
			}
		}
	}
	return nil
}

func runRecoveryLoop(ctx context.Context, s *store.Store, e *command.Engine) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	worker := consumer.SubmissionWorker{Engine: e}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			orders, err := s.ListRecoverableOrders(ctx, 100)
			if err != nil {
				log.WarnContextf(ctx, "trade recovery scan: %v", err)
				continue
			}
			for _, o := range orders {
				var runErr error
				switch o.State {
				case "READY", "SUBMITTING", "SUBMIT_UNKNOWN":
					_, runErr = worker.Handle(ctx, o.SpaceID, o.OrderID)
				case "CANCELING":
					_, runErr = e.RecoverCanceling(ctx, o.SpaceID, o.OrderID)
				case "CANCEL_UNKNOWN":
					_, runErr = e.ResolveCancelUnknown(ctx, o.SpaceID, o.OrderID)
				}
				if runErr != nil {
					log.WarnContextf(ctx, "trade recovery order %s: %v", o.OrderID, runErr)
				}
			}
			sagas, sagaErr := s.ListRecoverableSagas(ctx, 100)
			if sagaErr != nil {
				log.WarnContextf(ctx, "trade saga recovery scan: %v", sagaErr)
				continue
			}
			for _, sg := range sagas {
				if _, err := e.ResumeReplace(ctx, sg.SpaceID, sg.SagaID); err != nil {
					log.WarnContextf(ctx, "trade saga %s recovery: %v", sg.SagaID, err)
				}
			}
			runs, runErr := s.ListActiveRebalanceRuns(ctx, 100)
			if runErr != nil {
				log.WarnContextf(ctx, "trade rebalance recovery scan: %v", runErr)
				continue
			}
			svc := rebalanceapp.Service{Store: s, Engine: e}
			for _, run := range runs {
				if _, err := svc.Advance(ctx, run.SpaceID, run.RunID, run.AccountID, run.ChannelID); err != nil {
					log.WarnContextf(ctx, "trade rebalance %s recovery: %v", run.RunID, err)
				}
			}
		}
	}
}

func runExecutionConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	var pull *jetstream.PullConsumer
	for ctx.Err() == nil {
		if pull == nil {
			p, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.ExecutionDurable, FilterSubject: "moox.trade.execution.slice_ready.v1", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 256, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy})
			if err != nil {
				log.WarnContextf(ctx, "bind trade execution consumer: %v", err)
				time.Sleep(time.Second)
				continue
			}
			pull = p
		}
		ds, err := pull.Fetch(ctx, 32)
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			log.WarnContextf(ctx, "fetch trade execution: %v", err)
			time.Sleep(time.Second)
			continue
		}
		for _, d := range ds {
			if err := handleExecutionDelivery(ctx, d, s, e, cfg.ExecutionDurable); err != nil {
				_ = d.Nak(ctx, time.Second)
				continue
			}
			_ = d.Ack(ctx)
		}
	}
}

func handleExecutionDelivery(ctx context.Context, d *jetstream.Delivery, s *store.Store, e *command.Engine, consumerName string) error {
	if d == nil || d.Message == nil {
		return jetstream.ErrInvalidDelivery
	}
	ctx = deliveryTraceContext(ctx, d)
	var wrapped wrapperspb.BytesValue
	if err := proto.Unmarshal(d.Message.Payload, &wrapped); err != nil {
		return err
	}
	var r store.OrderRecord
	if err := json.Unmarshal(wrapped.Value, &r); err != nil {
		return err
	}
	worker := consumer.SubmissionWorker{Engine: e}
	if _, err := worker.Handle(ctx, r.SpaceID, r.OrderID); err != nil {
		return err
	}
	if saga, err := s.GetSagaByReplacementOrder(ctx, r.SpaceID, r.OrderID); err == nil {
		if _, err := e.ResumeReplace(ctx, saga.SpaceID, saga.SagaID); err != nil {
			return err
		}
	}
	if err := advanceActiveRebalances(ctx, s, e); err != nil {
		return err
	}
	_, err := s.RecordInbox(ctx, consumerName, d.Message.MessageId, d.Message.Topic)
	return err
}

func deliveryTraceContext(ctx context.Context, delivery *jetstream.Delivery) context.Context {
	if delivery == nil || delivery.Message == nil || delivery.Message.Trace == nil {
		return ctx
	}
	return telemetry.WithTrace(ctx, telemetry.Trace{TraceID: delivery.Message.Trace.TraceId, RequestID: delivery.Message.Trace.RequestId})
}
