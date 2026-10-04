package paper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type DeciderAdapters interface {
	Adapter(string) (execution.ExecutionAdapter, error)
}

// Decider is the shared production and simulation matching policy.
type Decider struct {
	Store    *store.Store
	Adapters DeciderAdapters
	Now      func() time.Time
}

func (d *Decider) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Decider) Decide(ctx context.Context, candidate store.OrderRecord) (Decision, error) {
	adapter, adapterErr := d.Adapters.Adapter(candidate.TradingAccountID)
	if adapterErr != nil {
		return Decision{}, adapterErr
	}
	priceSource, hasReferencePrice := adapter.(execution.ReferencePriceSource)
	marketDataSource, hasMarketData := adapter.(execution.MarketDataSource)
	if !hasReferencePrice && !hasMarketData {
		return Decision{}, errors.New("paper reference quote unavailable")
	}
	paperConfig, configErr := d.Store.GetPaperAccountConfig(ctx, candidate.SpaceID, candidate.TradingAccountID)
	if configErr != nil {
		return Decision{}, storageError(configErr)
	}
	slippage := shared.Zero()
	if candidate.OrderType == string(exchange.OrderTypeMarket) && candidate.PaperExecutionPrice == nil {
		if paperConfig.SlippageBPS != "" {
			parsed, parseErr := shared.ParseDecimal(paperConfig.SlippageBPS)
			if parseErr != nil || parsed.IsNegative() || parsed.Cmp(shared.MustDecimal("10000")) >= 0 {
				return Decision{}, fmt.Errorf("paper: invalid slippage bps %q", paperConfig.SlippageBPS)
			}
			slippage = parsed
		}
	}
	price := shared.Zero()
	if candidate.OrderType == string(exchange.OrderTypeMarket) && candidate.PaperExecutionPrice != nil {
		parsed, parseErr := shared.ParseDecimal(*candidate.PaperExecutionPrice)
		if parseErr == nil && parsed.Cmp(shared.Zero()) > 0 {
			price = parsed
		}
	}
	if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.FirstMatchPending {
		if parsed, parseErr := shared.ParseDecimal(candidate.ReferencePrice); parseErr == nil && parsed.Cmp(shared.Zero()) > 0 {
			price = parsed
		}
	}
	if price.Cmp(shared.Zero()) <= 0 {
		var quoteErr error
		if hasMarketData {
			quote, marketErr := marketDataSource.GetQuote(ctx, shared.ExchangeSymbol(candidate.ExchangeSymbol))
			quoteErr = marketErr
			if quoteErr == nil && !QuoteFresh(quote, d.now(), 10*time.Second) {
				quoteErr = errors.New("paper public quote is stale")
			}
			if quoteErr == nil {
				if candidate.OrderType == string(exchange.OrderTypeMarket) {
					price, quoteErr = MarketExecutionPrice(exchange.Side(candidate.Side), quote, slippage)
				} else {
					price, quoteErr = MarketExecutionPrice(exchange.Side(candidate.Side), quote, shared.Zero())
				}
			}
		} else {
			quote, referenceErr := priceSource.GetReferencePrice(ctx, candidate.ExchangeSymbol)
			quoteErr = referenceErr
			if quoteErr == nil {
				price = quote.Price
				if quote.Price.Cmp(shared.Zero()) <= 0 ||
					!QuoteFresh(execution.MarketQuote{SourceTime: quote.UpdatedAt}, d.now(), 10*time.Second) {
					quoteErr = errors.New("paper reference quote is stale or empty")
				}
			}
		}
		if quoteErr != nil {
			return Decision{}, fmt.Errorf("paper reference quote unavailable: %w", quoteErr)
		}
		if price.Cmp(shared.Zero()) <= 0 {
			return Decision{}, errors.New("paper reference quote is empty")
		}
	}
	if candidate.OrderType == string(exchange.OrderTypeMarket) && candidate.PaperExecutionPrice == nil && !hasMarketData {
		if slippage.Cmp(shared.Zero()) > 0 {
			factor := shared.MustDecimal("1").Add(slippage.Div(shared.MustDecimal("10000")))
			if candidate.Side == string(exchange.SideSell) {
				factor = shared.MustDecimal("1").Sub(slippage.Div(shared.MustDecimal("10000")))
			}
			price = price.Mul(factor)
		}
	}
	if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.LimitPrice != nil {
		limit, parseErr := shared.ParseDecimal(*candidate.LimitPrice)
		if parseErr != nil {
			return Decision{Cancel: true, Reason: "paper limit price invalid"}, nil
		}
		if !LimitMarketable(exchange.Side(candidate.Side), limit, price) {
			if candidate.TimeInForce == string(exchange.FillPolicyGTC) {
				return Decision{Rest: true}, nil
			}
			return Decision{Cancel: true, Reason: "paper limit order is not marketable"}, nil
		}
	}
	fee := shared.Zero()
	feeAsset := candidate.ReservedAsset
	role := "TAKER"
	if candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.TimeInForce == string(exchange.FillPolicyGTC) && !candidate.FirstMatchPending {
		role = "MAKER"
	}
	if paperConfig.TakerFeeRate == "" {
		paperConfig.TakerFeeRate = "0"
	}
	feeRateRaw := paperConfig.TakerFeeRate
	if role == "MAKER" && paperConfig.MakerFeeRate != "" {
		feeRateRaw = paperConfig.MakerFeeRate
	}
	feeRate, feeErr := shared.ParseDecimal(feeRateRaw)
	if feeErr != nil || feeRate.IsNegative() {
		return Decision{}, fmt.Errorf("paper: invalid fee rate %q", feeRateRaw)
	}
	fee = price.Mul(shared.MustDecimal(candidate.Quantity)).Mul(feeRate)
	realizedPnL := shared.Zero()
	account, accountErr := d.Store.GetTradingAccountByID(ctx, candidate.TradingAccountID)
	if accountErr != nil {
		return Decision{}, storageError(accountErr)
	}
	if account.SettlementAsset != "" {
		feeAsset = account.SettlementAsset
	}
	if !candidate.ReduceOnly {
		snapshot, snapshotErr := adapter.GetAccountSnapshot(ctx)
		if snapshotErr != nil {
			return Decision{}, snapshotErr
		}
		if !paperReservationSufficient(candidate, account, snapshot, price, fee) {
			return Decision{Cancel: true, Reason: "paper reservation insufficient at match"}, nil
		}
	}
	if account.MarketType == string(exchange.MarketTypeSwap) {
		position, found, positionErr := d.Store.GetPosition(ctx, account.SpaceID, account.TradingAccountID, candidate.ExchangeSymbol, string(exchange.PositionSideNet))
		if positionErr != nil {
			return Decision{}, storageError(positionErr)
		}
		if found {
			positionQty := decimalOrZero(position.SignedQuantity)
			closeQty := decimalOrZero(candidate.Quantity)
			if positionQty.Abs().Cmp(closeQty) < 0 {
				closeQty = positionQty.Abs()
			}
			if !positionQty.IsZero() && !closeQty.IsZero() && ((positionQty.Cmp(shared.Zero()) > 0 && candidate.Side == string(exchange.SideSell)) || (positionQty.Cmp(shared.Zero()) < 0 && candidate.Side == string(exchange.SideBuy))) {
				direction := shared.MustDecimal("1")
				if positionQty.IsNegative() {
					direction = direction.Neg()
				}
				realizedPnL = price.Sub(decimalOrZero(position.EntryPrice)).Mul(closeQty).Mul(direction)
			}
		}
	}
	return Decision{Fill: exchange.Fill{
		ExchangeTradeID: candidate.TradingAccountID + ":" + candidate.ClientOrderID,
		ExchangeOrderID: candidate.ExchangeOrderID, ClientOrderID: candidate.ClientOrderID,
		ExchangeSymbol: candidate.ExchangeSymbol,
		Side:           exchange.Side(candidate.Side), PositionSide: exchange.PositionSide(candidate.PositionSide),
		Quantity: decimalOrZero(candidate.Quantity), Price: price, Fee: fee, RealizedPnL: realizedPnL,
		FeeAsset: feeAsset, SettlementAsset: feeAsset, LiquidityRole: role,
		TradedAt: d.now(),
	}}, nil
}

func paperReservationSufficient(
	candidate store.OrderRecord,
	account store.TradingAccountRecord,
	snapshot exchange.AccountSnapshot,
	price, fee shared.Decimal,
) bool {
	if account.MarketType != string(exchange.MarketTypeSpot) && account.MarketType != string(exchange.MarketTypeSwap) {
		return true
	}
	reserved, err := shared.ParseDecimal(candidate.RemainingReservedQuantity)
	if err != nil {
		return false
	}
	quantity, err := shared.ParseDecimal(candidate.Quantity)
	if err != nil {
		return false
	}
	required := price.Mul(quantity).Add(fee)
	if account.MarketType == string(exchange.MarketTypeSwap) {
		leverage := account.LeverageSettings[candidate.InstrumentID]
		if leverage == "" {
			leverage = account.LeverageSettings[candidate.ExchangeSymbol]
		}
		if leverage == "" && account.ExecutionMode == string(exchange.ExecutionModePaper) {
			leverage = account.LeverageSettings["*"]
		}
		parsedLeverage, leverageErr := shared.ParseDecimal(leverage)
		if leverageErr != nil || parsedLeverage.Cmp(shared.Zero()) <= 0 {
			return false
		}
		required = price.Mul(quantity).Div(parsedLeverage).Add(fee)
		return snapshot.AvailableFunds.Add(reserved).Cmp(required) >= 0
	}
	if candidate.Side != string(exchange.SideBuy) {
		return true
	}
	for _, balance := range snapshot.Balances {
		if balance.Asset != candidate.ReservedAsset {
			continue
		}
		return balance.Available.Add(reserved).Cmp(required) >= 0
	}
	return false
}
