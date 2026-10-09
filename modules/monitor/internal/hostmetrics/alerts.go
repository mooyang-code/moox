package hostmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/notification"
	"gorm.io/gorm"
)

// AlertEvaluator computes host threshold transitions after Storage accepts a sample.
// Alert failures never participate in the EventBus ACK decision.
type AlertEvaluator struct {
	Cache        *RuleCache
	Repository   *store.AlertRepository
	Now          func() time.Time
	Notification func(context.Context) (*domain.NotificationChannel, error)
	mu           sync.Mutex
	seen         map[string]struct{}
}

func (e *AlertEvaluator) Evaluate(ctx context.Context, agentID, hostname, messageID string, snapshot *hostmetricpb.HostSnapshot, observedAt time.Time) error {
	if e == nil || e.Cache == nil || e.Repository == nil || snapshot == nil {
		return nil
	}
	now := observedAt.UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	var firstErr error
	for metric, value := range hostValues(snapshot) {
		for _, rule := range e.Cache.Rules(agentID, metric) {
			if e.isSeen(messageID, rule.RuleID) {
				continue
			}
			threshold, recovery := hostThresholds(rule, metric)
			if !value.available {
				continue
			}
			sample := hostSample{agentID: agentID, hostname: hostname, metric: metric, value: value.value, threshold: threshold}
			if err := e.transition(ctx, rule, sample, messageID, value.value >= threshold, recovery, now); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
			e.remember(messageID, rule.RuleID)
		}
	}
	return firstErr
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

// hostSample is one host metric value as an alert describes it.
type hostSample struct {
	agentID, hostname, metric string
	value, threshold          float64
}

func (e *AlertEvaluator) transition(ctx context.Context, rule domain.AlertRule, sample hostSample, messageID string, failing bool, recovery float64, now time.Time) error {
	state, err := e.Repository.GetState(ctx, SpaceID, rule.RuleID, rule.CheckID)
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if state == nil {
		state = &domain.AlertState{SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, Status: domain.AlertStatusOK, DedupeKey: rule.RuleID + ":" + rule.CheckID}
	} else if strings.TrimSpace(state.DedupeKey) == "" {
		state.DedupeKey = rule.RuleID + ":" + rule.CheckID
	}
	if failing {
		state.FailureCount++
		state.SuccessCount = 0
		if state.Status != domain.AlertStatusFiring && state.FailureCount >= positive(rule.FailureThreshold) {
			state.Status = domain.AlertStatusFiring
			state.TriggeredAt = &now
			state.ResolvedAt = nil
			state.LastReminderAt = &now
			return e.record(ctx, rule, sample, messageID, state, domain.AlertEventTriggered, now, true)
		} else if state.Status == domain.AlertStatusFiring &&
			reminderDue(state.LastReminderAt, now, rule.MinimumReminderIntervalSeconds) {
			state.LastReminderAt = &now
			return e.record(ctx, rule, sample, messageID, state, domain.AlertEventReminder, now, true)
		}
	} else {
		state.SuccessCount++
		state.FailureCount = 0
		if state.Status == domain.AlertStatusFiring && state.SuccessCount >= positive(rule.SuccessThreshold) && sample.value <= recovery {
			state.Status = domain.AlertStatusResolved
			state.ResolvedAt = &now
			if err := e.record(ctx, rule, sample, messageID, state, domain.AlertEventResolved, now, false); err != nil {
				state.Status = domain.AlertStatusFiring
				state.ResolvedAt = nil
				state.SuccessCount = 0
				state.LastReminderAt = nil
				return err
			}
			return e.Repository.UpsertState(ctx, state)
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

func (e *AlertEvaluator) record(ctx context.Context, rule domain.AlertRule, sample hostSample, messageID string, state *domain.AlertState, eventType string, now time.Time, persistBeforeSend bool) error {
	agentID := sample.agentID
	payload, _ := json.Marshal(map[string]any{"agent_id": agentID, "value": sample.value, "metric": strings.TrimPrefix(rule.CheckID, HostRulePrefix)})
	if err := e.Repository.CreateEventIdempotent(ctx, &domain.AlertEvent{EventID: deterministicEventID(messageID, rule.RuleID, eventType), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: eventType, Status: state.Status, Payload: string(payload), CreatedAt: now}); err != nil {
		return err
	}
	if persistBeforeSend {
		if err := e.Repository.UpsertState(ctx, state); err != nil {
			return err
		}
	}
	title, message := hostAlertText(sample, eventType, now)
	if e.Notification != nil {
		channel, err := e.Notification(ctx)
		if err != nil {
			if persistBeforeSend {
				state.LastReminderAt = nil
				_ = e.Repository.UpsertState(ctx, state)
			}
			_ = e.Repository.CreateEvent(ctx, &domain.AlertEvent{EventID: uuid.NewString(), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: domain.AlertEventSendFailed, Status: state.Status, Message: err.Error(), CreatedAt: now})
			return err
		}
		if channel == nil || strings.TrimSpace(channel.WebhookURL) == "" {
			return nil
		}
		sender, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channel.ChannelType), WebhookURL: channel.WebhookURL})
		if err != nil {
			if persistBeforeSend {
				state.LastReminderAt = nil
				_ = e.Repository.UpsertState(ctx, state)
			}
			_ = e.Repository.CreateEvent(ctx, &domain.AlertEvent{EventID: uuid.NewString(), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: domain.AlertEventSendFailed, Status: state.Status, Message: err.Error(), CreatedAt: now})
			return err
		}
		severity := notification.SeverityCritical
		if eventType == domain.AlertEventResolved {
			severity = notification.SeverityInfo
		} else if eventType == domain.AlertEventReminder {
			severity = notification.SeverityWarning
		}
		if err := sender.Send(ctx, notification.Message{Key: rule.RuleID + ":" + rule.CheckID, Severity: severity, Title: title, Body: message}); err != nil {
			if persistBeforeSend {
				state.LastReminderAt = nil
				_ = e.Repository.UpsertState(ctx, state)
			}
			_ = e.Repository.CreateEvent(ctx, &domain.AlertEvent{EventID: uuid.NewString(), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: domain.AlertEventSendFailed, Status: state.Status, Message: err.Error(), CreatedAt: now})
			return err
		}
		return nil
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

var hostMetricLabels = map[string]string{
	HostMetricCPU:             "CPU 使用率",
	HostMetricMemory:          "内存使用率",
	HostMetricFilesystemUsage: "磁盘空间使用率",
	HostMetricDiskUtilization: "磁盘繁忙度",
	HostMetricNetworkErrors:   "网络错误包",
}

// MetricLabel 返回主机指标的中文名称，例如「CPU 使用率」。
func MetricLabel(metric string) string {
	if label := hostMetricLabels[metric]; label != "" {
		return label
	}
	return metric
}

// formatHostValue 渲染主机指标的取值：网络错误包为每秒个数，其余为百分比。
func formatHostValue(metric string, value float64) string {
	if metric == HostMetricNetworkErrors {
		return fmt.Sprintf("每秒 %.1f 个", value)
	}
	return fmt.Sprintf("%.1f%%", value)
}

// HostAlertReason 是主机阈值告警的中文原因，例如「CPU 使用率当前 93.2%，告警阈值 90.0%」；快照里没有这个指标时只给出阈值。
func HostAlertReason(rule domain.AlertRule, metric string, snapshot *hostmetricpb.HostSnapshot) string {
	threshold, _ := hostThresholds(rule, metric)
	label := MetricLabel(metric)
	if snapshot != nil {
		if current, ok := hostValues(snapshot)[metric]; ok && current.available {
			return fmt.Sprintf("%s当前 %s，告警阈值 %s", label, formatHostValue(metric, current.value), formatHostValue(metric, threshold))
		}
	}
	return fmt.Sprintf("%s超过告警阈值 %s", label, formatHostValue(metric, threshold))
}

// hostAlertText renders a host threshold alert for people: the host and
// metric as the title, the value against its threshold in the body.
func hostAlertText(sample hostSample, eventType string, now time.Time) (string, string) {
	host := strings.TrimSpace(sample.hostname)
	if host == "" {
		host = sample.agentID
	}
	label := MetricLabel(sample.metric)
	value := func(v float64) string { return formatHostValue(sample.metric, v) }
	var lines []string
	switch eventType {
	case domain.AlertEventResolved:
		lines = append(lines, "状态：已恢复", fmt.Sprintf("当前：%s %s", label, value(sample.value)))
	case domain.AlertEventReminder:
		lines = append(lines, "状态：仍未恢复", fmt.Sprintf("问题：%s %s，超过阈值 %s", label, value(sample.value), value(sample.threshold)))
	default:
		lines = append(lines, "状态：新告警", fmt.Sprintf("问题：%s %s，超过阈值 %s", label, value(sample.value), value(sample.threshold)))
	}
	lines = append(lines, "时间："+alerttext.Time(now)+"（北京时间）")
	return "主机 " + host + " · " + label, strings.Join(lines, "\n")
}
