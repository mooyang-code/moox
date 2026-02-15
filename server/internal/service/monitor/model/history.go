package model

import "time"

// MonitorHistory 监控历史数据模型
type MonitorHistory struct {
	ID              int       `gorm:"column:c_id;primaryKey"`
	HostAddress     string    `gorm:"column:c_host_address"`
	CPUUsage        float64   `gorm:"column:c_cpu_usage"`
	CPUCores        int       `gorm:"column:c_cpu_cores"`
	MemoryTotal     int64     `gorm:"column:c_memory_total"`
	MemoryUsed      int64     `gorm:"column:c_memory_used"`
	MemoryAvailable int64     `gorm:"column:c_memory_available"`
	MemoryPercent   float64   `gorm:"column:c_memory_percent"`
	DiskTotal       int64     `gorm:"column:c_disk_total"`
	DiskUsed        int64     `gorm:"column:c_disk_used"`
	DiskPercent     float64   `gorm:"column:c_disk_percent"`
	NetworkDevice   string    `gorm:"column:c_network_device"`
	NetworkRxSpeed  int64     `gorm:"column:c_network_rx_speed"`
	NetworkTxSpeed  int64     `gorm:"column:c_network_tx_speed"`
	Load1           float64   `gorm:"column:c_load1"`
	Load5           float64   `gorm:"column:c_load5"`
	Load15          float64   `gorm:"column:c_load15"`
	CollectTime     time.Time `gorm:"column:c_collect_time"`
	CreateTime      time.Time `gorm:"column:c_ctime"`
}

// TableName 指定表名
func (MonitorHistory) TableName() string {
	return "t_host_monitor_history"
}

// HistoryPoint 历史数据点（用于前端图表）
type HistoryPoint struct {
	Timestamp      time.Time `json:"timestamp"`
	CPUUsage       float64   `json:"cpu_usage"`
	MemoryPercent  float64   `json:"memory_percent"`
	DiskPercent    float64   `json:"disk_percent"`
	NetworkRxSpeed int64     `json:"network_rx_speed"`
	NetworkTxSpeed int64     `json:"network_tx_speed"`
}
