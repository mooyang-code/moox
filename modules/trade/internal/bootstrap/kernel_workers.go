package bootstrap

import (
	"context"
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
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

func startKernelWorkers(ctx context.Context, cfg config.EventBusConfig, s *store.Store, e *command.Engine) error {
	wakeup := newKernelWakeup()
	s.SetWakeup(wakeup.Wake)
	go runTradeStateWorker(ctx, s, e, wakeup)
	go runPrivateStreamSupervisor(ctx, s, e, wakeup)
	if !cfg.Enabled {
		return nil
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(cfg.URLs, "moox-trade"))
	if err != nil {
		return err
	}
	setKernelEventBusClient(client)
	go func() {
		<-ctx.Done()
		client.Close()
	}()
	go runRebalanceConsumer(ctx, client, cfg, s, e, wakeup)
	return nil
}

func runRebalanceConsumer(ctx context.Context, client *jetstream.Client, cfg config.EventBusConfig, s *store.Store, e *command.Engine, wakeup *kernelWakeup) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		log.WarnContextf(ctx, "load trade event registry: %v", err)
		return
	}
	for ctx.Err() == nil {
		consumer, openErr := events.NewConsumer(ctx, client, registry, events.ConsumerConfig{
			Name: cfg.RebalanceConsumer, Event: events.TradeRebalanceRequested,
			AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 64,
			FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverAllPolicy,
			DeliverDecodeErrors: true,
		})
		if openErr != nil {
			log.WarnContextf(ctx, "open trade rebalance consumer: %v", openErr)
			if !sleepContext(ctx, time.Second) {
				return
			}
			continue
		}
		handler := jetstream.DeliveryHandlerFunc(func(handlerCtx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
			return handleRebalanceDelivery(handlerCtx, delivery, s, e, cfg.RebalanceConsumer, wakeup)
		})
		runner := jetstream.NewRunner(consumer, handler, jetstream.RunnerConfig{
			BatchSize: 16,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				log.WarnContextf(ctx, "trade rebalance delivery failed: %v", err)
			}),
		})
		runErr := runner.Run(ctx)
		_ = consumer.Close()
		if ctx.Err() != nil {
			return
		}
		if runErr != nil {
			log.WarnContextf(ctx, "trade rebalance consumer stopped: %v", runErr)
		}
		if !sleepContext(ctx, time.Second) {
			return
		}
	}
}

func handleRebalanceDelivery(ctx context.Context, delivery *jetstream.Delivery, s *store.Store, e *command.Engine, consumerName string, wakeup *kernelWakeup) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	message, payload, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	request, ok := payload.(*tradeeventpb.RebalanceRequested)
	if !ok || request.GetRequestId() != message.GetEventId() || request.GetExecutionBindingId() != message.GetSubjectId() {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("%w: envelope identity mismatch", rebalanceapp.ErrInvalidRequest)}
	}
	planner := rebalanceapp.RequestPlanner{Resolver: tradeSnapshotResolver{store: s, engine: e, spaceID: message.GetSpaceId(), channelID: request.GetChannelId()}}
	input, err := planner.Build(ctx, message.GetSpaceId(), request)
	if err != nil {
		if rebalanceapp.IsPermanentRequestError(err) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	fresh, err := (rebalanceapp.Service{Store: s, Engine: e}).CreateFromEvent(ctx, consumerName, message.GetEventId(), message.GetEventName(), input)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
	}
	if fresh && wakeup != nil {
		wakeup.Wake()
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func runTradeStateWorker(ctx context.Context, s *store.Store, e *command.Engine, wakeup *kernelWakeup) {
	_ = recoverOrdersOnce(ctx, s, e)
	for {
		select {
		case <-ctx.Done():
			return
		case <-wakeup.C():
			if err := recoverOrdersOnce(ctx, s, e); err != nil && ctx.Err() == nil {
				log.WarnContextf(ctx, "trade state worker: %v", err)
			}
		}
	}
}

func runPrivateStreamSupervisor(ctx context.Context, s *store.Store, e *command.Engine, wakeup *kernelWakeup) {
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
						return
					}
					streamCtx = exchange.WithPrivateStreamState(streamCtx, func(ready bool) {
						telemetry.SetPrivateConnected(key, ready)
					})
					err = adapter.SubscribePrivate(streamCtx, func(eventCtx context.Context, fill exchange.FillEvent) error {
						orderRow, lookupErr := s.GetOrderForPrivateFill(eventCtx, row.SpaceID, row.ChannelID, fill.Symbol, fill.ExchangeOrderID)
						if lookupErr != nil {
							return lookupErr
						}
						fill.BaseAsset, fill.QuoteAsset = orderRow.BaseAsset, orderRow.QuoteAsset
						_, handleErr := (consumer.FillHandler{Store: s}).HandleSource(eventCtx, orderRow.SpaceID, orderRow.AccountID, orderRow.OrderID, fill.ExchangeTradeID, fill, "private_stream")
						if handleErr == nil {
							wakeup.Wake()
						}
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
		for _, order := range orders {
			var runErr error
			switch order.State {
			case "READY", "SUBMITTING", "SUBMIT_UNKNOWN":
				_, runErr = worker.Handle(ctx, order.SpaceID, order.OrderID)
			case "CANCELING":
				_, runErr = e.RecoverCanceling(ctx, order.SpaceID, order.OrderID)
			case "CANCEL_UNKNOWN":
				_, runErr = e.ResolveCancelUnknown(ctx, order.SpaceID, order.OrderID)
			}
			if runErr != nil {
				errs = append(errs, fmt.Errorf("recover order %s: %w", order.OrderID, runErr))
			}
		}
	}
	sagas, err := s.ListRecoverableSagas(ctx, 100)
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, saga := range sagas {
			if _, err := e.ResumeReplace(ctx, saga.SpaceID, saga.SagaID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := advanceActiveRebalances(ctx, s, e); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
