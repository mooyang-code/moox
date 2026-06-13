package model

import (
	"encoding/json"
	"time"
)

// TaskType 任务类型
type TaskType string

const (
	TaskTypeKLine     TaskType = "kline"
	TaskTypeTicker    TaskType = "ticker"
	TaskTypeOrderBook TaskType = "orderbook"
	TaskTypeTrade     TaskType = "trade"
)

// TaskStatus 任务状态
type TaskStatus string

const (
	TaskStatusPending TaskStatus = "pending"
	TaskStatusRunning TaskStatus = "running"
	TaskStatusStopped TaskStatus = "stopped"
	TaskStatusError   TaskStatus = "error"
)

// CollectorType 采集器类型
type CollectorType string

const (
	CollectorTypeBinance CollectorType = "binance"
	CollectorTypeOKX     CollectorType = "okx"
	CollectorTypeHuobi   CollectorType = "huobi"
)

// EventAction 事件类型
type EventAction string

const (
	EventActionTask      EventAction = "task"
	EventActionKeepalive EventAction = "keepalive"
)

// NodeStatus 节点状态
type NodeStatus string

const (
	NodeStatusOffline  NodeStatus = "offline"
	NodeStatusOnline   NodeStatus = "online"
	NodeStatusTimeout  NodeStatus = "timeout"
	NodeStatusAbnormal NodeStatus = "abnormal"
)

// Task 任务定义
type Task struct {
	ID         string          `json:"id"`
	Type       TaskType        `json:"type"`
	Exchange   string          `json:"exchange"`
	Symbol     string          `json:"symbol"`
	Interval   string          `json:"interval,omitempty"`
	Schedule   string          `json:"schedule"` // cron表达式
	Config     json.RawMessage `json:"config"`   // 任务特定配置
	Status     TaskStatus      `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	LastRun    *time.Time      `json:"last_run,omitempty"`
	NextRun    *time.Time      `json:"next_run,omitempty"`
	Statistics *TaskStats      `json:"statistics,omitempty"`
}

// TaskStats 任务统计信息
type TaskStats struct {
	TotalRuns    int64      `json:"total_runs"`
	SuccessRuns  int64      `json:"success_runs"`
	FailedRuns   int64      `json:"failed_runs"`
	LastSuccess  *time.Time `json:"last_success,omitempty"`
	LastError    *time.Time `json:"last_error,omitempty"`
	LastErrorMsg string     `json:"last_error_msg,omitempty"`
	AvgDuration  float64    `json:"avg_duration"` // 平均执行时间(秒)
}

// TaskSummary 任务摘要（用于心跳上报）
type TaskSummary struct {
	ID      string     `json:"id"`
	Type    TaskType   `json:"type"`
	Status  TaskStatus `json:"status"`
	LastRun *time.Time `json:"last_run,omitempty"`
	NextRun *time.Time `json:"next_run,omitempty"`
}

// NodeInfo 节点信息
type NodeInfo struct {
	NodeID       string            `json:"node_id"`
	NodeType     string            `json:"node_type"`
	Region       string            `json:"region"`
	Namespace    string            `json:"namespace"`
	Version      string            `json:"version"`
	RunningTasks []string          `json:"running_tasks"`
	Capabilities []CollectorType   `json:"capabilities"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// NodeMetrics 节点指标
type NodeMetrics struct {
	CPUUsage    float64   `json:"cpu_usage"`
	MemoryUsage float64   `json:"memory_usage"`
	TaskCount   int       `json:"task_count"`
	SuccessRate float64   `json:"success_rate"`
	ErrorCount  int64     `json:"error_count"`
	LastError   string    `json:"last_error,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// HeartbeatPayload 心跳上报数据
type HeartbeatPayload struct {
	NodeID              string                 `json:"node_id"`
	NodeType            string                 `json:"node_type"`
	Timestamp           time.Time              `json:"timestamp"`
	RunningTasks        []*TaskSummary         `json:"running_tasks"`
	Metrics             *NodeMetrics           `json:"metrics"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
	SupportedCollectors []string               `json:"supported_collectors,omitempty"` // 支持的采集器数据类型
	TasksMD5            string                 `json:"tasks_md5"`                      // 当前任务列表MD5值
	LocalDNSRecords     []*LocalDNSReportItem  `json:"local_dns_records,omitempty"`    // 本地解析的 DNS 记录
}

// LocalDNSReportItem 本地 DNS 解析结果（用于上报）
type LocalDNSReportItem struct {
	Domain    string    `json:"domain"`     // 域名
	IPList    []string  `json:"ip_list"`    // 可用的 IP 列表（按延迟排序）
	ResolveAt time.Time `json:"resolve_at"` // 解析时间
}

// CloudFunctionEvent 云函数事件
type CloudFunctionEvent struct {
	Action     EventAction            `json:"action,omitempty"`
	Data       map[string]interface{} `json:"data,omitempty"`
	Timestamp  string                 `json:"timestamp"` // 使用时间格式字符串（支持时区）
	RequestID  string                 `json:"request_id,omitempty"`
	Source     string                 `json:"source,omitempty"` // 探测来源标识
	ServerIP   string                 `json:"server_ip"`        // 服务端IP
	ServerPort int                    `json:"server_port"`      // 服务端心跳API端口
}

// TaskExecuteEvent 任务立即执行事件（服务端触发）
type TaskExecuteEvent struct {
	TaskID     string   `json:"task_id"`
	DataType   string   `json:"data_type"`
	DataSource string   `json:"data_source"`
	InstType   string   `json:"inst_type"`
	Symbol     string   `json:"symbol"`
	Intervals  []string `json:"intervals"`
	Immediate  bool     `json:"immediate"` // 是否立即执行
}

// ProbeResponse 心跳探测响应
type ProbeResponse struct {
	NodeID    string       `json:"node_id"`
	State     string       `json:"state"`
	Timestamp time.Time    `json:"timestamp"`
	Details   ProbeDetails `json:"details"`
	Metadata  interface{}  `json:"metadata,omitempty"`
}

// ProbeDetails 心跳探测详情
type ProbeDetails struct {
	NodeInfo      *NodeInfo      `json:"node_info"`
	RunningTasks  []*TaskSummary `json:"running_tasks"`
	TaskStats     TaskStatsInfo  `json:"task_stats"`
	Metrics       *NodeMetrics   `json:"metrics"`
	SystemInfo    SystemInfo     `json:"system_info"`
	HeartbeatInfo HeartbeatInfo  `json:"heartbeat_info"`
}

// TaskStatsInfo 任务统计信息
type TaskStatsInfo struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Pending int `json:"pending"`
	Stopped int `json:"stopped"`
	Error   int `json:"error"`
}

// SystemInfo 系统信息
type SystemInfo struct {
	GoVersion    string `json:"go_version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	NumCPU       int    `json:"num_cpu"`
	NumGoroutine int    `json:"num_goroutine"`
}

// HeartbeatInfo 心跳统计信息
type HeartbeatInfo struct {
	LastReport  time.Time `json:"last_report"`
	ReportCount int64     `json:"report_count"`
	ErrorCount  int64     `json:"error_count"`
	Interval    string    `json:"interval"`
	ServerIP    string    `json:"server_ip"`
	ServerPort  int       `json:"server_port"`
}

// Response 通用响应
type Response struct {
	Success   bool        `json:"success"`
	Message   string      `json:"message,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// CollectParams 采集参数
type CollectParams struct {
	Symbol    string                 `json:"symbol"`
	Interval  string                 `json:"interval,omitempty"`
	StartTime *time.Time             `json:"start_time,omitempty"`
	EndTime   *time.Time             `json:"end_time,omitempty"`
	Limit     int                    `json:"limit,omitempty"`
	Options   map[string]interface{} `json:"options,omitempty"`
}

// CollectResult 采集结果
type CollectResult struct {
	Data      interface{}            `json:"data"`
	Count     int                    `json:"count"`
	Timestamp time.Time              `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}
