package cloudnode

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	"github.com/mooyang-code/moox/server/internal/service/database"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// 全局变量
var (
	globalProberInstance *HeartbeatProber // 全局探测器实例
	proberInstanceOnce   sync.Once        // 确保单例初始化
)

// HeartbeatProber 主动探测器（重命名避免与接口冲突）
type HeartbeatProber struct {
	config       *config.ProberConfig // 探测器配置
	heartbeatDAO dao.HeartbeatDAO

	probers map[string]Prober // 已注册的探测器
	mu      sync.RWMutex      // 保护 probers 的读写锁
}

// InitProberInstance 初始化全局探测器实例（供 bootstrap 调用）
func InitProberInstance(dbManager *database.Manager, cloudNodeCfg *config.Config) {
	proberInstanceOnce.Do(func() {
		log.Info("[HeartbeatProber] Initializing global prober instance...")

		// 创建 heartbeatDAO
		heartbeatDAO := dao.NewHeartbeatNodeDAO(dbManager.GetDB())

		// 创建探测器实例
		globalProberInstance = NewProber(heartbeatDAO, &cloudNodeCfg.Prober)

		// 将全局注册表中的探测器注册到 prober 实例
		// 注意：RegisterDefaultProbers 需要在外部调用，确保探测器已注册
		for _, proberInstance := range ListProbers() {
			globalProberInstance.RegisterProber(proberInstance)
		}
		log.Info("[HeartbeatProber] Global prober instance initialized")
	})
}

// HealthProbeSchedule trpc定时器[入口函数] - 定时健康探测（仅探测超时节点）
func HealthProbeSchedule(ctx context.Context, params string) error {
	ctxClone := trpc.CloneContext(ctx)
	log.InfoContextf(ctxClone, "[HeartbeatProber] Starting health probe schedule, params: %s", params)

	if globalProberInstance == nil {
		err := fmt.Errorf("prober instance not initialized")
		log.ErrorContextf(ctxClone, "[HeartbeatProber] %v", err)
		return err
	}

	// 执行探测超时节点
	if err := globalProberInstance.probeTimeoutNodes(ctxClone); err != nil {
		log.ErrorContextf(ctxClone, "[HeartbeatProber] Health probe failed: %v", err)
		return err
	}
	log.InfoContext(ctxClone, "[HeartbeatProber] Health probe schedule completed")
	return nil
}

// KeepaliveSchedule trpc定时器[入口函数] - 定时探测所有节点（用于保活）
func KeepaliveSchedule(ctx context.Context, params string) error {
	ctxClone := trpc.CloneContext(ctx)
	log.InfoContextf(ctxClone, "[KeepaliveSchedule] Starting all nodes probe schedule, params: %s", params)

	if globalProberInstance == nil {
		err := fmt.Errorf("prober instance not initialized")
		log.ErrorContextf(ctxClone, "[KeepaliveSchedule] %v", err)
		return err
	}

	// 执行探测所有节点
	if err := globalProberInstance.probeAllNodes(ctxClone); err != nil {
		log.ErrorContextf(ctxClone, "[KeepaliveSchedule] All nodes probe failed: %v", err)
		return err
	}
	log.InfoContext(ctxClone, "[KeepaliveSchedule] All nodes probe schedule completed")
	return nil
}

// NewProber 创建主动探测器
func NewProber(heartbeatDAO dao.HeartbeatDAO, cfg *config.ProberConfig) *HeartbeatProber {
	// 设置默认配置
	if cfg == nil {
		cfg = &config.ProberConfig{
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

// probeTimeoutNodes 探测所有超时节点
func (p *HeartbeatProber) probeTimeoutNodes(ctx context.Context) error {
	// 1. 获取超时节点
	timeoutRecords, err := p.heartbeatDAO.GetTimeoutNodes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get timeout nodes: %w", err)
	}
	if len(timeoutRecords) == 0 {
		return nil
	}

	// 2. 使用内置分批并发的probeBatch方法
	maxConcurrent := p.config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 100 // 默认最大并发数100
	}
	return p.probeBatch(ctx, timeoutRecords, maxConcurrent)
}

// probeAllNodes 探测所有注册的心跳节点
func (p *HeartbeatProber) probeAllNodes(ctx context.Context) error {
	log.InfoContextf(ctx, "[HeartbeatProber] Starting probe all nodes...")

	// 1. 获取所有心跳节点（包括正常和超时的）
	filter := &types.NodeFilter{
		// 可以根据需要设置过滤条件，比如只探测某些状态的节点
		// Status: "active", // 只探测活跃节点
	}

	allRecords, total, err := p.heartbeatDAO.List(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to get all heartbeat nodes: %w", err)
	}

	if len(allRecords) == 0 {
		log.InfoContext(ctx, "[HeartbeatProber] No heartbeat nodes found")
		return nil
	}

	log.InfoContextf(ctx, "[HeartbeatProber] Found %d nodes to probe", total)

	// 2. 使用内置分批并发的probeBatch方法
	maxConcurrent := p.config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 100 // 默认最大并发数100
	}
	return p.probeBatch(ctx, allRecords, maxConcurrent)
}

// probeBatch 使用 trpc.GoAndWait 并发探测节点列表，支持分批控制
// records: 要探测的节点列表
// maxConcurrent: 最大并发数
func (p *HeartbeatProber) probeBatch(ctx context.Context, records []*types.HeartbeatNode, maxConcurrent int) error {
	if len(records) == 0 {
		return nil
	}

	// 按照 maxConcurrent 分批处理
	for i := 0; i < len(records); i += maxConcurrent {
		end := i + maxConcurrent
		if end > len(records) {
			end = len(records)
		}

		batch := records[i:end]
		log.InfoContextf(ctx, "[HeartbeatProber] Probing batch: %d-%d of %d nodes", i+1, end, len(records))

		if err := p.probeSingleBatch(ctx, batch); err != nil {
			log.ErrorContextf(ctx, "[heartbeat] probe batch failed: %v", err)
			// 继续处理下一批，不中断
		}
	}
	return nil
}

// probeSingleBatch 探测单个批次（不做分批）
func (p *HeartbeatProber) probeSingleBatch(ctx context.Context, batch []*types.HeartbeatNode) error {
	var handlers []func() error

	for _, record := range batch {
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
	log.InfoContextf(ctx, "[heartbeat] ProbeHeartbeatNode: nodeID=%s, nodeType=%s, action=%s, prober=%s",
		record.NodeID, record.NodeType, action, prober.Name())

	// 构建探测请求，如果metadata中有probe_url则使用，否则使用默认值
	probeURL := ""
	if record.Metadata != nil {
		if url, ok := record.Metadata["probe_url"].(string); ok && url != "" {
			probeURL = url
		}
	}
	// 如果没有metadata中的probe_url，根据节点类型构建默认的探测URL
	if probeURL == "" {
		switch record.NodeType {
		case "scf":
			probeURL = fmt.Sprintf("https://scf.tencentcloudapi.com/%s", record.NodeID)
		case "server":
			probeURL = fmt.Sprintf("http://%s:8080/health", record.NodeID)
		default:
			probeURL = fmt.Sprintf("http://%s:8080/health", record.NodeID)
		}
	}

	result, err := prober.Probe(ctx, &ProbeRequest{
		NodeID:   record.NodeID,
		NodeType: record.NodeType,
		ProbeURL: probeURL,
		Timeout:  timeout,
		Action:   action,
		Metadata: record.Metadata,
	})
	responseTime := time.Since(startTime).Milliseconds()

	// 3. 构造返回结果
	probeResult := &types.ProbeResult{
		ProbeID:   probeID,
		CostTime:  int(responseTime),
		ProbeTime: startTime.UnixMilli(), // 本地探测时间戳
	}

	if result != nil {
		// 提取ProbeResponse的核心字段
		if result.NodeID != "" {
			probeResult.NodeID = result.NodeID
		}
		if result.State != "" {
			probeResult.State = result.State
		}
		if result.Timestamp != "" {
			probeResult.RemoteTimestamp = result.Timestamp
		}
		if result.OSName != "" {
			probeResult.OSName = result.OSName
		}
		if result.FunctionVersion != "" {
			probeResult.FunctionVersion = result.FunctionVersion
		}
		if result.RequestID != "" {
			probeResult.RequestID = result.RequestID
		}
	}
	if err != nil {
		probeResult.ErrorMessage = err.Error()
		log.ErrorContextf(ctx, "[heartbeat] probe failed for node %s: %v", record.NodeID, err)
	}

	// 4. 更新心跳节点表信息
	if updateErr := p.updateHeartbeatNodeFromProbe(ctx, record.NodeID, record.NodeType, probeResult, result); updateErr != nil {
		log.ErrorContextf(ctx, "[heartbeat] failed to update heartbeat node %s after probe: %v", record.NodeID, updateErr)
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

// updateHeartbeatNodeFromProbe 从探测结果更新心跳节点信息
func (p *HeartbeatProber) updateHeartbeatNodeFromProbe(ctx context.Context, nodeID, nodeType string,
	probeResult *types.ProbeResult, probeResponse *ProbeResponse) error {
	// 1. 获取现有心跳记录
	nodeRecord, err := p.heartbeatDAO.GetNodeByID(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node record failed:%s %w", nodeID, err)
	}

	// 如果记录不存在，创建新的记录
	if nodeRecord == nil {
		nodeRecord = &types.HeartbeatNode{
			NodeID:   nodeID,
			NodeType: nodeType,
		}
	}

	// 2. 更新探测相关字段
	now := time.Now()
	probeResultBool := probeResult.ErrorMessage == ""

	nodeRecord.LastProbeTime = &now
	if probeResultBool {
		nodeRecord.LastProbeResult = "success"
	} else {
		nodeRecord.LastProbeResult = "failed"
	}

	// 4. 更新扩展元数据
	if nodeRecord.Metadata == nil {
		nodeRecord.Metadata = make(map[string]interface{})
	}

	// 添加探测结果信息
	metadata := nodeRecord.Metadata
	metadata["last_probe_cost_time"] = probeResult.CostTime
	metadata["last_probe_request_id"] = probeResult.RequestID
	metadata["last_probe_state"] = probeResult.State
	metadata["last_probe_remote_timestamp"] = probeResult.RemoteTimestamp

	// 添加系统信息（如果探测结果中有）
	if probeResponse != nil {
		if probeResponse.OSName != "" {
			metadata["os_name"] = probeResponse.OSName
		}
		if probeResponse.FunctionVersion != "" {
			metadata["function_version"] = probeResponse.FunctionVersion
		}
	}

	// 5. 更新记录
	if nodeRecord.ID > 0 {
		// 更新现有记录
		return p.heartbeatDAO.Update(ctx, nodeRecord)
	} else {
		// 创建新记录
		// 设置初始统计数据
		nodeRecord.FirstHeartbeat = &now
		nodeRecord.TotalHeartbeats = 1
		nodeRecord.ConsecutiveTimeouts = 0
		nodeRecord.TotalTimeouts = 0
		return p.heartbeatDAO.Create(ctx, nodeRecord)
	}
}
