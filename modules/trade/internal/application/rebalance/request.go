package rebalance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domain "github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

var ErrInvalidRequest = errors.New("trade: invalid rebalance request")

type SnapshotResolver interface {
	ResolveChannel(context.Context, string, string, string, string) (Channel, error)
	ResolveLatestPrice(context.Context, string, string, string, *tradeeventpb.RebalanceTarget) (Market, error)
	ResolveCurrentQuantities(context.Context, string, string) (map[string]shared.Decimal, error)
	RoundQuantity(context.Context, string, string, string, string, shared.Decimal) (shared.Decimal, error)
}

type Channel struct {
	MarketType string
}

type RequestPlanner struct {
	Resolver SnapshotResolver
}

func (p RequestPlanner) Build(ctx context.Context, spaceID string, request *tradeeventpb.RebalanceRequested) (CreateInput, error) {
	if request == nil || request.GetRequestId() == "" || request.GetStrategyRunId() == "" ||
		request.GetExecutionBindingId() == "" || request.GetAccountId() == "" || request.GetChannelId() == "" ||
		request.GetDataRevision() == "" {
		return CreateInput{}, fmt.Errorf("%w: required identity is missing", ErrInvalidRequest)
	}
	if request.GetMode() != "paper" && request.GetMode() != "live" {
		return CreateInput{}, fmt.Errorf("%w: unsupported mode %q", ErrInvalidRequest, request.GetMode())
	}
	if p.Resolver == nil {
		return CreateInput{}, errors.New("trade: snapshot resolver is unavailable")
	}
	capital, err := shared.ParseDecimal(request.GetCapitalAmount())
	if err != nil || capital.Cmp(shared.Zero()) <= 0 {
		return CreateInput{}, fmt.Errorf("%w: invalid capital_amount", ErrInvalidRequest)
	}
	quoteAsset := strings.ToUpper(strings.TrimSpace(request.GetQuoteAsset()))
	if quoteAsset == "" {
		return CreateInput{}, fmt.Errorf("%w: quote_asset is required", ErrInvalidRequest)
	}
	channel, err := p.Resolver.ResolveChannel(
		ctx, spaceID, request.GetAccountId(), request.GetChannelId(), request.GetMode(),
	)
	if err != nil {
		return CreateInput{}, err
	}
	currents, err := p.Resolver.ResolveCurrentQuantities(ctx, spaceID, request.GetAccountId())
	if err != nil {
		return CreateInput{}, fmt.Errorf("resolve current positions: %w", err)
	}
	input := CreateInput{
		SpaceID: spaceID, RunID: request.GetRequestId(), IdempotencyKey: request.GetRequestId(),
		AccountID: request.GetAccountId(), ChannelID: request.GetChannelId(),
		MarketSnapshotID: request.GetDataRevision(), PositionSnapshotID: "trade_position_projection",
		RulesVersion: "exchange_instrument_rules", ExecutionMode: request.GetMode(),
		Mode: domain.FullTarget, Markets: map[string]Market{},
	}
	seen := make(map[string]struct{}, len(request.GetTargets()))
	gross := shared.Zero()
	for _, target := range request.GetTargets() {
		if target == nil || target.GetInstrumentId() == "" || target.GetSymbol() == "" {
			return CreateInput{}, fmt.Errorf("%w: target identity is missing", ErrInvalidRequest)
		}
		if _, exists := seen[target.GetSymbol()]; exists {
			return CreateInput{}, fmt.Errorf("%w: duplicate symbol %q", ErrInvalidRequest, target.GetSymbol())
		}
		seen[target.GetSymbol()] = struct{}{}
		if target.GetMarketType() != "spot" && target.GetMarketType() != "swap" {
			return CreateInput{}, fmt.Errorf("%w: unknown market_type %q", ErrInvalidRequest, target.GetMarketType())
		}
		if target.GetMarketType() != channel.MarketType {
			return CreateInput{}, fmt.Errorf("%w: target market_type %q does not match channel %q", ErrInvalidRequest, target.GetMarketType(), channel.MarketType)
		}
		weight, parseErr := shared.ParseDecimal(target.GetTargetWeight())
		if parseErr != nil || (weight.IsNegative() && target.GetMarketType() != "swap") {
			return CreateInput{}, fmt.Errorf("%w: invalid target_weight for %s", ErrInvalidRequest, target.GetSymbol())
		}
		gross = gross.Add(weight.Abs())
		if gross.Cmp(shared.MustDecimal("1")) > 0 {
			return CreateInput{}, fmt.Errorf("%w: gross target weight exceeds 1", ErrInvalidRequest)
		}
		market, resolveErr := p.Resolver.ResolveLatestPrice(ctx, spaceID, request.GetChannelId(), request.GetMode(), target)
		if resolveErr != nil {
			return CreateInput{}, fmt.Errorf("resolve latest price for %s: %w", target.GetSymbol(), resolveErr)
		}
		if !strings.EqualFold(market.QuoteAsset, quoteAsset) {
			return CreateInput{}, fmt.Errorf("%w: quote_asset %q does not match instrument quote %q", ErrInvalidRequest, quoteAsset, market.QuoteAsset)
		}
		price, parseErr := shared.ParseDecimal(market.Price)
		if parseErr != nil || price.Cmp(shared.Zero()) <= 0 {
			return CreateInput{}, fmt.Errorf("%w: invalid latest price for %s", ErrInvalidRequest, target.GetSymbol())
		}
		quantity, roundErr := p.Resolver.RoundQuantity(
			ctx, spaceID, request.GetChannelId(), request.GetMode(), target.GetSymbol(),
			capital.Mul(weight).Div(price),
		)
		if roundErr != nil {
			return CreateInput{}, fmt.Errorf("round quantity for %s: %w", target.GetSymbol(), roundErr)
		}
		current := currents[target.GetSymbol()]
		input.Targets = append(input.Targets, domain.Target{Symbol: target.GetSymbol(), Quantity: quantity})
		input.Currents = append(input.Currents, domain.Current{Symbol: target.GetSymbol(), Quantity: current})
		market.MarketType = target.GetMarketType()
		input.Markets[target.GetSymbol()] = market
	}
	for symbol, current := range currents {
		if current.IsZero() {
			continue
		}
		if _, exists := seen[symbol]; exists {
			continue
		}
		target := &tradeeventpb.RebalanceTarget{
			InstrumentId: symbol,
			Symbol:       symbol,
			MarketType:   channel.MarketType,
			TargetWeight: "0",
		}
		market, resolveErr := p.Resolver.ResolveLatestPrice(ctx, spaceID, request.GetChannelId(), request.GetMode(), target)
		if resolveErr != nil {
			return CreateInput{}, fmt.Errorf("resolve latest price for omitted position %s: %w", symbol, resolveErr)
		}
		if !strings.EqualFold(market.QuoteAsset, quoteAsset) {
			return CreateInput{}, fmt.Errorf("%w: quote_asset %q does not match instrument quote %q", ErrInvalidRequest, quoteAsset, market.QuoteAsset)
		}
		market.MarketType = channel.MarketType
		input.Targets = append(input.Targets, domain.Target{Symbol: symbol, Quantity: shared.Zero()})
		input.Currents = append(input.Currents, domain.Current{Symbol: symbol, Quantity: current})
		input.Markets[symbol] = market
	}
	return input, nil
}

func IsPermanentRequestError(err error) bool {
	return errors.Is(err, ErrInvalidRequest)
}
