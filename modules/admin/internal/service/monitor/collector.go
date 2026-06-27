package monitor

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor/dao"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"trpc.group/trpc-go/trpc-go"
)

// Collector HTTP 采集器
type Collector struct {
	client *http.Client
}

func newCollector() *Collector {
	monitorCfg := config.GetMonitorConfig()
	timeout := time.Duration(monitorCfg.CollectTimeout) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	return &Collector{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// RawMetrics 原始采集数据
type RawMetrics struct {
	HostID         int
	HostName       string
	Address        string
	MetricFamilies map[string]*dto.MetricFamily
	CollectTime    time.Time
	Duration       time.Duration
}

// CollectBatch 批量采集（并发）
func (c *Collector) CollectBatch(ctx context.Context, hosts []*dao.MonitorHost) map[int]*RawMetrics {
	if len(hosts) == 0 {
		return make(map[int]*RawMetrics)
	}

	results := make(map[int]*RawMetrics)
	resultMu := sync.Mutex{}

	// 从配置获取并发限制
	monitorCfg := config.GetMonitorConfig()
	batchSize := monitorCfg.ConcurrentLimit
	if batchSize <= 0 {
		batchSize = 20
	}

	// 使用 trpc.GoAndWait 实现并发采集（批量处理）
	for start := 0; start < len(hosts); start += batchSize {
		end := start + batchSize
		if end > len(hosts) {
			end = len(hosts)
		}

		handlers := make([]func() error, 0, end-start)
		for _, host := range hosts[start:end] {
			host := host // 避免闭包问题
			handlers = append(handlers, func() error {
				raw, err := c.collect(ctx, host)
				if err != nil {
					// 采集失败，不记录到结果中
					return nil
				}

				resultMu.Lock()
				results[host.ID] = raw
				resultMu.Unlock()
				return nil
			})
		}

		if len(handlers) > 0 {
			_ = trpc.GoAndWait(handlers...)
		}
	}

	return results
}

// collect 采集单个主机
func (c *Collector) collect(ctx context.Context, host *dao.MonitorHost) (*RawMetrics, error) {
	// 从配置获取 Node Exporter 端口
	monitorCfg := config.GetMonitorConfig()
	port := monitorCfg.NodeExporterPort
	if port == 0 {
		port = 9100
	}

	url := fmt.Sprintf("http://%s:%d/metrics", host.Address, port)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	startTime := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// 解析 Prometheus 文本格式
	// 必须使用 NewTextParser 传入验证方案，直接构造 TextParser{} 时 scheme 为零值(UnsetValidation)会导致 panic
	parser := expfmt.NewTextParser(model.UTF8Validation)
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse metrics failed: %w", err)
	}

	return &RawMetrics{
		HostID:         host.ID,
		HostName:       host.Name,
		Address:        host.Address,
		MetricFamilies: metricFamilies,
		CollectTime:    startTime,
		Duration:       time.Since(startTime),
	}, nil
}
