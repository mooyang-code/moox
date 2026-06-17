package collector

import (
	"context"
)

// Collector 采集器接口（简化版）
// 采集器只负责执行一次采集操作，不再管理生命周期和定时器
type Collector interface {
	// Source 数据源标识，如 "binance", "okx"
	Source() string
	// DataType 数据类型标识，如 "kline", "ticker", "news"
	DataType() string
	// Collect 执行一次采集
	// params 包含本次采集所需的参数（如交易对、周期等）
	Collect(ctx context.Context, params *CollectParams) error
}

// CollectParams 采集参数
type CollectParams struct {
	InstType string // 产品类型: SPOT, SWAP
	Symbol   string // 交易对: BTC-USDT
	Interval string // 周期（K线用）: 1m, 5m, 1h
}
