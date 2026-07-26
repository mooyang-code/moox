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

const (
	Interval1m  = "1m"  // 1分钟
	Interval3m  = "3m"  // 3分钟
	Interval5m  = "5m"  // 5分钟
	Interval15m = "15m" // 15分钟
	Interval30m = "30m" // 30分钟
	Interval1h  = "1h"  // 1小时
	Interval2h  = "2h"  // 2小时
	Interval4h  = "4h"  // 4小时
	Interval6h  = "6h"  // 6小时
	Interval12h = "12h" // 12小时
	Interval1d  = "1d"  // 1天
	Interval1w  = "1w"  // 1周
	Interval1M  = "1M"  // 1月
)
