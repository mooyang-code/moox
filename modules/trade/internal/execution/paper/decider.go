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
	isMarket := candidate.OrderType == string(exchange.OrderTypeMarket)
	isLimit := candidate.OrderType == string(exchange.OrderTypeLimit)
	// 挂单后成交（maker）：GTC 限价单第一次撮合没能成交、已经挂在盘口，之后才被对手成交。
	resting := isLimit && candidate.TimeInForce == string(exchange.FillPolicyGTC) && !candidate.FirstMatchPending
	slippage := shared.Zero()
	// 滑点只用在吃单上：市价单（没有冻结成交价时）和下单当时就可成交的限价单。
	if (isMarket && candidate.PaperExecutionPrice == nil) || (isLimit && candidate.FirstMatchPending) {
		if paperConfig.SlippageBPS != "" {
			parsed, parseErr := shared.ParseDecimal(paperConfig.SlippageBPS)
			if parseErr != nil || parsed.IsNegative() || parsed.Cmp(shared.MustDecimal("10000")) >= 0 {
				return Decision{}, fmt.Errorf("paper: invalid slippage bps %q", paperConfig.SlippageBPS)
			}
			slippage = parsed
		}
	}
	// book 是撮合依据的价格：市价单是下单时冻结的成交价（已含滑点），或实时卖一/买一加滑点；
	// 限价单是对手方最优价（买单看卖一、卖单看买一，不含滑点），第一次撮合用下单时记下的，挂单后再撮合看实时行情。
	book := shared.Zero()
	if isMarket && candidate.PaperExecutionPrice != nil {
		parsed, parseErr := shared.ParseDecimal(*candidate.PaperExecutionPrice)
		if parseErr == nil && parsed.Cmp(shared.Zero()) > 0 {
			book = parsed
		}
	}
	if isLimit && candidate.FirstMatchPending {
		if parsed, parseErr := shared.ParseDecimal(candidate.ReferencePrice); parseErr == nil && parsed.Cmp(shared.Zero()) > 0 {
			book = parsed
		}
	}
	if book.Cmp(shared.Zero()) <= 0 {
		quote, quoteErr := adapter.GetQuote(ctx, exchange.NewSymbolKey(candidate.MarketType, candidate.ExchangeSymbol))
		if quoteErr == nil && !QuoteFresh(quote, d.now(), 10*time.Second) {
			quoteErr = errors.New("paper public quote is stale")
		}
		if quoteErr == nil {
			// 市价单直接按实时行情加滑点成交；限价单这里只取对手方最优价，成交价在下面按限价规则定。
			bookSlippage := shared.Zero()
			if isMarket {
				bookSlippage = slippage
			}
			book, quoteErr = MarketExecutionPrice(exchange.Side(candidate.Side), quote, bookSlippage)
		}
		if quoteErr != nil {
			return Decision{}, fmt.Errorf("paper reference quote unavailable: %w", quoteErr)
		}
		if book.Cmp(shared.Zero()) <= 0 {
			return Decision{}, errors.New("paper reference quote is empty")
		}
	}
	price := book
	if isLimit && candidate.LimitPrice != nil {
		limit, parseErr := shared.ParseDecimal(*candidate.LimitPrice)
		if parseErr != nil {
			return Decision{Cancel: true, Reason: "paper limit price invalid"}, nil
		}
		if !LimitMarketable(exchange.Side(candidate.Side), limit, book) {
			if candidate.TimeInForce == string(exchange.FillPolicyGTC) {
				return Decision{Rest: true}, nil
			}
			return Decision{Cancel: true, Reason: "paper limit order is not marketable"}, nil
		}
		if resting {
			// 已挂在盘口的限价单被对手吃掉时按自己的限价成交：市场后来穿过限价，也不会得到更好的价格。
			price = limit
		} else {
			// 下单当时就可成交：按当时的卖一/买一加滑点吃单，但不会比限价更差。
			price = limitTakerPrice(exchange.Side(candidate.Side), book, slippage, limit)
		}
	}
	fee := shared.Zero()
	feeAsset := candidate.ReservedAsset
	role := "TAKER"
	if resting {
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

// limitTakerPrice 是可立即成交的限价单的吃单价：对手方最优价加滑点，买单不高于限价、卖单不低于限价。
func limitTakerPrice(side exchange.Side, book, slippageBPS, limit shared.Decimal) shared.Decimal {
	factor := slippageBPS.Div(shared.MustDecimal("10000"))
	if side == exchange.SideBuy {
		price := book.Mul(shared.MustDecimal("1").Add(factor))
		if price.Cmp(limit) > 0 {
			return limit
		}
		return price
	}
	price := book.Mul(shared.MustDecimal("1").Sub(factor))
	if price.Cmp(limit) < 0 {
		return limit
	}
	return price
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
