package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
)

type TargetOptions struct {
	Client         *jetstream.Client
	ConsumerName   string
	Store          *store.Store
	Wake           func()
	Now            func() time.Time
	SetReady       func(bool)
	WeightResolver TargetWeightResolver
	ResolveTimeout time.Duration
}

// TargetWeightResolver converts the strategy-owned target weights into the
// immutable quantity snapshot consumed by Trade.
type TargetWeightResolver interface {
	Resolve(context.Context, int64, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error)
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
		var invalidPayload *events.PayloadValidationError
		if errors.As(err, &invalidPayload) {
			return targetRejection("invalid_contract", err)
		}
		return targetRejection("invalid_event", err)
	}
	request, ok := payload.(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if !ok ||
		message.GetEventName() != events.LogicalAccountTargetWeightRequested.Name() ||
		message.GetEventVersion() != events.LogicalAccountTargetWeightRequested.Version() {
		return jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      fmt.Errorf("trade target: unexpected event payload %T", payload),
		}
	}

	if opts.WeightResolver == nil {
		return targetRejection("resolver_missing", errors.New("trade target weight resolver is required"))
	}
	// Fencing and expiry precede receipt replay and all market-data work for a
	// modern session target. Legacy Runner events remain accepted through the
	// compatibility adapter so old console-created Runners do not silently
	// stop publishing targets.
	modernContract := request.GetInstanceId() != "" || request.GetSessionId() != "" || request.GetStrategyId() != "" || request.GetBarEndTime() != nil || request.GetEffectiveAt() != nil || request.GetValidUntil() != nil
	if modernContract && (request.GetInstanceId() == "" || request.GetSessionId() == "" || request.GetStrategyId() == "" || request.GetBarEndTime() == nil || request.GetEffectiveAt() == nil || request.GetValidUntil() == nil) {
		return targetRejection("invalid_contract", fmt.Errorf("trade target: new session contract is incomplete"))
	}
	account, accountErr := opts.Store.GetLogicalAccount(ctx, message.GetSpaceId(), request.GetLogicalAccountId())
	if accountErr != nil {
		if errors.Is(accountErr, gorm.ErrRecordNotFound) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: accountErr}
		}
		return retryTarget(accountErr)
	}
	if modernContract {
		if account.OwnerInstanceID != request.GetInstanceId() || account.OwnerSessionID != request.GetSessionId() {
			return targetRejection("authorization_conflict", fmt.Errorf("%w: target session authorization", store.ErrConflict))
		}
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	if modernContract && (!now.Before(request.GetValidUntil().AsTime()) || now.Before(request.GetEffectiveAt().AsTime())) {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: store.ErrTargetExpired}
	}
	requestHash, err := targetapp.RequestHash(request)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if existing, lookupErr := opts.Store.GetTargetReceipt(ctx, message.GetSpaceId(), request.GetTargetId()); lookupErr == nil {
		if existing.RequestHash != requestHash {
			return targetRejection("receipt_conflict", fmt.Errorf("%w: target receipt request hash conflict", store.ErrConflict))
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return retryTarget(lookupErr)
	}
	// Avoid doing an expensive equity/quote lookup for a target that has
	// already been superseded. The transactional accept path remains the
	// authority, but this read-only fast path prevents stale redeliveries from
	// blocking a durable consumer on unavailable market data.
	sequence := uint64(request.GetCommandSequence())
	if sequence == 0 && request.GetBarEndTime() != nil {
		sequence = uint64(request.GetBarEndTime().AsTime().UnixMilli())
	}
	instanceID := request.GetInstanceId()
	if instanceID == "" {
		instanceID = request.GetRunnerId()
	}
	if current, currentErr := opts.Store.GetLogicalAccountTarget(ctx, message.GetSpaceId(), request.GetLogicalAccountId()); currentErr == nil {
		barEnd := int64(0)
		if request.GetBarEndTime() != nil {
			barEnd = request.GetBarEndTime().AsTime().UnixMilli()
		}
		isNewContract := request.GetInstanceId() != "" && request.GetSessionId() != "" && barEnd > 0
		if (isNewContract && current.InstanceID == request.GetInstanceId() && current.SessionID == request.GetSessionId() && (current.BarEndTime > barEnd || (current.BarEndTime == barEnd && current.TargetID != request.GetTargetId()))) ||
			(!isNewContract && (current.CommandSequence > sequence ||
				(current.CommandSequence == sequence && current.TargetID != request.GetTargetId()))) {
			// The command is permanently superseded. TERM preserves the
			// consumer's poison-message semantics without doing market-data I/O.
			return targetRejection("superseded", nil)
		}
	} else if !errors.Is(currentErr, gorm.ErrRecordNotFound) {
		return retryTarget(currentErr)
	}
	// Membership and account snapshots must remain stable from the resolver's
	// venue selection through receipt acceptance. Otherwise a concurrent member
	// removal can leave an immutable receipt pointing at a venue that no longer
	// belongs to the logical account.
	unlockAccount := opts.Store.LockLogicalAccount(message.GetSpaceId(), request.GetLogicalAccountId())
	defer unlockAccount()
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

	now = time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	record := store.LogicalAccountTargetRecord{
		SpaceID:          message.GetSpaceId(),
		LogicalAccountID: request.GetLogicalAccountId(),
		TargetID:         request.GetTargetId(),
		RunnerID:         instanceID,
		CommandSequence:  sequence,
		InstanceID:       request.GetInstanceId(),
		SessionID:        request.GetSessionId(),
		StrategyID:       request.GetStrategyId(),
		Targets:          make([]store.InstrumentTarget, 0, len(request.GetTargets())),
		Status:           "PENDING",
		AcceptedAt:       now.UnixMilli(),
	}
	if request.GetBarEndTime() != nil {
		record.BarEndTime = request.GetBarEndTime().AsTime().UTC().UnixMilli()
	}
	if request.GetEffectiveAt() != nil {
		record.EffectiveAt = request.GetEffectiveAt().AsTime().UTC().UnixMilli()
	}
	if request.GetValidUntil() != nil {
		record.ValidUntil = request.GetValidUntil().AsTime().UTC().UnixMilli()
	}
	timeout := opts.ResolveTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resolveCtx, cancel := context.WithTimeout(ctx, timeout)
	conversion, conversionErr := opts.WeightResolver.Resolve(resolveCtx, message.GetOccurredAt().AsTime().UnixMilli(), request, message.GetSpaceId())
	if conversionErr == nil {
		conversionErr = resolveCtx.Err()
	}
	cancel()
	if conversionErr != nil {
		if errors.Is(conversionErr, targetapp.ErrPermanent) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: conversionErr}
		}
		return retryTarget(conversionErr)
	}
	record.Targets = append(record.Targets, conversion.QuantityTargets...)
	// Keep the receipt column's JSON shape stable: an object keyed by
	// instrument_id, with full quote evidence as the value when available.
	referencePriceValue := make(map[string]targetapp.ReferencePriceEvidence, len(conversion.ReferencePriceEvidence))
	for _, evidence := range conversion.ReferencePriceEvidence {
		if evidence.InstrumentID != "" {
			referencePriceValue[evidence.InstrumentID] = evidence
		}
	}
	if len(referencePriceValue) == 0 {
		for instrumentID, price := range conversion.ReferencePrices {
			referencePriceValue[instrumentID] = targetapp.ReferencePriceEvidence{InstrumentID: instrumentID, Price: price}
		}
	}
	referencePrices, marshalErr := json.Marshal(referencePriceValue)
	if marshalErr != nil {
		return retryTarget(marshalErr)
	}
	receiptRunnerID := instanceID
	if request.GetInstanceId() != "" && request.GetSessionId() != "" {
		// Keep the legacy receipt index unique across independent sessions.
		receiptRunnerID = instanceID + "@" + request.GetSessionId()
	}
	receipt := store.TargetReceiptRecord{
		SpaceID: message.GetSpaceId(), TargetID: request.GetTargetId(), RunnerID: receiptRunnerID, InstanceID: request.GetInstanceId(), SessionID: request.GetSessionId(), StrategyID: request.GetStrategyId(), LogicalAccountID: request.GetLogicalAccountId(), CommandSequence: sequence, BarEndTime: record.BarEndTime, EffectiveAt: record.EffectiveAt, ValidUntil: record.ValidUntil, RequestHash: requestHash, SignalTime: conversion.SignalTime, WeightsJSON: conversion.WeightsJSON, Equity: conversion.Equity.String(), EquitySourceTime: conversion.EquitySourceTime, ReferencePricesJSON: string(referencePrices), QuantityTargetsJSON: mustMarshal(conversion.QuantityTargets), AcceptedAt: now.UnixMilli(),
	}
	var ownerGeneration int64
	if request.GetOwnerGeneration() > 0 {
		ownerGeneration = request.GetOwnerGeneration()
	}
	_, accepted, err := opts.Store.AcceptLogicalAccountTargetWithReceiptLocked(ctx, record, receipt, ownerGeneration)
	if err != nil {
		if errors.Is(err, store.ErrInvalidRecord) ||
			errors.Is(err, store.ErrConflict) ||
			errors.Is(err, store.ErrTargetExpired) ||
			errors.Is(err, store.ErrTargetAuthorization) ||
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

func mustMarshal(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func unsupportedLogicalTargetInstrument(
	ctx context.Context,
	tradeStore *store.Store,
	spaceID string,
	request *tradeeventpb.LogicalAccountTargetWeightRequested,
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
	enabledMemberCount := 0
	readyMemberCount := 0
	for _, member := range members {
		account, accountErr := tradeStore.GetTradingAccountByID(
			ctx, member.TradingAccountID,
		)
		if accountErr != nil {
			return "", false, accountErr
		}
		if account.Status != "ENABLED" {
			continue
		}
		enabledMemberCount++
		if !account.Ready {
			continue
		}
		readyMemberCount++
		instruments, instrumentErr := tradeStore.ListInstrumentsForAccount(ctx, account.TradingAccountID)
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
	// A target is terminally unsupported only after every enabled membership
	// has been inspected. If any enabled member is still warming up, retry so
	// that it can become the eventual eligible execution venue.
	membersReady = enabledMemberCount > 0 && readyMemberCount == enabledMemberCount
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
