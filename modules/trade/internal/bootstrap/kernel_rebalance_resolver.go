package bootstrap

import (
	"context"
	"errors"
	"math/big"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

type tradeSnapshotResolver struct {
	store     *store.Store
	engine    *command.Engine
	spaceID   string
	channelID string
}

func (r tradeSnapshotResolver) ResolveLatestPrice(ctx context.Context, spaceID, channelID string, target *tradeeventpb.RebalanceTarget) (rebalanceapp.Market, error) {
	price, err := r.store.LatestTradePrice(ctx, spaceID, target.GetSymbol())
	if err != nil {
		return rebalanceapp.Market{}, err
	}
	adapter, err := r.engine.AdapterFor(ctx, store.OrderRecord{SpaceID: spaceID, ChannelID: channelID, Symbol: target.GetSymbol()})
	if err != nil {
		return rebalanceapp.Market{}, err
	}
	rules, err := adapter.Rules(ctx, target.GetSymbol())
	if err != nil {
		return rebalanceapp.Market{}, err
	}
	return rebalanceapp.Market{BaseAsset: rules.BaseAsset, QuoteAsset: rules.QuoteAsset, Price: price}, nil
}

func (r tradeSnapshotResolver) ResolveCurrentQuantity(ctx context.Context, spaceID, accountID, symbol string) (shared.Decimal, error) {
	raw, err := r.store.CurrentPositionQuantity(ctx, spaceID, accountID, symbol)
	if err != nil {
		return shared.Decimal{}, err
	}
	return shared.ParseDecimal(raw)
}

func (r tradeSnapshotResolver) RoundQuantity(ctx context.Context, instrumentID string, quantity shared.Decimal) (shared.Decimal, error) {
	adapter, err := r.engine.AdapterFor(ctx, store.OrderRecord{SpaceID: r.spaceID, ChannelID: r.channelID, Symbol: instrumentID})
	if err != nil {
		return shared.Decimal{}, err
	}
	rules, err := adapter.Rules(ctx, instrumentID)
	if err != nil {
		return shared.Decimal{}, err
	}
	if rules.StepSize.IsZero() {
		return shared.Decimal{}, errors.New("trade: instrument step size is zero")
	}
	value, ok := new(big.Rat).SetString(quantity.String())
	if !ok {
		return shared.Decimal{}, shared.ErrInvalidDecimal
	}
	step, ok := new(big.Rat).SetString(rules.StepSize.String())
	if !ok {
		return shared.Decimal{}, shared.ErrInvalidDecimal
	}
	units := new(big.Rat).Quo(value, step)
	integer := new(big.Int).Quo(units.Num(), units.Denom())
	rounded := new(big.Rat).Mul(new(big.Rat).SetInt(integer), step)
	return shared.ParseDecimal(rounded.FloatString(max(0, rules.StepSize.Scale())))
}
