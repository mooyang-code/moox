// Package dispatch 把 EventBus 事件落成执行记录（接收），再由工作线程处理执行记录（通知、系统动作）。
package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// retryDelay 是写库失败时延迟重投的时间：写库失败不能丢事件，所以一直重投。
const retryDelay = 5 * time.Second

// Matches 报告规则的限定范围是否覆盖这条事件：同一字段的多个取值是“或”，不同字段之间是“且”，没有限定表示不限。
func Matches(rule store.Rule, entry catalog.Entry, message *eventpb.EventMessage) bool {
	for _, filter := range rule.Scope {
		value, ok := entry.ScopeValue(message, filter.Field)
		if !ok {
			return false
		}
		matched := false
		for _, want := range filter.Values {
			if want == value {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// Intake 接收 EventBus 事件：解码、匹配规则、在一个事务里写回执和执行记录，写库成功才确认消息。
type Intake struct {
	Store    *store.Store
	Registry *events.Registry
	Now      func() time.Time
}

// Outcome 是一条事件处理的结果，由事件消费层翻译成确认、重投或终止。
type Outcome int

const (
	// Accepted 表示事件已经处理（匹配到的规则已落库，或没有规则需要它）：可以确认。
	Accepted Outcome = iota
	// Rejected 表示事件本身无法处理（解码失败、不在目录里）：重投也没用，应当终止。
	Rejected
	// RetryLater 表示写库失败：不能丢事件，应当稍后重投。
	RetryLater
)

// RetryDelay 是写库失败时建议的重投延迟。
const RetryDelay = retryDelay

// Process 处理一条从 EventBus 取到的原始事件。
func (i *Intake) Process(ctx context.Context, raw []byte, subject, messageID, contentType string) (Outcome, error) {
	message, payload, err := events.DecodeRaw(i.Registry, raw, subject, messageID, contentType)
	if err != nil {
		// 解码失败重投也没用：终止并计数。
		telemetry.EventErrors.WithLabelValues("decode").Inc()
		log.WarnContextf(ctx, "事件解码失败，已终止: %v", err)
		return Rejected, err
	}
	entry, ok := catalog.Lookup(message.GetEventName())
	if !ok || entry.Event.Version() != message.GetEventVersion() {
		telemetry.EventErrors.WithLabelValues("not_subscribable").Inc()
		return Rejected, fmt.Errorf("事件 %s@%d 不在可订阅目录里", message.GetEventName(), message.GetEventVersion())
	}
	payloadJSON, err := PayloadJSON(payload)
	if err != nil {
		telemetry.EventErrors.WithLabelValues("payload_json").Inc()
		return Rejected, err
	}
	if err := i.accept(ctx, entry, message, payloadJSON); err != nil {
		log.WarnContextf(ctx, "事件写库失败，稍后重投: %v", err)
		return RetryLater, err
	}
	return Accepted, nil
}

func (i *Intake) accept(ctx context.Context, entry catalog.Entry, message *eventpb.EventMessage, payloadJSON string) error {
	rules, err := i.Store.ListEnabledRules(ctx, entry.Name())
	if err != nil {
		return err
	}
	var matched []store.Rule
	for _, rule := range rules {
		if Matches(rule, entry, message) {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	now := i.now().UnixMilli()
	return i.Store.InTx(ctx, func(tx *store.Tx) error {
		for _, rule := range matched {
			claimed, err := tx.ClaimReceipt(message.GetEventId(), rule.RuleID, now)
			if err != nil {
				return err
			}
			if !claimed {
				continue
			}
			if rule.ActionType == store.ActionSystem {
				if err := acceptSystem(tx, rule, message, payloadJSON, now); err != nil {
					return err
				}
				continue
			}
			if err := tx.InsertExecution(newExecution(rule, message, payloadJSON, "", now)); err != nil {
				return err
			}
		}
		return nil
	})
}

// acceptSystem 按（规则、标的）合并或新建：同一标的已经有未结束的执行记录时只更新它的事件内容，不新建。
func acceptSystem(tx *store.Tx, rule store.Rule, message *eventpb.EventMessage, payloadJSON string, now int64) error {
	key := rule.RuleID + "|" + message.GetSubjectId()
	existing, found, err := tx.FindUnfinishedByDedupe(key)
	if err != nil {
		return err
	}
	if !found {
		return tx.InsertExecution(newExecution(rule, message, payloadJSON, key, now))
	}
	if sameSchedule(existing.PayloadJSON, payloadJSON) {
		return nil
	}
	return tx.ReplaceExecutionEvent(existing.ExecutionID, payloadJSON, message.GetOccurredAt().AsTime().UnixMilli(), now)
}

// sameSchedule 报告两份下架事件内容的下架时间与范围是否相同：不同才需要让系统动作按新的内容重新安排。
func sameSchedule(previous, next string) bool {
	schedule := func(raw string) (string, string) {
		var payload struct {
			DelistAt string `json:"delist_at"`
			Scope    string `json:"scope"`
		}
		_ = json.Unmarshal([]byte(raw), &payload)
		return payload.DelistAt, payload.Scope
	}
	previousAt, previousScope := schedule(previous)
	nextAt, nextScope := schedule(next)
	return previousAt == nextAt && previousScope == nextScope
}

func newExecution(rule store.Rule, message *eventpb.EventMessage, payloadJSON, dedupeKey string, now int64) store.Execution {
	return store.Execution{
		ExecutionID: uuid.NewString(), RuleID: rule.RuleID, RuleName: rule.Name, ActionType: rule.ActionType,
		EventID: message.GetEventId(), EventName: message.GetEventName(), SpaceID: message.GetSpaceId(), SubjectID: message.GetSubjectId(),
		OccurredAt: message.GetOccurredAt().AsTime().UnixMilli(), PayloadJSON: payloadJSON,
		Status: store.StatusPending, DedupeKey: dedupeKey, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func (i *Intake) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

// PayloadJSON 把事件 payload 转成紧凑的 JSON：字段名沿用 proto 的蛇形命名，int64 按 protojson 的约定写成字符串。
func PayloadJSON(payload proto.Message) (string, error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("事件内容转 JSON 失败: %w", err)
	}
	// protojson 会随机插入空白，压紧后输出稳定。
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return "", err
	}
	return buffer.String(), nil
}
