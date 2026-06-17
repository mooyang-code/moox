package market

import (
	"encoding/json"
	"fmt"
	"time"
	
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

// Ticker 行情数据
type Ticker struct {
	common.BaseDataPoint
	Symbol           string         `json:"symbol"`
	Exchange         string         `json:"exchange"`
	LastPrice        common.Decimal `json:"last_price"`
	BidPrice         common.Decimal `json:"bid_price"`
	AskPrice         common.Decimal `json:"ask_price"`
	Volume24h        common.Decimal `json:"volume_24h"`
	QuoteVolume24h   common.Decimal `json:"quote_volume_24h"`
	High24h          common.Decimal `json:"high_24h"`
	Low24h           common.Decimal `json:"low_24h"`
	Open24h          common.Decimal `json:"open_24h"`
	PriceChange      common.Decimal `json:"price_change"`
	PriceChangePercent common.Decimal `json:"price_change_percent"`
	UpdateTime       time.Time      `json:"update_time"`
}

// NewTicker 创建行情数据
func NewTicker(exchange, symbol string) *Ticker {
	return &Ticker{
		BaseDataPoint: common.NewBaseDataPoint(exchange, "ticker"),
		Exchange:      exchange,
		Symbol:        symbol,
		UpdateTime:    time.Now(),
	}
}

// Validate 验证行情数据
func (t *Ticker) Validate() error {
	if t.Symbol == "" {
		return fmt.Errorf("交易对不能为空")
	}
	if t.Exchange == "" {
		return fmt.Errorf("交易所不能为空")
	}
	
	// 验证价格逻辑
	bid, _ := t.BidPrice.Float64()
	ask, _ := t.AskPrice.Float64()
	if bid > ask && bid > 0 && ask > 0 {
		return fmt.Errorf("买价不能高于卖价")
	}
	
	return nil
}

// Marshal 序列化
func (t *Ticker) Marshal() ([]byte, error) {
	return json.Marshal(t)
}

// Unmarshal 反序列化
func (t *Ticker) Unmarshal(data []byte) error {
	return json.Unmarshal(data, t)
}

// TickerBatch 行情批量数据
type TickerBatch struct {
	Exchange string    `json:"exchange"`
	Tickers  []*Ticker `json:"tickers"`
	Count    int       `json:"count"`
	UpdateTime time.Time `json:"update_time"`
}

// NewTickerBatch 创建行情批量数据
func NewTickerBatch(exchange string) *TickerBatch {
	return &TickerBatch{
		Exchange:   exchange,
		Tickers:    make([]*Ticker, 0),
		UpdateTime: time.Now(),
	}
}

// AddTicker 添加行情
func (tb *TickerBatch) AddTicker(ticker *Ticker) {
	tb.Tickers = append(tb.Tickers, ticker)
	tb.Count = len(tb.Tickers)
}