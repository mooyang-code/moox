package monitor

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/control/internal/service/monitor/model"
	dto "github.com/prometheus/client_model/go"
)

// Parser Prometheus 指标解析器
type Parser struct{}

// ExtractCPU 提取 CPU 原始指标
func (p *Parser) ExtractCPU(families map[string]*dto.MetricFamily) (*model.CPUMetrics, error) {
	family, ok := families["node_cpu_seconds_total"]
	if !ok {
		return nil, fmt.Errorf("node_cpu_seconds_total not found")
	}

	var totalIdle, totalNonIdle float64
	cpuCount := make(map[string]bool)

	for _, metric := range family.GetMetric() {
		cpuID := getLabel(metric, "cpu")
		mode := getLabel(metric, "mode")
		value := metric.GetCounter().GetValue()

		cpuCount[cpuID] = true

		if mode == "idle" || mode == "iowait" {
			totalIdle += value
		} else {
			totalNonIdle += value
		}
	}

	return &model.CPUMetrics{
		TotalIdle:    totalIdle,
		TotalNonIdle: totalNonIdle,
		Cores:        len(cpuCount),
	}, nil
}

// ExtractMemory 提取内存指标
func (p *Parser) ExtractMemory(families map[string]*dto.MetricFamily) (*model.MemoryMetrics, error) {
	memTotal := getGaugeValue(families, "node_memory_MemTotal_bytes")
	memAvailable := getGaugeValue(families, "node_memory_MemAvailable_bytes")
	memFree := getGaugeValue(families, "node_memory_MemFree_bytes")
	buffers := getGaugeValue(families, "node_memory_Buffers_bytes")
	cached := getGaugeValue(families, "node_memory_Cached_bytes")

	if memTotal == 0 {
		return nil, fmt.Errorf("memory metrics not found")
	}

	memUsed := memTotal - memAvailable
	memPercent := (memUsed / memTotal) * 100

	return &model.MemoryMetrics{
		Total:     int64(memTotal),
		Available: int64(memAvailable),
		Used:      int64(memUsed),
		Free:      int64(memFree),
		Buffers:   int64(buffers),
		Cached:    int64(cached),
		Percent:   memPercent,
	}, nil
}

// ExtractDisk 提取磁盘指标
func (p *Parser) ExtractDisk(families map[string]*dto.MetricFamily) ([]*model.DiskMetrics, error) {
	sizeFamily := families["node_filesystem_size_bytes"]
	availFamily := families["node_filesystem_avail_bytes"]

	if sizeFamily == nil {
		return nil, fmt.Errorf("filesystem metrics not found")
	}

	diskMap := make(map[string]*model.DiskMetrics)

	// 遍历所有文件系统
	for _, metric := range sizeFamily.GetMetric() {
		device := getLabel(metric, "device")
		mountpoint := getLabel(metric, "mountpoint")
		fstype := getLabel(metric, "fstype")

		// 过滤虚拟文件系统
		if isVirtualFS(fstype) || !strings.HasPrefix(device, "/dev/") {
			continue
		}

		key := device + mountpoint
		total := int64(metric.GetGauge().GetValue())

		diskMap[key] = &model.DiskMetrics{
			Device:     device,
			Mountpoint: mountpoint,
			FSType:     fstype,
			Total:      total,
		}
	}

	// 补充可用空间
	if availFamily != nil {
		for _, metric := range availFamily.GetMetric() {
			device := getLabel(metric, "device")
			mountpoint := getLabel(metric, "mountpoint")
			key := device + mountpoint

			if disk, exists := diskMap[key]; exists {
				avail := int64(metric.GetGauge().GetValue())
				disk.Available = avail
				disk.Used = disk.Total - avail
				if disk.Total > 0 {
					disk.Percent = float64(disk.Used) / float64(disk.Total) * 100
				}
			}
		}
	}

	var disks []*model.DiskMetrics
	for _, disk := range diskMap {
		disks = append(disks, disk)
	}

	return disks, nil
}

// ExtractNetwork 提取网络指标
func (p *Parser) ExtractNetwork(families map[string]*dto.MetricFamily) ([]*model.NetworkMetrics, error) {
	rxFamily := families["node_network_receive_bytes_total"]
	txFamily := families["node_network_transmit_bytes_total"]

	if rxFamily == nil {
		return nil, fmt.Errorf("network metrics not found")
	}

	networkMap := make(map[string]*model.NetworkMetrics)

	for _, metric := range rxFamily.GetMetric() {
		device := getLabel(metric, "device")

		// 过滤回环和虚拟网卡
		if device == "lo" || strings.HasPrefix(device, "veth") ||
			strings.HasPrefix(device, "docker") {
			continue
		}

		networkMap[device] = &model.NetworkMetrics{
			Device:  device,
			RxBytes: int64(metric.GetCounter().GetValue()),
		}
	}

	if txFamily != nil {
		for _, metric := range txFamily.GetMetric() {
			device := getLabel(metric, "device")
			if net, exists := networkMap[device]; exists {
				net.TxBytes = int64(metric.GetCounter().GetValue())
			}
		}
	}

	var networks []*model.NetworkMetrics
	for _, net := range networkMap {
		networks = append(networks, net)
	}

	return networks, nil
}

// ExtractLoad 提取系统负载
func (p *Parser) ExtractLoad(families map[string]*dto.MetricFamily) (*model.LoadMetrics, error) {
	load1 := getGaugeValue(families, "node_load1")
	load5 := getGaugeValue(families, "node_load5")
	load15 := getGaugeValue(families, "node_load15")

	if load1 == 0 && load5 == 0 && load15 == 0 {
		return nil, fmt.Errorf("load metrics not found")
	}

	return &model.LoadMetrics{
		Load1:  load1,
		Load5:  load5,
		Load15: load15,
	}, nil
}

// 辅助函数
func getLabel(metric *dto.Metric, name string) string {
	for _, label := range metric.GetLabel() {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}

func getGaugeValue(families map[string]*dto.MetricFamily, name string) float64 {
	family, ok := families[name]
	if !ok || len(family.GetMetric()) == 0 {
		return 0
	}
	return family.GetMetric()[0].GetGauge().GetValue()
}

func isVirtualFS(fstype string) bool {
	virtualFS := []string{"tmpfs", "devtmpfs", "sysfs", "proc", "cgroup", "cgroup2", "devpts"}
	for _, vfs := range virtualFS {
		if fstype == vfs {
			return true
		}
	}
	return false
}
