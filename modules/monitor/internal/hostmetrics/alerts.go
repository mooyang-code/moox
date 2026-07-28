package hostmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"gorm.io/gorm"
)

// AlertEvaluator computes host threshold transitions after Storage accepts a sample.
// Alert failures never participate in the EventBus ACK decision.
type AlertEvaluator struct {
	Cache      *RuleCache
	Repository *store.AlertRepository
	Now        func() time.Time
	Webhook    func(context.Context, string, string) (*domain.WebhookChannel, error)
	Notifier   alerting.Notifier
	mu         sync.Mutex
	seen       map[string]struct{}
}

func (e *AlertEvaluator) Evaluate(ctx context.Context, agentID, messageID string, snapshot *hostmetricpb.HostSnapshot, observedAt time.Time) error {
	if e == nil || e.Cache == nil || e.Repository == nil || snapshot == nil {
		return nil
	}
	now := observedAt.UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	for metric, value := range hostValues(snapshot) {
		for _, rule := range e.Cache.Rules(agentID, metric) {
			if e.isSeen(messageID, rule.RuleID) {
				continue
			}
			threshold, recovery := hostThresholds(rule, metric)
			if !value.available {
				continue
			}
			if err := e.transition(ctx, rule, agentID, messageID, value.value >= threshold, recovery, now, value.value); err != nil {
				return err
			}
			e.remember(messageID, rule.RuleID)
		}
	}
	return nil
}

func (e *AlertEvaluator) isSeen(messageID, ruleID string) bool {
	if messageID == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := messageID + "\x00" + ruleID
	_, ok := e.seen[key]
	return ok
}

func (e *AlertEvaluator) remember(messageID, ruleID string) {
	if messageID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.seen == nil {
		e.seen = make(map[string]struct{})
	}
	if len(e.seen) >= 100000 {
		e.seen = make(map[string]struct{})
	}
	e.seen[messageID+"\x00"+ruleID] = struct{}{}
}

type hostValue struct {
	value     float64
	available bool
}

func hostValues(s *hostmetricpb.HostSnapshot) map[string]hostValue {
	values := map[string]hostValue{}
	if cpu := s.GetCpu(); cpu != nil {
		values[HostMetricCPU] = hostValue{cpu.GetUsagePercent(), cpu.GetUsageAvailable()}
	}
	if memory := s.GetMemory(); memory != nil {
		values[HostMetricMemory] = hostValue{memory.GetUsagePercent(), true}
	}
	for _, fs := range s.GetFilesystems() {
		current := values[HostMetricFilesystemUsage]
		if !current.available || fs.GetUsagePercent() > current.value {
			values[HostMetricFilesystemUsage] = hostValue{fs.GetUsagePercent(), true}
		}
	}
	for _, disk := range s.GetDisks() {
		if disk.GetRateAvailable() {
			current := values[HostMetricDiskUtilization]
			if !current.available || disk.GetUtilizationPercent() > current.value {
				values[HostMetricDiskUtilization] = hostValue{disk.GetUtilizationPercent(), true}
			}
		}
	}
	for _, network := range s.GetNetworks() {
		if network.GetErrorRateAvailable() {
			current := values[HostMetricNetworkErrors]
			values[HostMetricNetworkErrors] = hostValue{
				current.value + network.GetReceiveErrorsPerSecond() + network.GetTransmitErrorsPerSecond(),
				true,
			}
		}
	}
	return values
}

func hostThresholds(rule domain.AlertRule, metric string) (float64, float64) {
	threshold := 80.0
	if metric == HostMetricNetworkErrors {
		threshold = 1
	}
	recovery := threshold
	var definition struct {
		Threshold         float64 `json:"threshold"`
		RecoveryThreshold float64 `json:"recovery_threshold"`
	}
	if json.Unmarshal([]byte(rule.Description), &definition) == nil {
		if definition.Threshold > 0 {
			threshold = definition.Threshold
		}
		if definition.RecoveryThreshold > 0 {
			recovery = definition.RecoveryThreshold
		}
	}
	return threshold, recovery
}

func (e *AlertEvaluator) transition(ctx context.Context, rule domain.AlertRule, agentID, messageID string, failing bool, recovery float64, now time.Time, value float64) error {
	state, err := e.Repository.GetState(ctx, SpaceID, rule.RuleID, rule.CheckID)
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if state == nil {
		state = &domain.AlertState{SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, Status: domain.AlertStatusOK}
	}
	if failing {
		state.FailureCount++
		state.SuccessCount = 0
		if state.Status != domain.AlertStatusFiring && state.FailureCount >= positive(rule.FailureThreshold) {
			state.Status = domain.AlertStatusFiring
			state.TriggeredAt = &now
			state.ResolvedAt = nil
			state.LastReminderAt = &now
			return e.record(ctx, rule, agentID, messageID, state, value, domain.AlertEventTriggered, now)
		} else if state.Status == domain.AlertStatusFiring &&
			reminderDue(state.LastReminderAt, now, rule.MinimumReminderIntervalSeconds) {
			state.LastReminderAt = &now
			return e.record(ctx, rule, agentID, messageID, state, value, domain.AlertEventReminder, now)
		}
	} else {
		state.SuccessCount++
		state.FailureCount = 0
		if state.Status == domain.AlertStatusFiring && state.SuccessCount >= positive(rule.SuccessThreshold) && value <= recovery {
			state.Status = domain.AlertStatusResolved
			state.ResolvedAt = &now
			if err := e.record(ctx, rule, agentID, messageID, state, value, domain.AlertEventResolved, now); err != nil {
				return err
			}
		}
	}
	return e.Repository.UpsertState(ctx, state)
}

func reminderDue(last *time.Time, now time.Time, intervalSeconds int) bool {
	if intervalSeconds <= 0 {
		return false
	}
	if last == nil {
		return true
	}
	return now.Sub(*last) >= time.Duration(intervalSeconds)*time.Second
}

func (e *AlertEvaluator) record(ctx context.Context, rule domain.AlertRule, agentID, messageID string, state *domain.AlertState, value float64, eventType string, now time.Time) error {
	payload, _ := json.Marshal(map[string]any{"agent_id": agentID, "value": value, "metric": strings.TrimPrefix(rule.CheckID, HostRulePrefix)})
	if err := e.Repository.CreateEventIdempotent(ctx, &domain.AlertEvent{EventID: deterministicEventID(messageID, rule.RuleID, eventType), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: eventType, Status: state.Status, Payload: string(payload), CreatedAt: now}); err != nil {
		return err
	}
	if err := e.Repository.UpsertState(ctx, state); err != nil {
		return err
	}
	if e.Webhook == nil || e.Notifier == nil || rule.WebhookID == "" {
		return nil
	}
	webhook, err := e.Webhook(ctx, SpaceID, rule.WebhookID)
	if err != nil || webhook == nil {
		return nil
	}
	event := alerting.Event{EventID: uuid.NewString(), EventType: eventType, Status: state.Status, Message: fmt.Sprintf("%s host metric %s=%v", agentID, strings.TrimPrefix(rule.CheckID, HostRulePrefix), value), Check: domain.Check{SpaceID: SpaceID, CheckID: rule.CheckID, Name: "Host metric"}, Result: domain.CheckResult{SpaceID: SpaceID, CheckID: rule.CheckID, Success: state.Status != domain.AlertStatusFiring, ErrorMessage: fmt.Sprintf("value=%v", value), CheckedAt: now}, Rule: rule, DedupeKey: rule.RuleID + ":" + rule.CheckID}
	if err := e.Notifier.Send(ctx, *webhook, event); err != nil {
		// Notification failure is recorded best-effort and never rolls back the sample.
		_ = e.Repository.CreateEvent(ctx, &domain.AlertEvent{EventID: uuid.NewString(), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: domain.AlertEventSendFailed, Status: state.Status, Message: err.Error(), CreatedAt: now})
	}
	return nil
}

func deterministicEventID(messageID, ruleID, eventType string) string {
	sum := sha256.Sum256([]byte(messageID + "\x00" + ruleID + "\x00" + eventType))
	return "host-alert-" + hex.EncodeToString(sum[:16])
}

func positive(value int) int {
	if value > 0 {
		return value
	}
	return 1
}
