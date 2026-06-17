package monitor

import (
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/monitor/model"
)

// Calculator 指标计算器（用于计算速率型指标）
type Calculator struct {
	// CPU 计算所需的上次数据
	lastCPU map[int]*model.CPUMetrics // key: host_id

	// 网络计算所需的上次数据
	lastNetwork map[string]*model.NetworkMetrics // key: host_id:device
	lastNetTime map[string]time.Time

	mu sync.RWMutex
}

func newCalculator() *Calculator {
	return &Calculator{
		lastCPU:     make(map[int]*model.CPUMetrics),
		lastNetwork: make(map[string]*model.NetworkMetrics),
		lastNetTime: make(map[string]time.Time),
	}
}

// CalculateCPUUsage 计算 CPU 使用率
func (c *Calculator) CalculateCPUUsage(hostID int, current *model.CPUMetrics) (float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	last, exists := c.lastCPU[hostID]
	if !exists {
		// 第一次采集，保存数据并返回 0
		c.lastCPU[hostID] = current
		return 0, nil
	}

	// 计算差值
	idleDelta := current.TotalIdle - last.TotalIdle
	nonIdleDelta := current.TotalNonIdle - last.TotalNonIdle
	totalDelta := idleDelta + nonIdleDelta

	if totalDelta == 0 {
		return 0, nil
	}

	// CPU 使用率 = (非空闲时间 / 总时间) * 100
	cpuUsage := (nonIdleDelta / totalDelta) * 100

	// 更新缓存
	c.lastCPU[hostID] = current

	return cpuUsage, nil
}

// CalculateNetworkSpeed 计算网络速率
func (c *Calculator) CalculateNetworkSpeed(hostID int, current []*model.NetworkMetrics, collectTime time.Time) []*model.NetworkSpeed {
	c.mu.Lock()
	defer c.mu.Unlock()

	var speeds []*model.NetworkSpeed

	for _, curr := range current {
		key := fmt.Sprintf("%d:%s", hostID, curr.Device)

		last, exists := c.lastNetwork[key]
		lastT, tExists := c.lastNetTime[key]

		// 先更新缓存
		c.lastNetwork[key] = curr
		c.lastNetTime[key] = collectTime

		// 卫语句：检查是否有历史数据
		if !exists || !tExists {
			continue
		}

		duration := collectTime.Sub(lastT).Seconds()
		// 卫语句：检查时间间隔是否合理
		if duration <= 0 || duration >= 3600 {
			continue
		}

		rxDelta := curr.RxBytes - last.RxBytes
		txDelta := curr.TxBytes - last.TxBytes

		// 卫语句：防止计数器重置（负数）
		if rxDelta < 0 || txDelta < 0 {
			continue
		}

		rxSpeed := float64(rxDelta) / duration
		txSpeed := float64(txDelta) / duration

		speeds = append(speeds, &model.NetworkSpeed{
			Device:  curr.Device,
			RxSpeed: int64(rxSpeed),
			TxSpeed: int64(txSpeed),
		})
	}

	return speeds
}
