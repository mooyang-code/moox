package market

import (
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

// Kline K线数据
type Kline struct {
	common.BaseDataPoint
	Symbol      string         `json:"symbol"`
	Exchange    string         `json:"exchange"`
	Interval    string         `json:"interval"`
	OpenTime    time.Time      `json:"open_time"`
	CloseTime   time.Time      `json:"close_time"`
	Open        common.Decimal `json:"open"`
	High        common.Decimal `json:"high"`
	Low         common.Decimal `json:"low"`
	Close       common.Decimal `json:"close"`
	Volume      common.Decimal `json:"volume"`
	QuoteVolume common.Decimal `json:"quote_volume"`
	TradeCount  int64          `json:"trade_count"`
	Revision    uint64         `json:"revision"`
}

// NewKline 创建K线数据
func NewKline(exchange, symbol, interval string) *Kline {
	return &Kline{
		BaseDataPoint: common.NewBaseDataPoint(exchange, "kline"),
		Exchange:      exchange,
		Symbol:        symbol,
		Interval:      interval,
	}
}
