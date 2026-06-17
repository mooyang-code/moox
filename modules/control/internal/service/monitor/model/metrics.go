package model

import "time"

// HostMetrics 主机完整监控指标
type HostMetrics struct {
	HostID    int       `json:"host_id"`
	HostName  string    `json:"host_name"`
	Address   string    `json:"address"`
	Status    string    `json:"status"` // online/offline/error
	Timestamp time.Time `json:"timestamp"`
	ErrorMsg  string    `json:"error_msg,omitempty"`

	CPU      *CPUMetrics      `json:"cpu,omitempty"`
	Memory   *MemoryMetrics   `json:"memory,omitempty"`
	Disks    []*DiskMetrics   `json:"disks,omitempty"`
	Networks []*NetworkSpeed  `json:"networks,omitempty"`
	Load     *LoadMetrics     `json:"load,omitempty"`
}

// CPUMetrics CPU 指标
type CPUMetrics struct {
	Usage        float64 `json:"usage"`         // 使用率 0-100
	Cores        int     `json:"cores"`         // CPU 核心数
	TotalIdle    float64 `json:"-"`             // 内部使用（用于计算）
	TotalNonIdle float64 `json:"-"`             // 内部使用
}

// MemoryMetrics 内存指标
type MemoryMetrics struct {
	Total     int64   `json:"total"`      // 总内存 bytes
	Available int64   `json:"available"`  // 可用内存
	Used      int64   `json:"used"`       // 已用内存
	Free      int64   `json:"free"`       // 空闲内存
	Buffers   int64   `json:"buffers"`    // 缓冲区
	Cached    int64   `json:"cached"`     // 缓存
	Percent   float64 `json:"percent"`    // 使用率
}

// DiskMetrics 磁盘指标
type DiskMetrics struct {
	Device     string  `json:"device"`      // /dev/sda1
	Mountpoint string  `json:"mountpoint"`  // /
	FSType     string  `json:"fstype"`      // ext4
	Total      int64   `json:"total"`       // 总容量 bytes
	Used       int64   `json:"used"`        // 已用空间
	Available  int64   `json:"available"`   // 可用空间
	Percent    float64 `json:"percent"`     // 使用率
}

// NetworkSpeed 网络速率
type NetworkSpeed struct {
	Device  string `json:"device"`   // eth0
	RxSpeed int64  `json:"rx_speed"` // 接收速率 bytes/s
	TxSpeed int64  `json:"tx_speed"` // 发送速率 bytes/s
}

// LoadMetrics 系统负载
type LoadMetrics struct {
	Load1  float64 `json:"load1"`   // 1分钟负载
	Load5  float64 `json:"load5"`   // 5分钟负载
	Load15 float64 `json:"load15"`  // 15分钟负载
}

// NetworkMetrics 网络原始指标（内部使用）
type NetworkMetrics struct {
	Device  string
	RxBytes int64
	TxBytes int64
}
