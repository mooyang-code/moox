package model

import "time"

// EventAction identifies the one-shot work accepted by the crypto_market SCF.
type EventAction string

const (
	EventActionMarketFetch        EventAction = "market_fetch"
	EventActionEgressProbe        EventAction = "egress_probe"
	EventActionInstrumentSnapshot EventAction = "instrument_snapshot"
)

// CloudFunctionEvent is the complete invocation contract for a short-lived SCF.
type CloudFunctionEvent struct {
	Action    EventAction            `json:"action,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp string                 `json:"timestamp"`
	// Tencent Timer events use capitalized fields. They remain separate from
	// the MooX one-shot envelope so the timer timestamp can make the batch ID
	// idempotent without a configuration request.
	Type                    string `json:"Type,omitempty"`
	TriggerName             string `json:"TriggerName,omitempty"`
	Time                    string `json:"Time,omitempty"`
	Message                 string `json:"Message,omitempty"`
	RequestID               string `json:"request_id,omitempty"`
	Source                  string `json:"source,omitempty"`
	StorageRPCGatewayTarget string `json:"storage_rpc_gateway_target,omitempty"`
}

// Response is the function response returned to CloudNode.
type Response struct {
	Success   bool        `json:"success"`
	Message   string      `json:"message,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	RequestID string      `json:"request_id,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

// CollectParams describes one source collection request.
type CollectParams struct {
	Symbol    string                 `json:"symbol"`
	Interval  string                 `json:"interval,omitempty"`
	StartTime *time.Time             `json:"start_time,omitempty"`
	EndTime   *time.Time             `json:"end_time,omitempty"`
	Limit     int                    `json:"limit,omitempty"`
	Options   map[string]interface{} `json:"options,omitempty"`
}
