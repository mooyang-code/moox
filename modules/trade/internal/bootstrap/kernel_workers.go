package bootstrap

import (
	"context"
	"encoding/json"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	tradebus "github.com/mooyang-code/moox/modules/trade/internal/infra/bus"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"time"
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
	go runRecoveryLoop(ctx, s, e)
	go runFillReconciliation(ctx, s, e)
	return nil
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
	handler := consumer.FillHandler{Store: s}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			orders, err := s.ListOpenOrders(ctx, 200)
			if err != nil {
				log.WarnContextf(ctx, "trade fill reconciliation scan: %v", err)
				continue
			}
			for _, o := range orders {
				adapter, resolveErr := e.AdapterFor(ctx, o)
				if resolveErr != nil {
					log.WarnContextf(ctx, "resolve order %s adapter: %v", o.OrderID, resolveErr)
					continue
				}
				fills, listErr := adapter.ListFills(ctx, o.Symbol, o.ExchangeOrderID)
				if listErr != nil {
					log.WarnContextf(ctx, "list order %s fills: %v", o.OrderID, listErr)
					continue
				}
				for _, f := range fills {
					if f.ExchangeTradeID == "" {
						continue
					}
					if err := handler.Handle(ctx, o.SpaceID, o.AccountID, o.OrderID, f.ExchangeTradeID, f); err != nil {
						log.WarnContextf(ctx, "apply order %s fill %s: %v", o.OrderID, f.ExchangeTradeID, err)
					}
				}
				state, stateErr := adapter.QueryByClientOrderID(ctx, o.Symbol, o.ClientOrderID)
				if stateErr == nil && (state.Status == "CANCELED" || state.Status == "REJECTED" || state.Status == "EXPIRED") {
					if _, err := e.ReconcileExchangeTerminal(ctx, o.SpaceID, o.OrderID, state.Status); err != nil {
						log.WarnContextf(ctx, "reconcile terminal order %s: %v", o.OrderID, err)
					}
				}
			}
		}
	}
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
