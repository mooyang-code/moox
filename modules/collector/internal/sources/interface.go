package sources

import (
	"time"
)

// CollectParams 采集参数
type CollectParams struct {
	SpaceID   string // 业务空间
	DatasetID string // 本次任务的真实写入目标数据集
	InstType  string // 产品类型: SPOT, SWAP
	Symbol    string // 外部交易对: BTCUSDT，用于请求交易所
	SubjectID string // MooX 数据对象ID: BTC-USDT，用于写入 storage
	Interval  string // 周期（K线用）: 1m, 5m, 1h
	Live      bool   // 是否由实时调度触发；闭合 K 线仍直接写入 Storage
}

// DNSResolution 是一个域名解析到的地址，collector-stream 的本地 DNS 缓存（dnscache）使用。
type DNSResolution struct {
	IPs        []string  `json:"ips,omitempty"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}
