package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

type HandlerConfig struct {
	NakDelay          time.Duration
	PeriodBudgetMin   time.Duration
	PeriodBudgetMax   time.Duration
	ReadColumnTimeout time.Duration
}

type Handler struct {
	sets     trigger.SetLocator
	store    trigger.PeriodStore
	runner   trigger.PipelineRunner
	lanes    *trigger.Lanes
	registry *events.Registry
	initErr  error
	cfg      HandlerConfig
}

func NewHandler(sets trigger.SetLocator, periodStore trigger.PeriodStore, runner trigger.PipelineRunner, locks trigger.SetLocks, cfg HandlerConfig) *Handler {
	if cfg.NakDelay <= 0 {
		cfg.NakDelay = 10 * time.Second
	}
	if cfg.PeriodBudgetMin <= 0 {
		cfg.PeriodBudgetMin = time.Minute
	}
	if cfg.PeriodBudgetMax <= 0 {
		cfg.PeriodBudgetMax = 15 * time.Minute
	}
	if cfg.PeriodBudgetMin > cfg.PeriodBudgetMax {
		cfg.PeriodBudgetMin = cfg.PeriodBudgetMax
	}
	if cfg.ReadColumnTimeout <= 0 {
		cfg.ReadColumnTimeout = 20 * time.Second
	}
	registry, initErr := events.DefaultRegistry()
	return &Handler{
		sets: sets, store: periodStore, runner: runner, lanes: trigger.NewLanes(locks), registry: registry, initErr: initErr, cfg: cfg,
	}
}

func (h *Handler) Close() {
	if h != nil && h.lanes != nil {
		h.lanes.Close()
	}
}

func (h *Handler) LaneStatuses() []trigger.LaneStatus {
	if h == nil || h.lanes == nil {
		return nil
	}
	return h.lanes.Status()
}

func (h *Handler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil || h.sets == nil || h.store == nil || h.runner == nil || h.lanes == nil || h.registry == nil {
		if h != nil && h.initErr != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("initialize factor period event registry: %w", h.initErr)}
		}
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor period handler is not initialized")}
	}
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor period delivery is nil")}
	}
	if delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("decode collector period event: %w", delivery.DecodeError)}
	}
	message, payload, err := events.DecodeCollectorPeriodCompletedWithContentType(
		h.registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject collector period event: %w", err)}
	}
	if message == nil || payload == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("collector period event is empty")}
	}

	setID := domain.SetID(payload.GetDatasetId(), payload.GetFrequency())
	_, err = h.lanes.Do(ctx, setID, func(laneCtx context.Context) (pipeline.Outcome, error) {
		set, factors, found, lookupErr := h.sets.EnabledSetByDataset(laneCtx, message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency())
		if lookupErr != nil {
			return pipeline.Outcome{}, fmt.Errorf("%w: lookup enabled factor set: %v", storageio.ErrInfra, lookupErr)
		}
		if !found || set.Status != domain.SetStatusEnabled {
			return pipeline.Outcome{}, nil
		}
		if set.SetID == "" || set.SpaceID != message.GetSpaceId() || set.SourceDatasetID != payload.GetDatasetId() || set.Freq != payload.GetFrequency() {
			return pipeline.Outcome{}, fmt.Errorf("enabled factor set identity does not match collector event")
		}
		exists, existsErr := h.store.ComputedExists(laneCtx, set.SpaceID, set.ResultDatasetID, message.GetEventId(), payload.GetPeriodTime())
		if existsErr != nil {
			return pipeline.Outcome{}, fmt.Errorf("%w: check computed factor period: %v", storageio.ErrInfra, existsErr)
		}
		if exists {
			return pipeline.Outcome{}, nil
		}

		clock, clockErr := periodclock.ForSpace(set.SpaceID)
		if clockErr != nil {
			return pipeline.Outcome{}, clockErr
		}
		columnsCtx, cancel := context.WithTimeout(laneCtx, h.cfg.ReadColumnTimeout)
		carryColumns, columnsErr := h.store.DatasetColumns(columnsCtx, set.SpaceID, set.SourceDatasetID)
		cancel()
		if columnsErr != nil {
			return pipeline.Outcome{}, fmt.Errorf("%w: read source dataset columns: %v", storageio.ErrInfra, columnsErr)
		}
		budget, budgetErr := periodBudget(clock, set.Freq, h.cfg.PeriodBudgetMin, h.cfg.PeriodBudgetMax)
		if budgetErr != nil {
			return pipeline.Outcome{}, budgetErr
		}
		plan, planErr := pipeline.BuildLivePlan(clock, pipeline.LiveInput{
			Set: set, Factors: factors, PeriodTime: time.Unix(payload.GetPeriodTime(), 0).UTC(),
			Universe: payload.GetUniverseSubjectIds(), UpstreamFailed: payload.GetFailedSubjects(),
			CarryColumns: carryColumns, TriggerEventID: message.GetEventId(), Budget: budget,
		})
		if planErr != nil {
			return pipeline.Outcome{}, planErr
		}
		return h.runner.Run(laneCtx, plan)
	})
	if err == nil {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	if errors.Is(err, storageio.ErrInfra) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		log.WarnContextf(ctx, "factor_period_retry set_id=%s period_time=%d error=%v", setID, payload.GetPeriodTime(), err)
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: h.cfg.NakDelay, Err: err}
	}
	log.ErrorContextf(ctx, "factor_period_rejected set_id=%s period_time=%d error=%v", setID, payload.GetPeriodTime(), err)
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
}

func periodBudget(clock periodclock.Clock, freq string, minBudget, maxBudget time.Duration) (time.Duration, error) {
	duration, err := clock.Duration(freq)
	if err != nil {
		return 0, err
	}
	budget := duration * 2
	if budget < minBudget {
		budget = minBudget
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	return budget, nil
}

var _ jetstream.DeliveryHandler = (*Handler)(nil)
