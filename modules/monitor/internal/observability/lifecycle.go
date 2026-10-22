package observability

import (
	"encoding/json"
	"strings"
)

// LifecycleStatus 是模块随健康检查响应（details.lifecycle）上报的生命周期状态（设计 6.1），
// 字段与 packages/lifecycle 的 Compact 一致；Monitor 只读取，不依赖该包。
type LifecycleStatus struct {
	Stage      string                `json:"stage"`
	Color      string                `json:"color"`
	Degraded   bool                  `json:"degraded"`
	Reason     string                `json:"reason"`
	Self       string                `json:"self"`
	Pause      *LifecyclePause       `json:"pause"`
	Hold       string                `json:"hold"`
	Catchup    *LifecycleCatchup     `json:"catchup"`
	Counts     map[string]int        `json:"counts"`
	PauseKinds map[string]int        `json:"pause_kinds"`
	Attention  []LifecycleObjectView `json:"attention"`
	Recovered  int                   `json:"recovered"`
}

// LifecyclePause 是暂停详情：类型（manual | system | auto）、原因、操作者、开始时间。
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

// LifecycleObjectView 是需要关注的一个对象。
type LifecycleObjectView struct {
	Kind      string            `json:"kind"`
	ID        string            `json:"id"`
	Stage     string            `json:"stage"`
	Effective string            `json:"effective"`
	Color     string            `json:"color"`
	Reason    string            `json:"reason"`
	Pause     *LifecyclePause   `json:"pause"`
	Catchup   *LifecycleCatchup `json:"catchup"`
	Checks    []LifecycleCheck  `json:"checks"`
}

// LifecycleCheck 是一项失败的检查。
type LifecycleCheck struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Remedy  string `json:"remedy"`
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
