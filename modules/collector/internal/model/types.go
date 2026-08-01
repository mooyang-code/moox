package model

import "time"

// CollectorType 采集器类型
type CollectorType string

const (
	CollectorTypeBinance CollectorType = "binance"
	NodeTypeSCFEvent                   = "scf-event"
)

// EventAction 事件类型
type EventAction string

const (
	// EventActionMarketFetch runs one bounded market-fetch batch and exits.
	EventActionMarketFetch EventAction = "market_fetch"
	// EventActionEgressProbe validates that this SCF instance can reach the
	// configured public network and Binance endpoint before it is scheduled.
	EventActionEgressProbe EventAction = "egress_probe"
)

// TaskSummary 任务摘要（用于心跳上报）
type TaskSummary struct {
	ID      string     `json:"id"`
	Type    string     `json:"type"`
	Status  string     `json:"status"`
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
	SpaceID             string                 `json:"space_id"`
	NodeID              string                 `json:"node_id"`
	NodeType            string                 `json:"node_type"`
	RunningVersion      string                 `json:"running_version"`
	Timestamp           time.Time              `json:"timestamp"`
	RunningTasks        []*TaskSummary         `json:"running_tasks"`
	Metrics             *NodeMetrics           `json:"metrics"`
	Metadata            map[string]interface{} `json:"metadata,omitempty"`
	SupportedCollectors []string               `json:"supported_collectors,omitempty"` // 支持的采集器数据类型
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
	Action                  EventAction                  `json:"action,omitempty"`
	Data                    map[string]interface{}       `json:"data,omitempty"`
	Timestamp               string                       `json:"timestamp"` // 使用时间格式字符串（支持时区）
	RequestID               string                       `json:"request_id,omitempty"`
	Source                  string                       `json:"source,omitempty"`                 // 探测来源标识
	ServiceGatewayTarget    string                       `json:"service_gateway_target,omitempty"` // /api/service gateway target
	StorageRPCGatewayTarget string                       `json:"storage_rpc_gateway_target,omitempty"`
	ServiceDeployments      map[string]ServiceDeployment `json:"service_deployments,omitempty"` // 控制面下发的 active 服务部署信息
}

// ServiceDeployment 是控制面 keepalive 下发给 SCF runtime 的服务部署信息。
type ServiceDeployment struct {
	ServiceName string `json:"service_name,omitempty"`
	ServiceKind string `json:"service_kind,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
	RPCAddress  string `json:"rpc_address,omitempty"`
	GatewayPath string `json:"gateway_path,omitempty"`
	Scope       string `json:"scope,omitempty"`
	Status      string `json:"status,omitempty"`
}

// TaskExecuteEvent 任务立即执行事件（服务端触发）
type TaskExecuteEvent struct {
	SpaceID       string   `json:"space_id"`
	DatasetID     string   `json:"dataset_id"`
	TaskID        string   `json:"task_id"`
	JobItemID     string   `json:"job_item_id"`
	DeliveryCount uint64   `json:"delivery_count"`
	MaxDeliver    int      `json:"max_deliver"`
	DataType      string   `json:"data_type"`
	DataSource    string   `json:"data_source"`
	Market        string   `json:"market,omitempty"`
	InstType      string   `json:"inst_type"`
	SubjectID     string   `json:"subject_id,omitempty"`
	Symbol        string   `json:"symbol"`
	Intervals     []string `json:"intervals"`
	Live          bool     `json:"live,omitempty"`
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
	LastReport           time.Time `json:"last_report"`
	ReportCount          int64     `json:"report_count"`
	ErrorCount           int64     `json:"error_count"`
	Interval             string    `json:"interval"`
	ServiceGatewayTarget string    `json:"service_gateway_target,omitempty"`
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
