package exchangebridge

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"strings"
)

type Resolver struct {
	Store   service.Store
	Factory service.ExchangeFactory
}

func (r Resolver) Resolve(ctx context.Context, space, channelID string) (exchange.TradingAdapter, error) {
	ch, err := r.Store.GetChannel(ctx, space, channelID)
	if err != nil {
		return nil, err
	}
	if ch.IsSimulated {
		return nil, errors.New("trade: simulated channel is not implemented")
	}
	key, err := r.Store.GetAPIKey(ctx, space, ch.APIKeyID)
	if err != nil {
		return nil, err
	}
	factory := r.Factory
	if factory == nil {
		factory = exchange.New
	}
	a, err := factory(ch.Exchange)
	if err != nil {
		return nil, err
	}
	return &bound{adapter: a, credential: exchange.Credential{APIKey: key.APIKey, APISecret: key.APISecret, Passphrase: key.Passphrase}, market: exchange.MarketType(ch.MarketType)}, nil
}

func (r Resolver) DescribeChannel(ctx context.Context, space, channelID string) (exchange.Channel, error) {
	ch, err := r.Store.GetChannel(ctx, space, channelID)
	if err != nil {
		return exchange.Channel{}, err
	}
	return exchange.Channel{AccountID: ch.AccountID, Exchange: ch.Exchange, MarketType: ch.MarketType, IsSimulated: ch.IsSimulated}, nil
}

func (r Resolver) ResolvePublic(ctx context.Context, space, channelID string) (exchange.TradingAdapter, error) {
	ch, err := r.Store.GetChannel(ctx, space, channelID)
	if err != nil {
		return nil, err
	}
	factory := r.Factory
	if factory == nil {
		factory = exchange.New
	}
	adapter, err := factory(ch.Exchange)
	if err != nil {
		return nil, err
	}
	return &bound{adapter: adapter, market: exchange.MarketType(ch.MarketType)}, nil
}

type bound struct {
	adapter    exchange.ExchangeAdapter
	credential exchange.Credential
	market     exchange.MarketType
}

func (b *bound) MarketType() string   { return string(b.market) }
func (b *bound) ExchangeName() string { return b.adapter.Name() }

func (b *bound) Place(ctx context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	out, err := b.adapter.PlaceOrder(ctx, b.credential, &exchange.PlaceOrderReq{Market: b.market, Symbol: r.Symbol, Side: exchange.OrderSide(strings.ToLower(r.Side)), Type: exchange.OrderType(strings.ToLower(r.Type)), TimeInForce: r.TimeInForce, Price: r.Price.String(), Quantity: r.Quantity.String(), ClientOrderID: r.ClientOrderID, ReduceOnly: r.ReduceOnly})
	if err != nil {
		return exchange.ExchangeOrderResult{}, classify(err)
	}
	return exchange.ExchangeOrderResult{ExchangeOrderID: out.ExchangeOrderID, ClientOrderID: out.ClientOrderID, Status: status(out.Status), FilledQuantity: shared.Zero()}, nil
}
func (b *bound) Cancel(ctx context.Context, symbol, clientID string) (exchange.ExchangeOrderResult, error) {
	out, err := b.adapter.CancelOrder(ctx, b.credential, &exchange.CancelOrderReq{Market: b.market, Symbol: symbol, ClientOrderID: clientID})
	if err != nil {
		return exchange.ExchangeOrderResult{}, classify(err)
	}
	return exchange.ExchangeOrderResult{ExchangeOrderID: out.ExchangeOrderID, ClientOrderID: out.ClientOrderID, Status: status(out.Status), FilledQuantity: shared.Zero()}, nil
}
func (b *bound) QueryByClientOrderID(ctx context.Context, symbol, clientID string) (exchange.ExchangeOrderResult, error) {
	out, err := b.adapter.GetOrder(ctx, b.credential, &exchange.GetOrderReq{Market: b.market, Symbol: symbol, ClientOrderID: clientID})
	if err != nil {
		return exchange.ExchangeOrderResult{}, classify(err)
	}
	filled, parseErr := shared.ParseDecimal(out.FilledQty)
	if parseErr != nil {
		filled = shared.Zero()
	}
	return exchange.ExchangeOrderResult{ExchangeOrderID: out.ExchangeOrderID, ClientOrderID: out.ClientOrderID, Status: status(out.Status), FilledQuantity: filled}, nil
}
func (b *bound) Rules(ctx context.Context, symbol string) (instrument.Rules, error) {
	xs, err := b.adapter.GetInstruments(ctx, b.market)
	if err != nil {
		return instrument.Rules{}, err
	}
	for _, x := range xs {
		if x.Symbol == symbol {
			tick, e := shared.ParseDecimal(x.TickSize)
			if e != nil {
				return instrument.Rules{}, e
			}
			step, e := shared.ParseDecimal(x.LotSize)
			if e != nil {
				return instrument.Rules{}, e
			}
			minq, e := shared.ParseDecimal(x.MinQty)
			if e != nil {
				return instrument.Rules{}, e
			}
			minn, e := shared.ParseDecimal(x.MinNotional)
			if e != nil {
				return instrument.Rules{}, e
			}
			lastPrice := shared.Zero()
			if strings.TrimSpace(x.LastPrice) != "" {
				lastPrice, e = shared.ParseDecimal(x.LastPrice)
				if e != nil {
					return instrument.Rules{}, e
				}
			}
			return instrument.Rules{Version: "exchange-live", Symbol: symbol, BaseAsset: x.BaseCcy, QuoteAsset: x.QuoteCcy, TickSize: tick, StepSize: step, MinQuantity: minq, MinNotional: minn, LastPrice: lastPrice}, nil
		}
	}
	return instrument.Rules{}, errors.New("trade: instrument not found")
}
func (b *bound) SubscribePrivate(ctx context.Context, handler exchange.PrivateEventHandler) error {
	stream, ok := b.adapter.(exchange.PrivateStreamAdapter)
	if !ok {
		return errors.New("trade: private stream not available for adapter")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	ph := &privateHandler{ctx: streamCtx, handler: handler, cancel: cancel}
	err := stream.SubscribePrivate(streamCtx, b.credential, b.market, ph)
	if ph.err != nil {
		return ph.err
	}
	return err
}

type privateHandler struct {
	ctx     context.Context
	handler exchange.PrivateEventHandler
	cancel  context.CancelFunc
	err     error
}

func (h *privateHandler) OnOrderUpdate(*exchange.OrderEvent)       {}
func (h *privateHandler) OnPositionUpdate(*exchange.PositionEvent) {}
func (h *privateHandler) OnBalanceUpdate(*exchange.BalanceEvent)   {}
func (h *privateHandler) OnError(error)                            {}
func (h *privateHandler) OnTrade(event *exchange.TradeEvent) {
	if event == nil || h.handler == nil {
		return
	}
	trade := event.Trade
	quantity, err := shared.ParseDecimal(trade.Quantity)
	if err != nil {
		return
	}
	price, err := shared.ParseDecimal(trade.Price)
	if err != nil {
		return
	}
	fee := shared.Zero()
	if trade.Fee != "" {
		fee, err = shared.ParseDecimal(trade.Fee)
		if err != nil {
			return
		}
	}
	err = h.handler(h.ctx, exchange.FillEvent{ExchangeTradeID: trade.ExchangeTradeID, ExchangeOrderID: trade.OrderID, ClientOrderID: trade.ClientOrderID, Symbol: trade.Symbol, Side: strings.ToUpper(string(trade.Side)), Quantity: quantity, Price: price, Fee: fee, FeeCurrency: trade.FeeCurrency, TradedAt: trade.TradedAt})
	if err != nil {
		h.err = err
		h.cancel()
	}
}
func (b *bound) ListFills(ctx context.Context, symbol, orderID string) ([]exchange.FillEvent, error) {
	rows, err := b.adapter.ListTrades(ctx, b.credential, &exchange.ListTradesReq{Market: b.market, Symbol: symbol, OrderID: orderID, Limit: 500})
	if err != nil {
		return nil, classify(err)
	}
	rules, err := b.Rules(ctx, symbol)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.FillEvent, 0, len(rows))
	for _, r := range rows {
		q, e := shared.ParseDecimal(r.Quantity)
		if e != nil {
			return nil, e
		}
		p, e := shared.ParseDecimal(r.Price)
		if e != nil {
			return nil, e
		}
		fee := shared.Zero()
		if r.Fee != "" {
			fee, e = shared.ParseDecimal(r.Fee)
			if e != nil {
				return nil, e
			}
		}
		out = append(out, exchange.FillEvent{ExchangeTradeID: r.ExchangeTradeID, ExchangeOrderID: r.OrderID, Symbol: r.Symbol, Side: strings.ToUpper(string(r.Side)), BaseAsset: rules.BaseAsset, QuoteAsset: rules.QuoteAsset, Quantity: q, Price: p, Fee: fee, FeeCurrency: r.FeeCurrency, TradedAt: r.TradedAt})
	}
	return out, nil
}
func status(s exchange.OrderStatus) string {
	switch s {
	case exchange.StatusFilled:
		return "FILLED"
	case exchange.StatusCanceled, exchange.StatusPartialCanceled:
		return "CANCELED"
	case exchange.StatusRejected:
		return "REJECTED"
	case exchange.StatusExpired:
		return "EXPIRED"
	case exchange.StatusPartiallyFilled:
		return "PARTIALLY_FILLED"
	default:
		return "OPEN"
	}
}
func classify(err error) error {
	msg := strings.ToLower(err.Error())
	cat := exchange.ErrorPermanent
	if strings.Contains(msg, "order not found") || strings.Contains(msg, "unknown order") {
		cat = exchange.ErrorOrderNotFound
	} else if strings.Contains(msg, "timeout") || strings.Contains(msg, "connection reset") {
		cat = exchange.ErrorTransportUncertain
	} else if strings.Contains(msg, "rate") || strings.Contains(msg, "429") {
		cat = exchange.ErrorRateLimited
	} else if strings.Contains(msg, "balance") {
		cat = exchange.ErrorInsufficientBalance
	}
	return &exchange.ClassifiedError{Category: cat, Err: err}
}
