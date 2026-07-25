package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

type tradeSnapshotResolver struct {
	store  *store.Store
	engine *command.Engine
}

func (r tradeSnapshotResolver) ResolveChannel(ctx context.Context, spaceID, accountID, channelID, mode string) (rebalanceapp.Channel, error) {
	channel, err := r.engine.DescribeChannel(ctx, spaceID, channelID)
	if err != nil {
		return rebalanceapp.Channel{}, err
	}
	if channel.AccountID != accountID {
		return rebalanceapp.Channel{}, fmt.Errorf("%w: channel does not belong to account", rebalanceapp.ErrInvalidRequest)
	}
	if mode == "paper" && !channel.IsSimulated {
		return rebalanceapp.Channel{}, fmt.Errorf("%w: paper mode requires a simulated channel", rebalanceapp.ErrInvalidRequest)
	}
	if mode == "live" && channel.IsSimulated {
		return rebalanceapp.Channel{}, fmt.Errorf("%w: live mode requires a real channel", rebalanceapp.ErrInvalidRequest)
	}
	if channel.MarketType != "spot" && channel.MarketType != "swap" {
		return rebalanceapp.Channel{}, fmt.Errorf("%w: unsupported channel market_type %q", rebalanceapp.ErrInvalidRequest, channel.MarketType)
	}
	return rebalanceapp.Channel{MarketType: channel.MarketType}, nil
}

func (r tradeSnapshotResolver) ResolveLatestPrice(ctx context.Context, spaceID, channelID, _ string, target *tradeeventpb.RebalanceTarget) (rebalanceapp.Market, error) {
	adapter, err := r.engine.PublicAdapterFor(ctx, spaceID, channelID)
	if err != nil {
		return rebalanceapp.Market{}, err
	}
	rules, err := adapter.Rules(ctx, target.GetSymbol())
	if err != nil {
		return rebalanceapp.Market{}, err
	}
	if rules.LastPrice.Cmp(shared.Zero()) <= 0 {
		return rebalanceapp.Market{}, errors.New("trade: instrument last price is unavailable")
	}
	return rebalanceapp.Market{BaseAsset: rules.BaseAsset, QuoteAsset: rules.QuoteAsset, Price: rules.LastPrice.String()}, nil
}

func (r tradeSnapshotResolver) ResolveCurrentQuantities(ctx context.Context, spaceID, accountID string) (map[string]shared.Decimal, error) {
	rows, err := r.store.ListPositions(ctx, spaceID, accountID, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]shared.Decimal, len(rows))
	for _, row := range rows {
		quantity, parseErr := shared.ParseDecimal(row.Quantity)
		if parseErr != nil {
			return nil, parseErr
		}
		out[row.Symbol] = quantity
	}
	return out, nil
}

func (r tradeSnapshotResolver) RoundQuantity(ctx context.Context, spaceID, channelID, _ string, symbol string, quantity shared.Decimal) (shared.Decimal, error) {
	adapter, err := r.engine.PublicAdapterFor(ctx, spaceID, channelID)
	if err != nil {
		return shared.Decimal{}, err
	}
	rules, err := adapter.Rules(ctx, symbol)
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
