package trigger

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TargetJSON 是 targets_json 中一个元素的结构。
type TargetJSON struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
}

// EncodeTargets 把目标权重编码为 targets_json。
func EncodeTargets(targets []engine.Target) ([]TargetJSON, json.RawMessage, error) {
	items := make([]TargetJSON, 0, len(targets))
	for _, target := range targets {
		items = append(items, TargetJSON{InstrumentID: target.InstrumentID, TargetWeight: target.Weight.String()})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, nil, err
	}
	return items, raw, nil
}

// DecodeTargets 解析 targets_json。
func DecodeTargets(raw json.RawMessage) ([]TargetJSON, error) {
	if len(raw) == 0 {
		return []TargetJSON{}, nil
	}
	var items []TargetJSON
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []TargetJSON{}
	}
	return items, nil
}

// EncodeSummary 编码 summary_json：装配说明在前、引擎说明在后，合并到同一个 notes 列表。
func EncodeSummary(summary engine.Summary, notes []string) (json.RawMessage, error) {
	if len(notes) > 0 {
		summary.Notes = append(append([]string(nil), notes...), summary.Notes...)
	}
	return json.Marshal(summary)
}

// ResultItems 把引擎解释转为存储行。
func ResultItems(items []engine.Item) []store.ResultItem {
	out := make([]store.ResultItem, 0, len(items))
	for _, item := range items {
		row := store.ResultItem{RuleID: item.RuleID, InstrumentID: item.InstrumentID, Stage: item.Stage, Reason: item.Reason}
		if item.Score != "" {
			if score, err := strconv.ParseFloat(item.Score, 64); err == nil {
				row.Score = &score
			}
		}
		if item.Rank > 0 {
			rank := item.Rank
			row.Rank = &rank
		}
		if item.Weight != "" {
			weight := item.Weight
			row.Weight = &weight
		}
		out = append(out, row)
	}
	return out
}

// MarshalTargetEvent 构造发给 Trade 的目标权重事件（事件契约不变）。
func MarshalTargetEvent(instance store.Instance, result store.Result, targets []TargetJSON) ([]byte, error) {
	if instance.LogicalAccountID == nil {
		return nil, fmt.Errorf("观察实例没有组合账户，不能构造目标事件")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payloadTargets := make([]*tradeeventpb.InstrumentWeightTarget, 0, len(targets))
	for _, target := range targets {
		payloadTargets = append(payloadTargets, &tradeeventpb.InstrumentWeightTarget{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight})
	}
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: result.ResultID, InstanceId: result.InstanceID, LogicalAccountId: *instance.LogicalAccountID,
		SessionId: result.SessionID, StrategyId: instance.StrategyID,
		BarEndTime: timestamppb.New(result.BarEndTime), EffectiveAt: timestamppb.New(result.BarEndTime), ValidUntil: timestamppb.New(result.ValidUntil),
		Targets: payloadTargets,
	}
	return registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, payload, events.PublishOptions{EventID: result.ResultID, OccurredAt: result.CreatedAt, SpaceID: instance.SpaceID, SubjectID: *instance.LogicalAccountID})
}
