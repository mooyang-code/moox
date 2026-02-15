package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/database"
	"github.com/mooyang-code/moox/server/internal/service/monitor/dao"
	"github.com/mooyang-code/moox/server/internal/service/monitor/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// serviceImpl 服务实现
type serviceImpl struct {
	sshHostDAO *dao.SSHHostDAO
	historyDAO *dao.MonitorHistoryDAO
	collector  *Collector
	parser     *Parser
	calculator *Calculator
}

// NewService 创建监控服务实例
func NewService(dbManager *database.Manager) Service {
	db := dbManager.GetDB()
	svc := &serviceImpl{
		sshHostDAO: dao.NewSSHHostDAO(db),
		historyDAO: dao.NewMonitorHistoryDAO(db),
		collector:  newCollector(),
		parser:     &Parser{},
		calculator: newCalculator(),
	}

	return svc
}

// EnableMonitor 启用监控
func (s *serviceImpl) EnableMonitor(ctx context.Context, hostID int) error {
	return s.sshHostDAO.SetMonitorEnabled(ctx, hostID, true)
}

// DisableMonitor 禁用监控
func (s *serviceImpl) DisableMonitor(ctx context.Context, hostID int) error {
	return s.sshHostDAO.SetMonitorEnabled(ctx, hostID, false)
}

// IsMonitorEnabled 检查是否启用监控
func (s *serviceImpl) IsMonitorEnabled(ctx context.Context, hostID int) (bool, error) {
	return s.sshHostDAO.IsMonitorEnabled(ctx, hostID)
}

// GetCurrentMetrics 获取当前监控指标
func (s *serviceImpl) GetCurrentMetrics(ctx context.Context, hostIDs []int) ([]*model.HostMetrics, error) {
	// 获取启用监控的主机列表
	hosts, err := s.sshHostDAO.ListMonitorHosts(ctx, hostIDs)
	if err != nil {
		return nil, err
	}

	if len(hosts) == 0 {
		return []*model.HostMetrics{}, nil
	}

	// 并发采集
	rawMetricsMap := s.collector.CollectBatch(ctx, hosts)

	// 处理指标
	var results []*model.HostMetrics
	for _, host := range hosts {
		raw, ok := rawMetricsMap[host.ID]
		if !ok {
			// 采集失败，返回离线状态
			results = append(results, &model.HostMetrics{
				HostID:   host.ID,
				HostName: host.Name,
				Address:  host.Address,
				Status:   "offline",
				ErrorMsg: "collection failed or timeout",
			})
			continue
		}

		// 处理指标
		metrics, err := s.processMetrics(ctx, raw)
		if err != nil {
			log.Errorf("Process metrics failed for host %s: %v", host.Name, err)
			results = append(results, &model.HostMetrics{
				HostID:   host.ID,
				HostName: host.Name,
				Address:  host.Address,
				Status:   "error",
				ErrorMsg: err.Error(),
			})
			continue
		}

		results = append(results, metrics)
	}

	return results, nil
}

// GetHistoryMetrics 获取历史监控数据
func (s *serviceImpl) GetHistoryMetrics(ctx context.Context, hostAddress string, duration string) ([]*model.HistoryPoint, error) {
	histories, err := s.historyDAO.Query(ctx, hostAddress, duration)
	if err != nil {
		return nil, err
	}

	// 转换为前端需要的格式
	points := make([]*model.HistoryPoint, 0, len(histories))
	for _, h := range histories {
		points = append(points, &model.HistoryPoint{
			Timestamp:      h.CollectTime,
			CPUUsage:       h.CPUUsage,
			MemoryPercent:  h.MemoryPercent,
			DiskPercent:    h.DiskPercent,
			NetworkRxSpeed: h.NetworkRxSpeed,
			NetworkTxSpeed: h.NetworkTxSpeed,
		})
	}

	return points, nil
}

// TestNodeExporter 测试连通性
func (s *serviceImpl) TestNodeExporter(ctx context.Context, hostID int) (*TestResult, error) {
	host, err := s.sshHostDAO.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}

	// 尝试采集
	hosts := []*dao.MonitorHost{host}
	rawMetricsMap := s.collector.CollectBatch(ctx, hosts)

	raw, ok := rawMetricsMap[host.ID]
	if !ok {
		return &TestResult{
			Reachable: false,
			Message:   "Connection failed or timeout",
		}, nil
	}

	return &TestResult{
		Reachable:    true,
		Message:      "Connected successfully",
		DurationMs:   raw.Duration.Milliseconds(),
		MetricsCount: len(raw.MetricFamilies),
	}, nil
}

// CollectAll 执行完整采集（由定时器调用）
func (s *serviceImpl) CollectAll(ctx context.Context) error {
	startTime := time.Now()

	// 1. 获取所有启用监控的主机
	hosts, err := s.sshHostDAO.ListMonitorHosts(ctx, nil)
	if err != nil {
		return fmt.Errorf("get monitor hosts failed: %w", err)
	}

	if len(hosts) == 0 {
		log.InfoContext(ctx, "[Monitor] No hosts enabled for monitoring")
		return nil
	}

	log.InfoContextf(ctx, "[Monitor] Start collecting %d hosts", len(hosts))

	// 2. 并发采集所有主机
	rawMetricsMap := s.collector.CollectBatch(ctx, hosts)

	// 3. 处理并存储每个主机的数据
	successCount := 0
	for _, host := range hosts {
		raw, ok := rawMetricsMap[host.ID]
		if !ok {
			log.WarnContextf(ctx, "[Monitor] Host %s collection failed", host.Name)
			continue
		}

		// 处理指标
		metrics, err := s.processMetrics(ctx, raw)
		if err != nil {
			log.ErrorContextf(ctx, "[Monitor] Process metrics failed for host %s: %v", host.Name, err)
			continue
		}

		// 存储到历史表
		if err := s.saveToHistory(ctx, metrics); err != nil {
			log.ErrorContextf(ctx, "[Monitor] Save history failed for host %s: %v", host.Name, err)
			continue
		}

		successCount++
	}

	log.InfoContextf(ctx, "[Monitor] Collection completed: %d/%d success in %v",
		successCount, len(hosts), time.Since(startTime))

	return nil
}

// processMetrics 处理原始指标
func (s *serviceImpl) processMetrics(ctx context.Context, raw *RawMetrics) (*model.HostMetrics, error) {
	// 提取原始指标
	cpuRaw, err := s.parser.ExtractCPU(raw.MetricFamilies)
	if err != nil {
		return nil, fmt.Errorf("extract CPU failed: %w", err)
	}

	memory, err := s.parser.ExtractMemory(raw.MetricFamilies)
	if err != nil {
		return nil, fmt.Errorf("extract memory failed: %w", err)
	}

	disks, _ := s.parser.ExtractDisk(raw.MetricFamilies)
	networkRaw, _ := s.parser.ExtractNetwork(raw.MetricFamilies)
	load, _ := s.parser.ExtractLoad(raw.MetricFamilies)

	// 计算 CPU 使用率
	cpuUsage, _ := s.calculator.CalculateCPUUsage(raw.HostID, cpuRaw)
	cpuRaw.Usage = cpuUsage

	// 计算网络速率
	networkSpeeds := s.calculator.CalculateNetworkSpeed(raw.HostID, networkRaw, raw.CollectTime)

	return &model.HostMetrics{
		HostID:    raw.HostID,
		HostName:  raw.HostName,
		Address:   raw.Address,
		Status:    "online",
		Timestamp: raw.CollectTime,
		CPU:       cpuRaw,
		Memory:    memory,
		Disks:     disks,
		Networks:  networkSpeeds,
		Load:      load,
	}, nil
}

// saveToHistory 保存到历史表
func (s *serviceImpl) saveToHistory(ctx context.Context, metrics *model.HostMetrics) error {
	// 提取主网卡数据
	var networkDevice string
	var networkRxSpeed, networkTxSpeed int64
	if len(metrics.Networks) > 0 {
		networkDevice = metrics.Networks[0].Device
		networkRxSpeed = metrics.Networks[0].RxSpeed
		networkTxSpeed = metrics.Networks[0].TxSpeed
	}

	// 提取根分区数据
	diskTotal, diskUsed, diskPercent := s.extractDiskData(metrics.Disks)

	history := &model.MonitorHistory{
		HostAddress:     metrics.Address,
		CPUUsage:        metrics.CPU.Usage,
		CPUCores:        metrics.CPU.Cores,
		MemoryTotal:     metrics.Memory.Total,
		MemoryUsed:      metrics.Memory.Used,
		MemoryAvailable: metrics.Memory.Available,
		MemoryPercent:   metrics.Memory.Percent,
		DiskTotal:       diskTotal,
		DiskUsed:        diskUsed,
		DiskPercent:     diskPercent,
		NetworkDevice:   networkDevice,
		NetworkRxSpeed:  networkRxSpeed,
		NetworkTxSpeed:  networkTxSpeed,
		Load1:           metrics.Load.Load1,
		Load5:           metrics.Load.Load5,
		Load15:          metrics.Load.Load15,
		CollectTime:     metrics.Timestamp,
		CreateTime:      time.Now(),
	}

	return s.historyDAO.Insert(ctx, history)
}

// extractDiskData 提取磁盘数据（优先根分区，否则第一个分区）
func (s *serviceImpl) extractDiskData(disks []*model.DiskMetrics) (total, used int64, percent float64) {
	if len(disks) == 0 {
		return 0, 0, 0
	}

	// 查找根分区
	for _, disk := range disks {
		if disk.Mountpoint == "/" {
			return disk.Total, disk.Used, disk.Percent
		}
	}

	// 没有根分区，使用第一个分区
	return disks[0].Total, disks[0].Used, disks[0].Percent
}

// CleanHistory 清理历史监控数据
func (s *serviceImpl) CleanHistory(ctx context.Context, keepDays int) error {
	affected, err := s.historyDAO.CleanOldData(ctx, keepDays)
	if err != nil {
		return fmt.Errorf("clean history failed: %w", err)
	}

	log.InfoContextf(ctx, "[Monitor] Cleaned %d history records older than %d days", affected, keepDays)
	return nil
}
