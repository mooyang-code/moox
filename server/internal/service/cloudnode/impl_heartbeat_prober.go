package cloudnode

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// HeartbeatProber 主动探测器（重命名避免与接口冲突）
type HeartbeatProber struct {
	heartbeatDAO dao.HeartbeatDAO
	config       *config.ProberConfig // 探测器配置

	ticker  *time.Ticker
	stopCh  chan struct{}
	running bool

	probers map[string]Prober // 已注册的探测器
	mu      sync.RWMutex      // 保护 probers 的读写锁
}

// NewProber 创建主动探测器
func NewProber(heartbeatDAO dao.HeartbeatDAO, cfg *config.ProberConfig) *HeartbeatProber {
	// 设置默认配置
	if cfg == nil {
		cfg = &config.ProberConfig{
			Enabled:       true,
			ScanInterval:  30 * time.Second,
			MaxConcurrent: 5,
		}
	}

	return &HeartbeatProber{
		heartbeatDAO: heartbeatDAO,
		config:       cfg,
		probers:      make(map[string]Prober),
	}
}

// RegisterProber 注册探测器
func (p *HeartbeatProber) RegisterProber(prober Prober) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probers[prober.Name()] = prober
}

// Start 启动探测器
func (p *HeartbeatProber) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("prober is already running")
	}

	// 检查探测器是否启用
	if !p.config.Enabled {
		log.Info("[HeartbeatProber] Prober is disabled in config, skip starting")
		return nil
	}

	// 使用配置的扫描间隔
	interval := p.config.ScanInterval
	if interval <= 0 {
		interval = 30 * time.Second // 默认30秒
	}

	log.Infof("[HeartbeatProber] Starting prober with scan interval: %v, max concurrent: %d",
		interval, p.config.MaxConcurrent)

	p.ticker = time.NewTicker(interval)
	p.stopCh = make(chan struct{})
	p.running = true
	go p.run()

	return nil
}

// Stop 停止探测器
func (p *HeartbeatProber) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	close(p.stopCh)
	p.ticker.Stop()
	p.running = false

	return nil
}

// IsRunning 检查探测器是否运行中
func (p *HeartbeatProber) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// run 探测器主循环
func (p *HeartbeatProber) run() {
	for {
		select {
		case <-p.ticker.C:
			if err := p.probeTimeoutNodes(context.Background()); err != nil {
				log.ErrorContextf(context.Background(), "[heartbeat] probe timeout nodes failed: %v", err)
			}
		case <-p.stopCh:
			return
		}
	}
}

// probeTimeoutNodes 探测所有超时节点
func (p *HeartbeatProber) probeTimeoutNodes(ctx context.Context) error {
	// 1. 获取超时节点
	timeoutRecords, err := p.heartbeatDAO.GetByStatus(ctx, types.NodeStatusTimeout)
	if err != nil {
		return fmt.Errorf("failed to get timeout nodes: %w", err)
	}
	if len(timeoutRecords) == 0 {
		return nil
	}

	// 2. 过滤启用探测的节点
	var enabledRecords []*types.HeartbeatNode
	for _, record := range timeoutRecords {
		if record.ProbeEnabled {
			enabledRecords = append(enabledRecords, record)
		}
	}

	if len(enabledRecords) == 0 {
		return nil
	}

	// 3. 分批并发探测节点（控制并发数）
	maxConcurrent := p.config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 100 // 默认最大并发数100
	}

	// 按照 maxConcurrent 分批处理
	for i := 0; i < len(enabledRecords); i += maxConcurrent {
		end := i + maxConcurrent
		if end > len(enabledRecords) {
			end = len(enabledRecords)
		}

		batch := enabledRecords[i:end]
		if err := p.probeBatch(ctx, batch); err != nil {
			log.ErrorContextf(ctx, "[heartbeat] probe batch failed: %v", err)
			// 继续处理下一批，不中断
		}
	}
	return nil
}

// probeBatch 使用 trpc.GoAndWait 并发探测一批节点
func (p *HeartbeatProber) probeBatch(ctx context.Context, records []*types.HeartbeatNode) error {
	var handlers []func() error

	for _, record := range records {
		r := record // 避免闭包问题
		handlers = append(handlers, func() error {
			if _, err := p.ProbeHeartbeatNode(ctx, r, "health"); err != nil {
				log.ErrorContextf(ctx, "[heartbeat] probe node %s:%s failed: %v", r.NodeID, r.NodeType, err)
				// 不返回错误，继续探测其他节点
				return nil
			}
			return nil
		})
	}
	return trpc.GoAndWait(handlers...)
}

// ProbeHeartbeatNode 探测心跳节点
func (p *HeartbeatProber) ProbeHeartbeatNode(ctx context.Context, record *types.HeartbeatNode, action string) (*types.ProbeResult, error) {
	// 1. 选择探测器
	prober := p.getProber(record.NodeType)
	if prober == nil {
		err := fmt.Errorf("prober not found for type: %s", record.NodeType)
		log.ErrorContextf(ctx, "[heartbeat] %v", err)
		return nil, err
	}

	// 2. 执行探测
	probeID := uuid.New().String()
	startTime := time.Now()
	timeout := 10 // 默认10秒超时

	// 添加调试日志
	log.InfoContextf(ctx, "[heartbeat] ProbeNode: nodeID=%s, nodeType=%s, action=%s, prober=%s",
		record.NodeID, record.NodeType, action, prober.Name())

	result, err := prober.Probe(ctx, &ProbeRequest{
		NodeID:   record.NodeID,
		NodeType: record.NodeType,
		ProbeURL: record.ProbeURL,
		Timeout:  timeout,
		Action:   action, // 传递动作参数
		Metadata: record.Metadata,
	})

	responseTime := time.Since(startTime).Milliseconds()
	success := err == nil && result != nil && result.Success

	// 3. 构造返回结果
	probeResult := &types.ProbeResult{
		ProbeID:      probeID,
		Success:      success,
		ResponseTime: int(responseTime),
		ProbeTime:    startTime,
		Details: map[string]interface{}{
			"action": action,
		},
	}

	if result != nil {
		probeResult.StatusCode = result.StatusCode
		if probeResult.Details == nil {
			probeResult.Details = make(map[string]interface{})
		}
		// 合并结果详情
		for k, v := range result.Details {
			probeResult.Details[k] = v
		}
	}

	if err != nil {
		probeResult.ErrorMessage = err.Error()
		log.ErrorContextf(ctx, "[heartbeat] probe failed for node %s: %v", record.NodeID, err)
	}
	return probeResult, nil
}

// getProber 获取探测器
func (p *HeartbeatProber) getProber(nodeType string) Prober {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// 根据节点类型选择对应的探测器
	switch nodeType {
	case "scf":
		// SCF类型使用SCF探测器
		if prober, exists := p.probers["scf"]; exists {
			return prober
		}
	case "server":
		// Server类型使用HTTP探测器
		if prober, exists := p.probers["http"]; exists {
			return prober
		}
	default:
		// 其他类型优先根据节点类型选择探测器
		if prober, exists := p.probers[nodeType]; exists {
			return prober
		}
	}

	// 默认使用HTTP探测器
	if prober, exists := p.probers["http"]; exists {
		return prober
	}
	return nil
}
