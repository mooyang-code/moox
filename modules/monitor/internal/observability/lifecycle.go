package observability

import (
	"encoding/json"
	"strings"
)

// LifecycleStatus 是模块随健康检查响应（details.lifecycle）上报的模块级生命周期状态（设计 3.8），
// 字段与 packages/lifecycle 的 State 一致；Monitor 只读取，不依赖该包。
type LifecycleStatus struct {
	// Stage 是 running | paused | resuming。
	Stage   string            `json:"stage"`
	Reason  string            `json:"reason"`
	Pause   *LifecyclePause   `json:"pause"`
	Catchup *LifecycleCatchup `json:"catchup"`
}

// LifecyclePause 是暂停详情：类型（manual | system）、原因、操作者、开始时间。
type LifecyclePause struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	By     string `json:"by"`
	Since  string `json:"since"`
}

// LifecycleCatchup 是恢复追赶进度。
type LifecycleCatchup struct {
	Since      string `json:"since"`
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	CaughtUp   bool   `json:"caught_up"`
	BlockedBy  string `json:"blocked_by"`
	Unfillable int    `json:"unfillable"`
	Message    string `json:"message"`
}

// ParseLifecycle 从健康检查响应体里取出 details.lifecycle；响应没有它（模块没有生命周期接口）、
// 响应体被截断或格式不符时返回 nil。
func ParseLifecycle(body string) *LifecycleStatus {
	body = strings.TrimSpace(body)
	if body == "" || !strings.Contains(body, `"lifecycle"`) {
		return nil
	}
	var payload struct {
		Details struct {
			Lifecycle *LifecycleStatus `json:"lifecycle"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}
	if payload.Details.Lifecycle == nil || payload.Details.Lifecycle.Stage == "" {
		return nil
	}
	return payload.Details.Lifecycle
}
