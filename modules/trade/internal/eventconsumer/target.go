package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
)

type TargetOptions struct {
	Client       *jetstream.Client
	ConsumerName string
	Store        *store.Store
	Wake         func()
	Now          func() time.Time
	SetReady     func(bool)
	Gate         sync.Locker
}

func HandleTarget(
	ctx context.Context,
	delivery *jetstream.Delivery,
	opts TargetOptions,
) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      jetstream.ErrInvalidDelivery,
		}
	}
	if opts.Store == nil {
		return retryTarget(store.ErrInvalidRecord)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return retryTarget(err)
	}
	message, payload, err := events.DecodeRaw(
		registry,
		delivery.RawData,
		delivery.Subject,
		delivery.RawMessageID,
		delivery.ContentType,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	request, ok := payload.(*tradeeventpb.LogicalAccountTargetRequested)
	if !ok ||
		message.GetEventName() != events.LogicalAccountTargetRequested.Name() ||
		message.GetEventVersion() != events.LogicalAccountTargetRequested.Version() {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      fmt.Errorf("trade target: unexpected event payload %T", payload),
		}
	}

	if opts.Gate != nil {
		opts.Gate.Lock()
		defer opts.Gate.Unlock()
	}
	unsupportedInstrument, membersReady, err := unsupportedLogicalTargetInstrument(
		ctx, opts.Store, message.GetSpaceId(), request,
	)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}
	if unsupportedInstrument != "" {
		err = fmt.Errorf(
			"%w: no enabled member supports instrument %s",
			store.ErrInvalidRecord, unsupportedInstrument,
		)
		if membersReady {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}

	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	record := store.LogicalAccountTargetRecord{
		SpaceID:          message.GetSpaceId(),
		LogicalAccountID: request.GetLogicalAccountId(),
		TargetID:         request.GetTargetId(),
		RunnerID:         request.GetRunnerId(),
		CommandSequence:  uint64(request.GetCommandSequence()),
		Targets:          make([]store.InstrumentTarget, 0, len(request.GetTargets())),
		Status:           "PENDING",
		AcceptedAt:       now.UnixMilli(),
	}
	for _, target := range request.GetTargets() {
		record.Targets = append(record.Targets, store.InstrumentTarget{
			InstrumentID: target.GetInstrumentId(),
			Quantity:     target.GetQuantity(),
		})
	}
	_, accepted, err := opts.Store.AcceptLogicalAccountTarget(ctx, record)
	if err != nil {
		if errors.Is(err, store.ErrInvalidRecord) ||
			errors.Is(err, store.ErrConflict) ||
			errors.Is(err, gorm.ErrRecordNotFound) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}
	if accepted && opts.Wake != nil {
		opts.Wake()
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func unsupportedLogicalTargetInstrument(
	ctx context.Context,
	tradeStore *store.Store,
	spaceID string,
	request *tradeeventpb.LogicalAccountTargetRequested,
) (instrumentID string, membersReady bool, err error) {
	if len(request.GetTargets()) == 0 {
		return "", true, nil
	}
	members, err := tradeStore.ListLogicalAccountMembers(
		ctx, spaceID, request.GetLogicalAccountId(), false,
	)
	if err != nil {
		return "", false, err
	}
	if len(members) == 0 {
		return request.GetTargets()[0].GetInstrumentId(), false, nil
	}
	supportedIDs := make(map[string]struct{})
	membersReady = true
	for _, member := range members {
		account, accountErr := tradeStore.GetExchangeAccountByID(
			ctx, member.ExchangeAccountID,
		)
		if accountErr != nil {
			return "", false, accountErr
		}
		if !account.Ready {
			membersReady = false
		}
		instruments, instrumentErr := tradeStore.ListInstruments(
			ctx, account.Exchange, account.MarketType,
		)
		if instrumentErr != nil {
			return "", false, instrumentErr
		}
		for _, instrument := range instruments {
			if instrument.Status != "TRADING" && instrument.Status != "live" {
				continue
			}
			if instrument.SettlementAsset != "" &&
				instrument.SettlementAsset != account.SettlementAsset {
				continue
			}
			supportedIDs[instrument.InstrumentID] = struct{}{}
		}
	}
	for _, target := range request.GetTargets() {
		if _, ok := supportedIDs[target.GetInstrumentId()]; !ok {
			return target.GetInstrumentId(), membersReady, nil
		}
	}
	return "", membersReady, nil
}

func retryTarget(err error) jetstream.HandlerResult {
	return jetstream.HandlerResult{
		Decision: jetstream.RETRY,
		Delay:    time.Second,
		Err:      err,
	}
}
