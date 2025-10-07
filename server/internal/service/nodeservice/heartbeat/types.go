package heartbeat

import "time"

// NodeStatus 节点状态
type NodeStatus int

const (
	NodeStatusOnline  NodeStatus = 1
	NodeStatusOffline NodeStatus = 0
)

// HeartbeatData 心跳数据
type HeartbeatData struct {
	NodeID       string            `json:"node_id"`
	Timestamp    time.Time         `json:"timestamp"`
	Status       string            `json:"status"` // running/idle
	RunningTasks []RunningTaskInfo `json:"running_tasks"`
}

// RunningTaskInfo 运行中任务信息
type RunningTaskInfo struct {
	TaskID        string    `json:"task_id"`
	CollectorType string    `json:"collector_type"`
	Source        string    `json:"source"`
	StartTime     time.Time `json:"start_time"`
	LastExecTime  time.Time `json:"last_exec_time"`
	ExecCount     int64     `json:"exec_count"`
	ErrorCount    int64     `json:"error_count"`
}

// HeartbeatResponse 心跳响应
type HeartbeatResponse struct {
	Success   bool      `json:"success"`
	Timestamp time.Time `json:"timestamp"`
}

// NodeState 节点状态信息
type NodeState struct {
	NodeID        string
	LastHeartbeat time.Time
	Status        NodeStatus
	RunningTasks  int
}

// InitRequest 初始化请求
type InitRequest struct {
	NodeID  string `json:"node_id"`
	MooxURL string `json:"moox_url"`
}