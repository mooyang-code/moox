package types

import collectmgrtypes "github.com/mooyang-code/moox/server/internal/service/collectmgr/types"
import "time"

// NodeStatus 节点状态
type NodeStatus int

const (
	NodeStatusOffline  NodeStatus = 0 // 离线
	NodeStatusOnline   NodeStatus = 1 // 在线
	NodeStatusTimeout  NodeStatus = 2 // 超时
	NodeStatusAbnormal NodeStatus = 3 // 异常
)

// NodeType 节点类型
const (
	NodeTypeSCFEvent  = "scf-event" // 云函数（事件型）
	NodeTypeSCFWeb    = "scf-web"   // 云函数（Web型）
	NodeTypeServer    = "server"    // 服务器
	NodeTypeContainer = "container" // 容器
	NodeTypeCustom    = "custom"    // 自定义
)

// ReportHeartbeatRequest 上报心跳请求
type ReportHeartbeatRequest struct {
	NodeID              string                 `json:"node_id" binding:"required"`
	NodeType            string                 `json:"node_type" binding:"required"`
	SourceService       string                 `json:"source_service"`
	Timestamp           *time.Time             `json:"timestamp"`
	Metrics             map[string]interface{} `json:"metrics"`
	Metadata            map[string]interface{} `json:"metadata"`
	SupportedCollectors []string               `json:"supported_collectors"`   // 支持的采集器数据类型
	TasksMD5            string                 `json:"tasks_md5"`              // 当前任务列表MD5值
	LocalDNSRecords     []*LocalDNSReportItem  `json:"local_dns_records,omitempty"` // 终端DNS解析结果（可选）
}

// LocalDNSReportItem 终端上报的DNS记录
type LocalDNSReportItem struct {
	Domain    string    `json:"domain" binding:"required"`
	IPList    []string  `json:"ip_list" binding:"required"`
	ResolveAt time.Time `json:"resolve_at" binding:"required"`
}

// ReportHeartbeatResponse 心跳上报响应
type ReportHeartbeatResponse struct {
	PackageVersion string              `json:"package_version"`  // 包版本信息
	TaskInstances  []*TaskInstanceInfo `json:"task_instances"`   // 任务实例列表
	TasksMD5       string              `json:"tasks_md5"`        // 服务端任务MD5值
}

// TaskInstanceInfo 任务实例信息（复用 collectmgr/types 定义）
type TaskInstanceInfo = collectmgrtypes.TaskInstanceLite
