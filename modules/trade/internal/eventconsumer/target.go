package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type TargetOptions struct {
	Client         *jetstream.Client
	ConsumerName   string
	Store          *store.Store
	Wake           func()
	WakeTarget     func(spaceID, logicalAccountID string)
	Now            func() time.Time
	SetReady       func(bool)
	WeightResolver TargetWeightResolver
	ResolveTimeout time.Duration
	// Intake 返回 false（暂停、恢复追赶、重置）时延迟重投真实目标；影子目标只是记录，不受影响。
	Intake func() bool
}

// TargetWeightResolver converts the strategy-owned target weights into the
// immutable quantity snapshot consumed by Trade.
type TargetWeightResolver interface {
	Resolve(context.Context, *tradeeventpb.LogicalAccountTargetWeightRequested, string) (targetapp.WeightConversion, error)
}

// conversionAttentionDeliveries 是换算连续失败多少次才告警。
const conversionAttentionDeliveries = 5

// HandleTarget 处理一条策略目标消息：先校验并归一化（admitTarget），再换算数量、落库并唤醒执行器（acceptTarget）。
func HandleTarget(
	ctx context.Context,
	delivery *jetstream.Delivery,
	opts TargetOptions,
) jetstream.HandlerResult {
	admitted, result, done := admitTarget(ctx, delivery, opts)
	if done {
		return result
	}
	return acceptTarget(ctx, delivery, opts, admitted)
}

// admittedTarget 是通过解码、授权、有效期和标的归一化检查的真实目标。
type admittedTarget struct {
	message *eventpb.EventMessage
	request *tradeeventpb.LogicalAccountTargetWeightRequested
	account store.LogicalAccountRecord
}

// admitTarget 解码并校验目标消息。done 为真表示消息已有结论（拒绝、重投、影子目标已记录），
// 调用方直接返回 result；否则返回可以继续接收的目标。
func admitTarget(
	ctx context.Context,
	delivery *jetstream.Delivery,
	opts TargetOptions,
) (admitted admittedTarget, result jetstream.HandlerResult, done bool) {
	if delivery == nil {
		return admittedTarget{}, jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      jetstream.ErrInvalidDelivery,
		}, true
	}
	if opts.Store == nil {
		return admittedTarget{}, retryTarget(store.ErrInvalidRecord), true
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return admittedTarget{}, retryTarget(err), true
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
			return admittedTarget{}, targetRejection("invalid_contract", err), true
		}
		return admittedTarget{}, targetRejection("invalid_event", err), true
	}
	request, ok := payload.(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if !ok ||
		message.GetEventName() != events.LogicalAccountTargetWeightRequested.Name() ||
		message.GetEventVersion() != events.LogicalAccountTargetWeightRequested.Version() {
		return admittedTarget{}, jetstream.HandlerResult{
			Decision: jetstream.TERM,
			Err:      fmt.Errorf("trade target: unexpected event payload %T", payload),
		}, true
	}

	if opts.WeightResolver == nil {
		return admittedTarget{}, targetRejection("resolver_missing", errors.New("trade target weight resolver is required")), true
	}
	// DecodeRaw has already enforced the complete session-scoped contract.
	account, accountErr := opts.Store.GetLogicalAccount(ctx, message.GetSpaceId(), request.GetLogicalAccountId())
	if accountErr != nil {
		if errors.Is(accountErr, gorm.ErrRecordNotFound) {
			return admittedTarget{}, jetstream.HandlerResult{Decision: jetstream.TERM, Err: accountErr}, true
		}
		return admittedTarget{}, retryTarget(accountErr), true
	}
	if account.ControlMode != "STRATEGY" {
		return admittedTarget{}, targetRejection("authorization_conflict", fmt.Errorf("%w: 组合账户不是策略控制的", store.ErrTargetAuthorization)), true
	}
	if account.OwnerInstanceID != request.GetInstanceId() || account.OwnerSessionID != request.GetSessionId() {
		return admittedTarget{}, targetRejection("authorization_conflict", fmt.Errorf("%w: 目标所属的策略会话与账户当前授权不一致", store.ErrConflict)), true
	}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	// A target may arrive before its effective bar boundary because Strategy
	// and Trade clocks/transport are not perfectly aligned. Persist it while
	// the validity window is still open; the executor will hold it in PENDING
	// until effective_at and only terminally reject it after valid_until.
	if !now.Before(request.GetValidUntil().AsTime()) {
		return admittedTarget{}, jetstream.HandlerResult{Decision: jetstream.TERM, Err: store.ErrTargetExpired}, true
	}
	// 上游标的 ID 不带市场后缀，回执和目标只保存标准化后的 Trade 内部 ID。
	tradable, err := opts.Store.LogicalAccountMarkets(ctx, message.GetSpaceId(), account.LogicalAccountID)
	if err != nil {
		return admittedTarget{}, retryTarget(err), true
	}
	if len(tradable) == 0 && len(request.GetTargets()) > 0 {
		// 组合还没有启用的成员，没有可交易的市场：等成员加入后重投，和原来等成员就绪一致。
		return admittedTarget{}, retryTarget(errors.New("组合账户还没有启用的成员")), true
	}
	request, err = normalizeTargetRequest(request, marketTypes(tradable))
	if err != nil {
		return admittedTarget{}, targetRejection("invalid_contract", err), true
	}
	if request.GetShadow() {
		return admittedTarget{}, handleShadowTarget(ctx, opts, message.GetSpaceId(), request, now), true
	}
	if opts.Intake != nil && !opts.Intake() {
		// 暂停期间不接收真实目标，但也不销毁它：它可能是策略暂停前刚发布、还在路上的目标。
		// 留在消费者里延迟重投，放开后照常接收；已过有效期的由上面的过期检查终止。
		return admittedTarget{}, pausedTargetRetry(), true
	}
	return admittedTarget{message: message, request: request, account: account}, jetstream.HandlerResult{}, false
}

// acceptTarget 把已通过校验的真实目标换算成数量快照，在账户锁内连同回执一起落库，成功后唤醒执行器。
func acceptTarget(
	ctx context.Context,
	delivery *jetstream.Delivery,
	opts TargetOptions,
	admitted admittedTarget,
) jetstream.HandlerResult {
	message, request, account := admitted.message, admitted.request, admitted.account
	requestHash, err := targetapp.RequestHash(request)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	if existing, lookupErr := opts.Store.GetTargetReceipt(ctx, message.GetSpaceId(), request.GetTargetId()); lookupErr == nil {
		if existing.RequestHash != requestHash {
			return targetRejection("receipt_conflict", fmt.Errorf("%w: 同一目标的回执请求哈希冲突", store.ErrConflict))
		}
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return retryTarget(lookupErr)
	}
	// Avoid doing an expensive equity/quote lookup for a target that has
	// already been superseded. The transactional accept path remains the
	// authority, but this read-only fast path prevents stale redeliveries from
	// blocking a durable consumer on unavailable market data.
	// The target table stores a sequence for ordering. It is derived from the
	// immutable bar timestamp, never supplied as an authorization fallback.
	barEnd := request.GetBarEndTime().AsTime().UnixMilli()
	sequence := uint64(barEnd)
	instanceID := request.GetInstanceId()
	if current, currentErr := opts.Store.GetLogicalAccountTarget(ctx, message.GetSpaceId(), request.GetLogicalAccountId()); currentErr == nil {
		if current.InstanceID == request.GetInstanceId() && current.SessionID == request.GetSessionId() &&
			(current.BarEndTime > barEnd || (current.BarEndTime == barEnd && current.TargetID != request.GetTargetId())) {
			// The command is permanently superseded. TERM preserves the
			// consumer's poison-message semantics without doing market-data I/O.
			return targetRejection("superseded", nil)
		}
	} else if !errors.Is(currentErr, gorm.ErrRecordNotFound) {
		return retryTarget(currentErr)
	}
	// Membership and account snapshots must remain stable from the resolver's
	// exchange selection through receipt acceptance. Otherwise a concurrent member
	// removal can leave an immutable receipt pointing at an exchange that no longer
	// belongs to the logical account.
	// 拿锁阶段也受处理预算约束：完整同步等长时间持锁的操作不能让串行消费的目标消息一直卡在这里，
	// 超时按可重试处理，其他账户的消息才能继续。
	lockBudget := opts.ResolveTimeout
	if lockBudget <= 0 {
		lockBudget = 10 * time.Second
	}
	lockCtx, cancelLock := context.WithTimeout(ctx, lockBudget)
	unlockAccount, lockErr := opts.Store.LockLogicalAccountContext(lockCtx, message.GetSpaceId(), request.GetLogicalAccountId())
	cancelLock()
	if lockErr != nil {
		return retryTarget(lockErr)
	}
	defer unlockAccount()
	unsupportedInstrument, delisted, membersReady, err := unsupportedLogicalTargetInstrument(
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
			"%w: 没有启用的成员支持标的 %s",
			store.ErrInvalidRecord, unsupportedInstrument,
		)
		if membersReady {
			return targetRejection("unsupported_instrument", err)
		}
		return retryTarget(err)
	}

	if len(delisted) > 0 {
		request = holdTargets(request, delisted)
	}
	now := time.Now().UTC()
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
	record.BarEndTime = request.GetBarEndTime().AsTime().UTC().UnixMilli()
	record.EffectiveAt = request.GetEffectiveAt().AsTime().UTC().UnixMilli()
	record.ValidUntil = request.GetValidUntil().AsTime().UTC().UnixMilli()
	timeout := opts.ResolveTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resolveCtx, cancel := context.WithTimeout(ctx, timeout)
	conversion, conversionErr := opts.WeightResolver.Resolve(resolveCtx, request, message.GetSpaceId())
	if conversionErr == nil {
		conversionErr = resolveCtx.Err()
	}
	cancel()
	if conversionErr != nil {
		if errors.Is(conversionErr, targetapp.ErrPermanent) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: conversionErr}
		}
		// 换算失败的目标不进目标表，过期后就消失了，页面看不到。偶发失败重投就好，持续失败才提醒人。
		if delivery.DeliveryCount >= conversionAttentionDeliveries {
			telemetry.NoteTargetRejection(account.Name, "目标换算失败："+telemetry.StableReason(conversionErr.Error()), now)
		}
		return retryTarget(conversionErr)
	}
	record.Targets = append(record.Targets, conversion.QuantityTargets...)
	referencePrices, marshalErr := json.Marshal(referencePriceByInstrument(conversion))
	if marshalErr != nil {
		return retryTarget(marshalErr)
	}
	// Keep the historical receipt index unique across independent sessions.
	receiptRunnerID := instanceID + "@" + request.GetSessionId()
	receipt := store.TargetReceiptRecord{
		SpaceID: message.GetSpaceId(), TargetID: request.GetTargetId(), RunnerID: receiptRunnerID, InstanceID: request.GetInstanceId(), SessionID: request.GetSessionId(), StrategyID: request.GetStrategyId(), LogicalAccountID: request.GetLogicalAccountId(), CommandSequence: sequence, BarEndTime: record.BarEndTime, EffectiveAt: record.EffectiveAt, ValidUntil: record.ValidUntil, RequestHash: requestHash, SignalTime: conversion.SignalTime, WeightsJSON: conversion.WeightsJSON, Equity: conversion.Equity.String(), EquitySourceTime: conversion.EquitySourceTime, ReferencePricesJSON: string(referencePrices), QuantityTargetsJSON: mustMarshal(conversion.QuantityTargets), AcceptedAt: now.UnixMilli(),
	}
	var accepted bool
	_, accepted, err = opts.Store.AcceptLogicalAccountTargetWithReceiptLocked(ctx, record, receipt)
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
	if accepted {
		if opts.WakeTarget != nil {
			opts.WakeTarget(record.SpaceID, record.LogicalAccountID)
		} else if opts.Wake != nil {
			opts.Wake()
		}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

// handleShadowTarget 处理预热中的策略实例发来的影子目标（设计 6.3）：完成权重到数量的换算并记录，
// 不接受为正式目标，不拿账户锁、不预留资金、不唤醒执行器。
func handleShadowTarget(
	ctx context.Context,
	opts TargetOptions,
	spaceID string,
	request *tradeeventpb.LogicalAccountTargetWeightRequested,
	now time.Time,
) jetstream.HandlerResult {
	timeout := opts.ResolveTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resolveCtx, cancel := context.WithTimeout(ctx, timeout)
	conversion, err := opts.WeightResolver.Resolve(resolveCtx, request, spaceID)
	if err == nil {
		err = resolveCtx.Err()
	}
	cancel()
	if err != nil {
		if errors.Is(err, targetapp.ErrPermanent) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}
	_, err = opts.Store.RecordShadowTarget(ctx, store.ShadowTargetRecord{
		SpaceID: spaceID, TargetID: request.GetTargetId(), LogicalAccountID: request.GetLogicalAccountId(),
		InstanceID: request.GetInstanceId(), SessionID: request.GetSessionId(), StrategyID: request.GetStrategyId(),
		BarEndTime: request.GetBarEndTime().AsTime().UTC().UnixMilli(), ValidUntil: request.GetValidUntil().AsTime().UTC().UnixMilli(),
		WeightsJSON: conversion.WeightsJSON, Equity: conversion.Equity.String(), EquitySourceTime: conversion.EquitySourceTime,
		ReferencePricesJSON: mustMarshal(referencePriceByInstrument(conversion)), QuantityTargetsJSON: mustMarshal(conversion.QuantityTargets),
		RecordedAt: now.UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidRecord) {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		return retryTarget(err)
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

// referencePriceByInstrument 整理回执里的参考价：按 instrument_id 为键的对象，有完整报价证据时值就是证据，
// 没有时用换算给出的价格兜底，保持回执列的 JSON 形状稳定。
func referencePriceByInstrument(conversion targetapp.WeightConversion) map[string]targetapp.ReferencePriceEvidence {
	result := make(map[string]targetapp.ReferencePriceEvidence, len(conversion.ReferencePriceEvidence))
	for _, evidence := range conversion.ReferencePriceEvidence {
		if evidence.InstrumentID != "" {
			result[evidence.InstrumentID] = evidence
		}
	}
	if len(result) == 0 {
		for instrumentID, price := range conversion.ReferencePrices {
			result[instrumentID] = targetapp.ReferencePriceEvidence{InstrumentID: instrumentID, Price: price}
		}
	}
	return result
}

func marketTypes(markets []string) []exchange.MarketType {
	result := make([]exchange.MarketType, len(markets))
	for i, market := range markets {
		result[i] = exchange.MarketType(market)
	}
	return result
}

// normalizeTargetRequest 按每一项自带的 market_type 把标的 ID 标准化成 Trade 内部 ID。
// 市场不在组合可交易的市场里、或标准化后撞成同一个内部 ID，都按契约错误处理。
func normalizeTargetRequest(
	request *tradeeventpb.LogicalAccountTargetWeightRequested,
	tradable []exchange.MarketType,
) (*tradeeventpb.LogicalAccountTargetWeightRequested, error) {
	normalized, ok := proto.Clone(request).(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if !ok {
		return nil, fmt.Errorf("trade target: unexpected event payload %T", request)
	}
	seen := make(map[string]struct{}, len(normalized.GetTargets()))
	for _, target := range normalized.GetTargets() {
		market := exchange.MarketType(target.GetMarketType())
		if !slices.Contains(tradable, market) {
			return nil, fmt.Errorf("%w: 标的 %s 的市场 %q 不在组合可交易的市场 %v 里",
				exchange.ErrInvalidInstrumentID, target.GetInstrumentId(), market, tradable)
		}
		instrumentID, err := exchange.NormalizeInstrumentID(target.GetInstrumentId(), market)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[instrumentID]; dup {
			return nil, fmt.Errorf("%w: 标的 %s 重复出现", exchange.ErrInvalidInstrumentID, instrumentID)
		}
		seen[instrumentID] = struct{}{}
		target.InstrumentId = instrumentID
	}
	return normalized, nil
}

func mustMarshal(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// holdTargets 把指定的标的改成“保持”：名单里已经不能交易的标的只跳过这一项，既不买也不卖，
// 其余照常执行；全部跳过时也不会变成清仓指令。
func holdTargets(request *tradeeventpb.LogicalAccountTargetWeightRequested, instrumentIDs []string) *tradeeventpb.LogicalAccountTargetWeightRequested {
	held, ok := proto.Clone(request).(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if !ok {
		return request
	}
	for _, target := range held.GetTargets() {
		if slices.Contains(instrumentIDs, target.GetInstrumentId()) {
			target.Hold, target.TargetWeight = true, "0"
		}
	}
	return held
}

// unsupportedLogicalTargetInstrument 找出第一个没有成员能交易的标的。名单里（下架名单）已经不能交易的标的不算不受支持，
// 单独列在 delisted 里：只跳过这一项，不拒绝整份目标。
func unsupportedLogicalTargetInstrument(
	ctx context.Context,
	tradeStore *store.Store,
	spaceID string,
	request *tradeeventpb.LogicalAccountTargetWeightRequested,
) (instrumentID string, delisted []string, membersReady bool, err error) {
	if len(request.GetTargets()) == 0 {
		return "", nil, true, nil
	}
	members, err := tradeStore.ListLogicalAccountMembers(
		ctx, spaceID, request.GetLogicalAccountId(), false,
	)
	if err != nil {
		return "", nil, false, err
	}
	if len(members) == 0 {
		return request.GetTargets()[0].GetInstrumentId(), nil, false, nil
	}
	blockedSet, err := tradeStore.LoadBlockedSet(ctx)
	if err != nil {
		return "", nil, false, err
	}
	blockedIDs := make(map[string]struct{})
	supportedIDs := make(map[string]struct{})
	enabledMemberCount := 0
	readyMemberCount := 0
	for _, member := range members {
		account, accountErr := tradeStore.GetTradingAccountByID(
			ctx, member.TradingAccountID,
		)
		if accountErr != nil {
			return "", nil, false, accountErr
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
			return "", nil, false, instrumentErr
		}
		for _, instrument := range instruments {
			if !exchange.StatusTradable(instrument.Status) {
				if blockedSet.ContainsInstrument(account.Exchange, instrument) {
					blockedIDs[instrument.InstrumentID] = struct{}{}
				}
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
	// that it can become the eventual eligible execution exchange.
	membersReady = enabledMemberCount > 0 && readyMemberCount == enabledMemberCount
	for _, target := range request.GetTargets() {
		// 保持标的不会被下单，不能因为它暂时不受支持（例如已下架的持仓）而拒绝整个目标。
		if target.GetHold() {
			continue
		}
		if _, ok := supportedIDs[target.GetInstrumentId()]; !ok {
			if _, delist := blockedIDs[target.GetInstrumentId()]; delist {
				delisted = append(delisted, target.GetInstrumentId())
				continue
			}
			return target.GetInstrumentId(), nil, membersReady, nil
		}
	}
	return "", delisted, membersReady, nil
}

// pausedTargetDelay 是暂停期间目标的重投间隔：暂停是人为的、可能持续很久，不需要每秒重试。
const pausedTargetDelay = 5 * time.Second

func pausedTargetRetry() jetstream.HandlerResult {
	return jetstream.HandlerResult{
		Decision: jetstream.RETRY,
		Delay:    pausedTargetDelay,
		Err:      errors.New("交易已暂停：目标等待恢复或过期"),
	}
}

func retryTarget(err error) jetstream.HandlerResult {
	return jetstream.HandlerResult{
		Decision: jetstream.RETRY,
		Delay:    time.Second,
		Err:      err,
	}
}
