package market

import (
	"encoding/json"
	"fmt"
	"time"
	
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

// PriceLevel 价格档位
type PriceLevel struct {
	Price    common.Decimal `json:"price"`
	Quantity common.Decimal `json:"quantity"`
}

// OrderBook 订单簿数据
type OrderBook struct {
	common.BaseDataPoint
	Symbol     string        `json:"symbol"`
	Exchange   string        `json:"exchange"`
	Bids       []PriceLevel  `json:"bids"`       // 买单
	Asks       []PriceLevel  `json:"asks"`       // 卖单
	UpdateID   int64         `json:"update_id"`
	UpdateTime time.Time     `json:"update_time"`
}

// NewOrderBook 创建订单簿
func NewOrderBook(exchange, symbol string) *OrderBook {
	return &OrderBook{
		BaseDataPoint: common.NewBaseDataPoint(exchange, "orderbook"),
		Exchange:      exchange,
		Symbol:        symbol,
		Bids:          make([]PriceLevel, 0),
		Asks:          make([]PriceLevel, 0),
		UpdateTime:    time.Now(),
	}
}

// AddBid 添加买单
func (ob *OrderBook) AddBid(price, quantity string) {
	ob.Bids = append(ob.Bids, PriceLevel{
		Price:    common.NewDecimal(price),
		Quantity: common.NewDecimal(quantity),
	})
}

// AddAsk 添加卖单
func (ob *OrderBook) AddAsk(price, quantity string) {
	ob.Asks = append(ob.Asks, PriceLevel{
		Price:    common.NewDecimal(price),
		Quantity: common.NewDecimal(quantity),
	})
}

// Validate 验证订单簿数据
func (ob *OrderBook) Validate() error {
	if ob.Symbol == "" {
		return fmt.Errorf("交易对不能为空")
	}
	if ob.Exchange == "" {
		return fmt.Errorf("交易所不能为空")
	}
	
	// 验证买单价格递减
	for i := 1; i < len(ob.Bids); i++ {
		prev, _ := ob.Bids[i-1].Price.Float64()
		curr, _ := ob.Bids[i].Price.Float64()
		if prev < curr {
			return fmt.Errorf("买单价格必须递减排序")
		}
	}
	
	// 验证卖单价格递增
	for i := 1; i < len(ob.Asks); i++ {
		prev, _ := ob.Asks[i-1].Price.Float64()
		curr, _ := ob.Asks[i].Price.Float64()
		if prev > curr {
			return fmt.Errorf("卖单价格必须递增排序")
		}
	}
	
	// 验证买一价格低于卖一价格
	if len(ob.Bids) > 0 && len(ob.Asks) > 0 {
		bestBid, _ := ob.Bids[0].Price.Float64()
		bestAsk, _ := ob.Asks[0].Price.Float64()
		if bestBid >= bestAsk {
			return fmt.Errorf("最高买价必须低于最低卖价")
		}
	}
	
	return nil
}

// GetSpread 获取买卖价差
func (ob *OrderBook) GetSpread() (common.Decimal, error) {
	if len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return common.Zero(), fmt.Errorf("订单簿为空")
	}
	
	bestBid, _ := ob.Bids[0].Price.Float64()
	bestAsk, _ := ob.Asks[0].Price.Float64()
	
	spread := bestAsk - bestBid
	return common.NewDecimalFromFloat(spread), nil
}

// GetMidPrice 获取中间价
func (ob *OrderBook) GetMidPrice() (common.Decimal, error) {
	if len(ob.Bids) == 0 || len(ob.Asks) == 0 {
		return common.Zero(), fmt.Errorf("订单簿为空")
	}
	
	bestBid, _ := ob.Bids[0].Price.Float64()
	bestAsk, _ := ob.Asks[0].Price.Float64()
	
	midPrice := (bestBid + bestAsk) / 2
	return common.NewDecimalFromFloat(midPrice), nil
}

// Marshal 序列化
func (ob *OrderBook) Marshal() ([]byte, error) {
	return json.Marshal(ob)
}

// Unmarshal 反序列化
func (ob *OrderBook) Unmarshal(data []byte) error {
	return json.Unmarshal(data, ob)
}