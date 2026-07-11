package binance

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type captureHandler struct {
	trade *exchange.TradeEvent
	err   error
}

func (h *captureHandler) OnOrderUpdate(*exchange.OrderEvent)       {}
func (h *captureHandler) OnTrade(v *exchange.TradeEvent)           { h.trade = v }
func (h *captureHandler) OnPositionUpdate(*exchange.PositionEvent) {}
func (h *captureHandler) OnBalanceUpdate(*exchange.BalanceEvent)   {}
func (h *captureHandler) OnError(err error)                        { h.err = err }

func TestDispatchSpotExecutionReport(t *testing.T) {
	h := &captureHandler{}
	err := dispatchBinancePrivate([]byte(`{"e":"executionReport","x":"TRADE","s":"BTCUSDT","S":"BUY","c":"client","i":42,"t":7,"l":"0.1","L":"60000","n":"0.0001","N":"BTC"}`), exchange.MarketSpot, h)
	if err != nil {
		t.Fatal(err)
	}
	if h.trade == nil || h.trade.Trade.ExchangeTradeID != "7" || h.trade.Trade.Quantity != "0.1" || h.trade.Trade.Side != exchange.SideBuy {
		t.Fatalf("trade=%+v", h.trade)
	}
}

func TestDispatchFuturesOrderTradeUpdate(t *testing.T) {
	h := &captureHandler{}
	err := dispatchBinancePrivate([]byte(`{"e":"ORDER_TRADE_UPDATE","o":{"x":"TRADE","s":"BTCUSDT","S":"SELL","c":"client","i":43,"t":8,"l":"0.2","L":"61000","n":"1","N":"USDT"}}`), exchange.MarketSwap, h)
	if err != nil {
		t.Fatal(err)
	}
	if h.trade == nil || h.trade.Trade.OrderID != "43" || h.trade.Trade.Side != exchange.SideSell {
		t.Fatalf("trade=%+v", h.trade)
	}
}
