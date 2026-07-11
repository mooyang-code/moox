package okx

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type captureHandler struct{ trade *exchange.TradeEvent }

func (h *captureHandler) OnOrderUpdate(*exchange.OrderEvent)       {}
func (h *captureHandler) OnTrade(v *exchange.TradeEvent)           { h.trade = v }
func (h *captureHandler) OnPositionUpdate(*exchange.PositionEvent) {}
func (h *captureHandler) OnBalanceUpdate(*exchange.BalanceEvent)   {}
func (h *captureHandler) OnError(error)                            {}

func TestDispatchOrdersFill(t *testing.T) {
	h := &captureHandler{}
	err := dispatchOKXPrivate([]byte(`{"arg":{"channel":"orders"},"data":[{"instId":"BTC-USDT-SWAP","side":"buy","ordId":"12","tradeId":"34","fillSz":"1","fillPx":"60000","fee":"-0.1","feeCcy":"USDT","fillTime":"1700000000000"}]}`), h)
	if err != nil {
		t.Fatal(err)
	}
	if h.trade == nil || h.trade.Trade.ExchangeTradeID != "34" || h.trade.Trade.Fee != "0.1" || h.trade.Trade.Side != exchange.SideBuy {
		t.Fatalf("trade=%+v", h.trade)
	}
}
