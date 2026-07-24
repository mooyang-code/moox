package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/application/reconciliation"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	tradebus "github.com/mooyang-code/moox/modules/trade/internal/infra/bus"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
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
	registry, err := events.DefaultRegistry()
	if err != nil {
		_ = client.Close()
		return err
	}
	eventPublisher, err := events.NewPublisher(client, registry)
	if err != nil {
		_ = client.Close()
		return err
	}
	relay := tradebus.Relay{Store: s, Publisher: eventPublisher, InstanceID: "trade", BootID: time.Now().UTC().Format(time.RFC3339Nano)}
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
	go runTradingSignalConsumer(ctx, client, cfg, s)
	go runPrivateStreamSupervisor(ctx, s, e)
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
	runTradePullConsumer(ctx, client, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.ProgressDurable, FilterSubject: "moox.trade.fill.received.v1.>", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy}, 16, "progress", func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		if err := advanceActiveRebalances(ctx, s, e); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		message, _, err := decodeTradeDelivery(delivery)
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		if _, err := s.RecordInbox(ctx, cfg.ProgressDurable, message.GetEventId(), delivery.Subject); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	})
}

func runReconciliationConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	runTradePullConsumer(ctx, client, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.ReconciliationDurable, FilterSubject: "moox.trade.reconciliation.requested.v1.>", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy}, 8, "reconciliation", func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		deliveryCtx := deliveryTraceContext(ctx, delivery)
		started := time.Now()
		var scope struct {
			SpaceID   string `json:"space_id"`
			AccountID string `json:"account_id"`
			ChannelID string `json:"channel_id"`
		}
		message, payload, err := decodeTradeDelivery(delivery)
		if err == nil {
			raw, marshalErr := protojson.Marshal(payload)
			err = marshalErr
			if err == nil {
				err = json.Unmarshal(raw, &scope)
			}
		}
		if err == nil && scope.SpaceID == "" {
			err = errors.New("trade: reconciliation space is required")
		}
		if err == nil {
			err = reconcileOrdersOnce(deliveryCtx, s, e, scope.SpaceID, scope.AccountID, scope.ChannelID)
		}
		telemetry.OperationLatency.WithLabelValues("reconcile").Observe(time.Since(started).Seconds())
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		if _, err = s.RecordInbox(deliveryCtx, cfg.ReconciliationDurable, message.GetEventId(), delivery.Subject); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	})
}

func runRebalanceConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	runTradePullConsumer(ctx, client, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.RebalanceDurable, FilterSubject: "moox.trade.rebalance.requested.v1.>", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 64, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy}, 16, "rebalance", func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		if err := handleRebalanceDelivery(ctx, delivery, s, e, cfg.RebalanceDurable); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	})
}

func runTradePullConsumer(ctx context.Context, client *jetstream.Client, ref jetstream.ConsumerRef, batch int, name string, handler jetstream.DeliveryHandlerFunc) {
	for ctx.Err() == nil {
		pull, err := client.BindPullConsumer(ctx, ref)
		if err != nil {
			log.WarnContextf(ctx, "bind trade %s consumer: %v", name, err)
			time.Sleep(time.Second)
			continue
		}
		runner := jetstream.NewRunner(pull, handler, jetstream.RunnerConfig{BatchSize: batch, ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			log.WarnContextf(ctx, "trade %s delivery failed: %v", name, err)
		})})
		err = runner.Run(ctx)
		_ = pull.Close()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.WarnContextf(ctx, "trade %s consumer stopped: %v", name, err)
		}
		time.Sleep(time.Second)
	}
}

func handleRebalanceDelivery(ctx context.Context, delivery *jetstream.Delivery, s *store.Store, e *command.Engine, consumerName string) error {
	ctx = deliveryTraceContext(ctx, delivery)
	message, payload, err := decodeTradeDelivery(delivery)
	if err != nil {
		return err
	}
	var run store.RebalanceRunRecord
	raw, err := protojson.Marshal(payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &run); err != nil {
		return err
	}
	if _, err := (rebalanceapp.Service{Store: s, Engine: e}).Advance(ctx, run.SpaceID, run.RunID, run.AccountID, run.ChannelID); err != nil {
		return err
	}
	_, err = s.RecordInbox(ctx, consumerName, message.GetEventId(), delivery.Subject)
	return err
}

func reconcileOrdersOnce(ctx context.Context, s *store.Store, e *command.Engine, space, account, channel string) error {
	_, err := (reconciliation.Reconciler{Store: s, Engine: e}).Scope(ctx, reconciliation.Scope{SpaceID: space, AccountID: account, ChannelID: channel, Limit: 200})
	return err
}

func recoverOrdersOnce(ctx context.Context, s *store.Store, e *command.Engine) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	worker := consumer.SubmissionWorker{Engine: e}
	var errs []error
	orders, err := s.ListRecoverableOrders(ctx, 100)
	if err != nil {
		errs = append(errs, fmt.Errorf("list recoverable orders: %w", err))
	} else {
		for _, o := range orders {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
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
				errs = append(errs, fmt.Errorf("recover order %s: %w", o.OrderID, runErr))
			}
		}
	}
	sagas, err := s.ListRecoverableSagas(ctx, 100)
	if err != nil {
		errs = append(errs, fmt.Errorf("list recoverable sagas: %w", err))
	} else {
		for _, saga := range sagas {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			if _, err := e.ResumeReplace(ctx, saga.SpaceID, saga.SagaID); err != nil {
				errs = append(errs, fmt.Errorf("recover saga %s: %w", saga.SagaID, err))
			}
		}
	}
	runs, err := s.ListActiveRebalanceRuns(ctx, 100)
	if err != nil {
		errs = append(errs, fmt.Errorf("list active rebalance runs: %w", err))
	} else {
		svc := rebalanceapp.Service{Store: s, Engine: e}
		for _, run := range runs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			if _, err := svc.Advance(ctx, run.SpaceID, run.RunID, run.AccountID, run.ChannelID); err != nil {
				errs = append(errs, fmt.Errorf("recover rebalance %s: %w", run.RunID, err))
			}
		}
	}
	return errors.Join(errs...)
}

func runExecutionConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine) {
	runTradePullConsumer(ctx, client, jetstream.ConsumerRef{Stream: cfg.Stream, Durable: cfg.ExecutionDurable, FilterSubject: "moox.trade.execution.slice.ready.v1.>", AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 256, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy}, 32, "execution", jetstream.DeliveryHandlerFunc(func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		if err := handleExecutionDelivery(ctx, delivery, s, e, cfg.ExecutionDurable); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}))
}

func handleExecutionDelivery(ctx context.Context, d *jetstream.Delivery, s *store.Store, e *command.Engine, consumerName string) error {
	ctx = deliveryTraceContext(ctx, d)
	message, payload, err := decodeTradeDelivery(d)
	if err != nil {
		return err
	}
	var r store.OrderRecord
	raw, err := protojson.Marshal(payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
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
	_, err = s.RecordInbox(ctx, consumerName, message.GetEventId(), d.Subject)
	return err
}

func deliveryTraceContext(ctx context.Context, delivery *jetstream.Delivery) context.Context {
	return ctx
}

func decodeTradeDelivery(delivery *jetstream.Delivery) (*eventpb.EventMessage, *structpb.Struct, error) {
	if delivery == nil {
		return nil, nil, jetstream.ErrInvalidDelivery
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, nil, err
	}
	message, payload, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return message, nil, err
	}
	structured, ok := payload.(*structpb.Struct)
	if !ok {
		return message, nil, fmt.Errorf("trade event payload has type %T", payload)
	}
	return message, structured, nil
}
