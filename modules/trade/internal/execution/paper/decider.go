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
		quote, quoteErr := adapter.GetQuote(ctx, exchange.NewSymbolKey(candidate.MarketType, candidate.ExchangeSymbol))
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
		if quoteErr != nil {
			return Decision{}, fmt.Errorf("paper reference quote unavailable: %w", quoteErr)
		}
		if price.Cmp(shared.Zero()) <= 0 {
			return Decision{}, errors.New("paper reference quote is empty")
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
	if candidate.MarketType == string(exchange.MarketTypeSpot) {
		// 现货手续费按成交额（报价币）计价：扣费币种必须是交易对的报价币，而不是账户结算币，
		// 否则 USDT 结算账户在 ETH/BTC 上会把 BTC 计价的手续费记成 USDT。
		instrument, instrumentErr := d.Store.GetInstrumentByIDForAccount(ctx, account.SpaceID, account.TradingAccountID, candidate.InstrumentID)
		if instrumentErr != nil {
			return Decision{}, storageError(instrumentErr)
		}
		if instrument.QuoteAsset != "" {
			feeAsset = instrument.QuoteAsset
		}
	} else if account.SettlementAsset != "" {
		feeAsset = account.SettlementAsset
	}
	// 现货卖单卖的是冻结的币本身，撮合时不用再看资金，不读快照：账户里别的币估值失败不能连累卖单。
	spotSell := candidate.MarketType == string(exchange.MarketTypeSpot) && candidate.Side != string(exchange.SideBuy)
	if !candidate.ReduceOnly && !spotSell {
		snapshot, snapshotErr := adapter.GetAccountSnapshot(ctx)
		if snapshotErr != nil {
			return Decision{}, snapshotErr
		}
		if !paperReservationSufficient(candidate, account, snapshot, price, fee) {
			return Decision{Cancel: true, Reason: "paper reservation insufficient at match"}, nil
		}
	}
	if candidate.MarketType == string(exchange.MarketTypeSwap) {
		position, found, positionErr := d.Store.GetPosition(ctx, account.SpaceID, account.TradingAccountID, exchange.NewSymbolKey(candidate.MarketType, candidate.ExchangeSymbol), string(exchange.PositionSideNet))
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
		Market:         exchange.MarketType(candidate.MarketType),
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
	if candidate.MarketType != string(exchange.MarketTypeSpot) && candidate.MarketType != string(exchange.MarketTypeSwap) {
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
	if candidate.MarketType == string(exchange.MarketTypeSwap) {
		leverage := account.LeverageSettings[candidate.InstrumentID]
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
