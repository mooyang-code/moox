package model

import "time"

// EventAction identifies the one-shot work accepted by the crypto SCF.
type EventAction string

const (
	EventActionMarketFetch EventAction = "market_fetch"
	EventActionEgressProbe EventAction = "egress_probe"

	// EventSourceCollectorBackfill 标记 Collector 补采分块的同步调用。
	EventSourceCollectorBackfill = "collector_backfill"
)

// CloudFunctionEvent is the complete invocation contract for a short-lived SCF.
type CloudFunctionEvent struct {
	Action    EventAction            `json:"action,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp string                 `json:"timestamp"`
	// Tencent Timer events use capitalized fields. They remain separate from
	// the MooX one-shot envelope so the timer timestamp can make the batch ID
	// idempotent without a configuration request.
	Type        string `json:"Type,omitempty"`
	TriggerName string `json:"TriggerName,omitempty"`
	Time        string `json:"Time,omitempty"`
	Message     string `json:"Message,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Source      string `json:"source,omitempty"`
}

// Response 是云函数一次调用的返回：Collector 同步调用补采函数、探测出口 IP 时读它，定时触发的调用没有人读。
type Response struct {
	Success   bool        `json:"success"`
	Message   string      `json:"message,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}
