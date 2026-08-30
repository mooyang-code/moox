package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

type EquitySource interface {
	ResolveLogicalAccountEquity(context.Context, string, string) (shared.Decimal, int64, error)
}

type WeightConversion struct {
	RequestHash            string
	SignalTime             int64
	Equity                 shared.Decimal
	EquitySourceTime       int64
	ReferencePrices        map[string]string
	ReferencePriceEvidence []ReferencePriceEvidence
	WeightsJSON            string
	QuantityTargets        []store.InstrumentTarget
}

// ReferencePriceEvidence records the exact account and quote selected for a
// weight-to-quantity conversion. It is persisted with the immutable target
// receipt so a later audit can reconstruct the conversion inputs.
type ReferencePriceEvidence struct {
	InstrumentID     string `json:"instrument_id"`
	TradingAccountID string `json:"trading_account_id"`
	ExchangeSymbol   string `json:"exchange_symbol"`
	Price            string `json:"price"`
	UpdatedAt        int64  `json:"updated_at"`
}

type WeightResolver struct {
	Store       *store.Store
	Prices      PriceSource
	Equity      EquitySource
	Now         func() time.Time
	MaxPriceAge time.Duration
}

// ErrPermanent marks a target request that cannot become valid by retrying
// market or account state (for example malformed weights or a negative Spot
// allocation). The EventBus consumer TERM's these messages rather than
// redelivering them forever.
var ErrPermanent = errors.New("permanent target weight request")

// These are Trade-owned safety ceilings. Strategy is free to emit partial
// gross exposure, but a malformed or compromised producer must not be able to
// turn one message into an unbounded position request.
var (
	defaultMaxSingleWeight = shared.MustDecimal("10")
	defaultMaxGrossWeight  = shared.MustDecimal("20")
)

func (r *WeightResolver) Resolve(ctx context.Context, messageSignalTime int64, request *tradeeventpb.LogicalAccountTargetWeightRequested, spaceID string) (WeightConversion, error) {
	if r == nil || r.Store == nil || r.Prices == nil || r.Equity == nil {
		return WeightConversion{}, fmt.Errorf("%w: target weight resolver is not configured", ErrPermanent)
	}
	if request == nil || request.GetTargetId() == "" || request.GetLogicalAccountId() == "" || request.GetRunnerId() == "" || request.GetCommandSequence() <= 0 {
		return WeightConversion{}, fmt.Errorf("%w: target weight request is incomplete", ErrPermanent)
	}
	requestHash, weightsJSON, orderedWeights, err := canonicalWeights(request)
	if err != nil {
		return WeightConversion{}, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	if err := validateWeightRisk(orderedWeights); err != nil {
		return WeightConversion{}, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	// The strategy period carried in the payload is the business signal time;
	// broker publication time is only a fallback for legacy/malformed callers.
	var signalTime int64
	if request.GetSignalTime() != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, request.GetSignalTime()); parseErr == nil {
			signalTime = parsed.UnixMilli()
		}
	}
	if signalTime <= 0 {
		signalTime = messageSignalTime
	}
	if signalTime <= 0 {
		signalTime = r.now().UnixMilli()
	}
	// An empty FULL target is an explicit flatten command. It must not depend
	// on an equity snapshot or a quote for an instrument that no longer exists.
	if len(orderedWeights) == 0 {
		return WeightConversion{RequestHash: requestHash, SignalTime: signalTime, Equity: shared.Zero(), WeightsJSON: weightsJSON, ReferencePrices: map[string]string{}, ReferencePriceEvidence: []ReferencePriceEvidence{}, QuantityTargets: []store.InstrumentTarget{}}, nil
	}
	equity, equityTime, err := r.Equity.ResolveLogicalAccountEquity(ctx, spaceID, request.GetLogicalAccountId())
	if err != nil {
		return WeightConversion{}, err
	}
	members, err := r.Store.ListLogicalAccountMembers(ctx, spaceID, request.GetLogicalAccountId(), false)
	if err != nil {
		return WeightConversion{}, err
	}
	if len(members) == 0 {
		return WeightConversion{}, fmt.Errorf("target weight resolver: logical account has no members")
	}
	prices := make(map[string]string, len(orderedWeights))
	evidence := make([]ReferencePriceEvidence, 0, len(orderedWeights))
	quantities := make([]store.InstrumentTarget, 0, len(orderedWeights))
	for _, weight := range orderedWeights {
		instrument, tradingAccountID, err := r.instrumentForMembers(ctx, spaceID, members, weight.InstrumentID)
		if err != nil {
			return WeightConversion{}, err
		}
		quote, err := r.Prices.LatestPrice(ctx, tradingAccountID, instrument.ExchangeSymbol)
		if err != nil {
			return WeightConversion{}, err
		}
		if quote.Price.Cmp(shared.Zero()) <= 0 || quote.UpdatedAt.IsZero() || !r.priceFresh(quote.UpdatedAt) {
			return WeightConversion{}, fmt.Errorf("target weight resolver: invalid reference price for %s", weight.InstrumentID)
		}
		targetWeight, _ := shared.ParseDecimal(weight.TargetWeight)
		quantity := equity.Mul(targetWeight).Div(quote.Price)
		if strings.EqualFold(instrument.MarketType, "SPOT") && quantity.IsNegative() {
			return WeightConversion{}, fmt.Errorf("%w: target weight resolver: Spot target cannot be negative for %s", ErrPermanent, weight.InstrumentID)
		}
		prices[weight.InstrumentID] = quote.Price.String()
		evidence = append(evidence, ReferencePriceEvidence{
			InstrumentID: weight.InstrumentID, TradingAccountID: tradingAccountID,
			ExchangeSymbol: instrument.ExchangeSymbol, Price: quote.Price.String(),
			UpdatedAt: quote.UpdatedAt.UTC().UnixMilli(),
		})
		quantities = append(quantities, store.InstrumentTarget{
			InstrumentID: weight.InstrumentID, Quantity: quantity.String(),
			TradingAccountID: tradingAccountID, ExchangeSymbol: instrument.ExchangeSymbol,
		})
	}
	return WeightConversion{RequestHash: requestHash, SignalTime: signalTime, Equity: equity, EquitySourceTime: equityTime, ReferencePrices: prices, ReferencePriceEvidence: evidence, WeightsJSON: weightsJSON, QuantityTargets: quantities}, nil
}

func validateWeightRisk(weights []orderedWeight) error {
	gross := shared.Zero()
	for _, weight := range weights {
		value, err := shared.ParseDecimal(weight.TargetWeight)
		if err != nil {
			return fmt.Errorf("target weight resolver: invalid weight for %s", weight.InstrumentID)
		}
		if value.Abs().Cmp(defaultMaxSingleWeight) > 0 {
			return fmt.Errorf("target weight resolver: single target weight exceeds %s for %s", defaultMaxSingleWeight.String(), weight.InstrumentID)
		}
		gross = gross.Add(value.Abs())
	}
	if gross.Cmp(defaultMaxGrossWeight) > 0 {
		return fmt.Errorf("target weight resolver: gross target weight exceeds %s", defaultMaxGrossWeight.String())
	}
	return nil
}

func (r *WeightResolver) instrumentForMembers(ctx context.Context, spaceID string, members []store.LogicalAccountMemberRecord, instrumentID string) (store.InstrumentRecord, string, error) {
	for _, member := range members {
		account, accountErr := r.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if accountErr != nil {
			return store.InstrumentRecord{}, "", accountErr
		}
		if account.Status != string(exchange.AccountStatusEnabled) || !account.Ready {
			continue
		}
		instrument, err := r.Store.GetInstrumentByIDForAccount(ctx, spaceID, member.TradingAccountID, instrumentID)
		if err == nil {
			if instrument.Status != "TRADING" && instrument.Status != "live" {
				continue
			}
			if instrument.SettlementAsset != "" && instrument.SettlementAsset != account.SettlementAsset {
				continue
			}
			return instrument, member.TradingAccountID, nil
		}
	}
	return store.InstrumentRecord{}, "", fmt.Errorf("target weight resolver: instrument %s is not supported by a ready account member", instrumentID)
}

func (r *WeightResolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *WeightResolver) priceFresh(updatedAt time.Time) bool {
	maxAge := r.MaxPriceAge
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	age := r.now().Sub(updatedAt.UTC())
	return age >= -maxAge && age <= maxAge
}

type orderedWeight struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
}

func canonicalWeights(request *tradeeventpb.LogicalAccountTargetWeightRequested) (string, string, []orderedWeight, error) {
	weights := make([]orderedWeight, 0, len(request.GetTargets()))
	seen := make(map[string]struct{}, len(request.GetTargets()))
	for _, target := range request.GetTargets() {
		if target == nil || target.GetInstrumentId() == "" {
			return "", "", nil, fmt.Errorf("target weight resolver: invalid target")
		}
		if _, ok := seen[target.GetInstrumentId()]; ok {
			return "", "", nil, fmt.Errorf("target weight resolver: duplicate instrument %s", target.GetInstrumentId())
		}
		seen[target.GetInstrumentId()] = struct{}{}
		weight, err := shared.ParseDecimal(target.GetTargetWeight())
		if err != nil {
			return "", "", nil, fmt.Errorf("target weight resolver: invalid weight for %s", target.GetInstrumentId())
		}
		weights = append(weights, orderedWeight{InstrumentID: target.GetInstrumentId(), TargetWeight: weight.String()})
	}
	sort.Slice(weights, func(i, j int) bool { return weights[i].InstrumentID < weights[j].InstrumentID })
	payload := struct {
		TargetID         string          `json:"target_id"`
		RunnerID         string          `json:"runner_id"`
		LogicalAccountID string          `json:"logical_account_id"`
		CommandSequence  int64           `json:"command_sequence"`
		SignalTime       string          `json:"signal_time"`
		Targets          []orderedWeight `json:"targets"`
	}{TargetID: request.GetTargetId(), RunnerID: request.GetRunnerId(), LogicalAccountID: request.GetLogicalAccountId(), CommandSequence: request.GetCommandSequence(), SignalTime: request.GetSignalTime(), Targets: weights}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", "", nil, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), string(mustJSON(weights)), weights, nil
}

// RequestHash returns the canonical logical request hash used by TargetReceipt.
func RequestHash(request *tradeeventpb.LogicalAccountTargetWeightRequested) (string, error) {
	hash, _, _, err := canonicalWeights(request)
	return hash, err
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
