package sources

import (
	"context"
)

// Collector 采集器接口（简化版）
// 采集器只负责执行一次采集操作，不再管理生命周期和定时器
type Collector interface {
	// Source 数据源标识，当前内置 "binance"，未来可扩展
	Source() string
	// DataType 数据类型标识，内置支持 "kline" 和 "symbol"
	DataType() string
	// Collect 执行一次采集
	// params 包含本次采集所需的参数（如交易对、周期等）
	Collect(ctx context.Context, params *CollectParams) error
}

// ResultCollector is implemented by collectors that can prove what the current
// invocation wrote. Collector remains the small extension boundary for sources
// that do not have row-oriented output.
type ResultCollector interface {
	CollectWithResult(ctx context.Context, params *CollectParams) (CollectResult, error)
}

// CollectResult is persisted with the JobItem and TaskInstance so callers can
// distinguish an intentional zero-write collection from a false success.
type CollectResult struct {
	RowsWritten          int               `json:"rows_written"`
	WrittenRowKeySamples []WrittenRowKey   `json:"written_row_key_samples,omitempty"`
	ZeroWriteReason      string            `json:"zero_write_reason,omitempty"`
	StorageReadScope     *StorageReadScope `json:"storage_read_scope,omitempty"`
}

type StorageReadScope struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	Freq      string `json:"freq"`
}

type WrittenRowKey struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	Freq      string `json:"freq"`
	DataTime  string `json:"data_time"`
}

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
