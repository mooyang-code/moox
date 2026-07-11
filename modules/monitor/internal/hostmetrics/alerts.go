package hostmetrics

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/repository"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"gorm.io/gorm"
)

// AlertEvaluator computes host threshold transitions after Storage accepts a sample.
// Alert failures never participate in the EventBus ACK decision.
type AlertEvaluator struct {
	Cache      *RuleCache
	Repository *repository.AlertRepository
	InstanceID string
	Now        func() time.Time
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
			if err := e.transition(ctx, rule, agentID, value.value >= threshold, recovery, now, value.value); err != nil {
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
		if network.GetRateAvailable() {
			current := values[HostMetricNetworkErrors]
			values[HostMetricNetworkErrors] = hostValue{current.value + float64(network.GetReceiveErrorsTotal()) + float64(network.GetTransmitErrorsTotal()), true}
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

func (e *AlertEvaluator) transition(ctx context.Context, rule domain.AlertRule, agentID string, failing bool, recovery float64, now time.Time, value float64) error {
	state, err := e.Repository.GetState(ctx, SpaceID, rule.RuleID, rule.CheckID)
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if state == nil {
		state = &domain.AlertState{SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, Status: domain.AlertStatusOK, OwnerInstanceID: e.InstanceID}
	}
	if failing {
		state.FailureCount++
		state.SuccessCount = 0
		if state.Status != domain.AlertStatusFiring && state.FailureCount >= positive(rule.FailureThreshold) {
			state.Status = domain.AlertStatusFiring
			state.TriggeredAt = &now
			return e.record(ctx, rule, agentID, state, value, domain.AlertEventTriggered, now)
		}
	} else {
		state.SuccessCount++
		state.FailureCount = 0
		if state.Status == domain.AlertStatusFiring && state.SuccessCount >= positive(rule.SuccessThreshold) && value <= recovery {
			state.Status = domain.AlertStatusResolved
			state.ResolvedAt = &now
			if err := e.record(ctx, rule, agentID, state, value, domain.AlertEventResolved, now); err != nil {
				return err
			}
		}
	}
	return e.Repository.UpsertState(ctx, state)
}

func (e *AlertEvaluator) record(ctx context.Context, rule domain.AlertRule, agentID string, state *domain.AlertState, value float64, eventType string, now time.Time) error {
	if err := e.Repository.UpsertState(ctx, state); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"agent_id": agentID, "value": value, "metric": strings.TrimPrefix(rule.CheckID, HostRulePrefix)})
	return e.Repository.CreateEvent(ctx, &domain.AlertEvent{EventID: uuid.NewString(), SpaceID: SpaceID, RuleID: rule.RuleID, CheckID: rule.CheckID, EventType: eventType, Status: state.Status, OwnerInstanceID: e.InstanceID, Payload: string(payload), CreatedAt: now})
}

func positive(value int) int {
	if value > 0 {
		return value
	}
	return 1
}
