package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
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

// CollectResult contains only storage-acknowledged output evidence.
type CollectResult struct {
	RowsWritten     uint64 `json:"rows_written"`
	OutputWatermark string `json:"output_watermark,omitempty"`
	SnapshotVersion string `json:"snapshot_version,omitempty"`
}

// ValidateCollectResult enforces the result evidence required by each data type.
func ValidateCollectResult(dataType string, result CollectResult) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode collect result: %w", err)
	}
	if len(raw) > 1024 {
		return fmt.Errorf("collect result exceeds 1 KiB")
	}
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "kline":
		if result.RowsWritten == 0 {
			return nil
		}
		if strings.TrimSpace(result.OutputWatermark) == "" {
			return fmt.Errorf("kline output_watermark is required when rows_written is positive")
		}
		watermark, err := time.Parse(time.RFC3339Nano, result.OutputWatermark)
		if err != nil {
			return fmt.Errorf("kline output_watermark must be RFC3339: %w", err)
		}
		if watermark.IsZero() {
			return fmt.Errorf("kline output_watermark must be a non-zero RFC3339 time")
		}
	case "symbol":
		if result.RowsWritten > 0 && strings.TrimSpace(result.SnapshotVersion) == "" {
			return fmt.Errorf("symbol snapshot_version is required when rows_written is positive")
		}
	default:
		return fmt.Errorf("unsupported collector data_type %q", dataType)
	}
	return nil
}

// NormalizeFreq returns the canonical Storage frequency for a collector interval.
func NormalizeFreq(interval string) (string, error) {
	interval = strings.TrimSpace(interval)
	if interval == "" {
		return "", fmt.Errorf("interval 不能为空")
	}
	count, err := strconv.ParseUint(interval[:len(interval)-1], 10, 64)
	if err != nil || count == 0 {
		return "", fmt.Errorf("interval %q 无效", interval)
	}
	unit := interval[len(interval)-1]
	switch unit {
	case 'h', 'H':
		return interval[:len(interval)-1] + "H", nil
	case 'd', 'D':
		return interval[:len(interval)-1] + "D", nil
	case 'w', 'W':
		return interval[:len(interval)-1] + "W", nil
	case 'y', 'Y':
		return interval[:len(interval)-1] + "Y", nil
	case 'm', 'M':
		return interval, nil
	default:
		return "", fmt.Errorf("interval %q 的单位不受支持", interval)
	}
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
	// DNSRoutes is the control-plane DNS snapshot carried by a short-lived
	// SCF task. The source keeps the original hostname for TLS SNI and uses the
	// resolved IPs only for the TCP dial.
	DNSRoutes map[string]DNSResolution
}

// DNSResolution is intentionally small: the scheduler only needs the
// hostname-to-address mapping captured before dispatching an SCF invocation.
type DNSResolution struct {
	IPs        []string          `json:"ips,omitempty"`
	ResolvedAt time.Time         `json:"resolved_at,omitempty"`
	LatencyMS  map[string]uint32 `json:"latency_ms,omitempty"`
}

// DNSIPs returns a defensive copy of the addresses for host.
func (p *CollectParams) DNSIPs(host string) []string {
	if p == nil || p.DNSRoutes == nil {
		return nil
	}
	route, ok := p.DNSRoutes[strings.ToLower(strings.TrimSpace(host))]
	if !ok {
		return nil
	}
	return append([]string(nil), route.IPs...)
}
