package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

// presenceGate 让同一主机的上下线通知串行发送，并在每次发送（含内部重试）前复核主机当前状态：
// 已被更新状态取代的通知直接丢弃，保证不会出现“已恢复 → 失联”的错误顺序。
type presenceGate struct {
	status func(ctx context.Context, agentID string) (string, error)
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
}

func newPresenceGate(status func(ctx context.Context, agentID string) (string, error)) *presenceGate {
	return &presenceGate{status: status, locks: make(map[string]*sync.Mutex)}
}

func (g *presenceGate) lock(agentID string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	l := g.locks[agentID]
	if l == nil {
		l = &sync.Mutex{}
		g.locks[agentID] = l
	}
	return l
}

// superseded 返回该通知是否已被主机的当前状态取代；查询失败时保守地按“未取代”处理。
func (g *presenceGate) superseded(ctx context.Context, transition hostmetrics.PresenceTransition) bool {
	if g == nil || g.status == nil {
		return false
	}
	current, err := g.status(ctx, transition.AgentID)
	return err == nil && current != "" && current != transition.To
}

func hostPresenceTransitionSinkProviderWithFailure(provider func(context.Context) (notification.Sender, error), onFailure func(context.Context, hostmetrics.PresenceTransition, error)) hostmetrics.PresenceTransitionFunc {
	return hostPresenceTransitionSinkWithGate(provider, onFailure, nil)
}

func hostPresenceTransitionSinkWithGate(provider func(context.Context) (notification.Sender, error), onFailure func(context.Context, hostmetrics.PresenceTransition, error), gate *presenceGate) hostmetrics.PresenceTransitionFunc {
	return func(ctx context.Context, transition hostmetrics.PresenceTransition) error {
		if gate != nil {
			l := gate.lock(transition.AgentID)
			l.Lock()
			defer l.Unlock()
		}
		log.InfoContextf(ctx, "host presence transition agent_id=%s from=%s to=%s observed_at=%s",
			transition.AgentID, transition.From, transition.To, transition.ObservedAt.Format(time.RFC3339Nano))
		if provider == nil {
			return nil
		}
		host := strings.TrimSpace(transition.Hostname)
		if host == "" {
			host = transition.AgentID
		}
		severity := notification.SeverityCritical
		title := "主机 " + host + " · 失联"
		body := "状态：新告警\n问题：" + presenceLostReason
		if transition.To == hostmetrics.PresenceReachable {
			severity = notification.SeverityInfo
			title = "主机 " + host + " · 已恢复在线"
			body = "状态：已恢复\n当前：" + presenceBackReason
		}
		message := notification.Message{
			Key:      "host-presence:" + transition.AgentID,
			Severity: severity,
			Title:    title,
			Body:     body + "\n时间：" + alerttext.Time(transition.ObservedAt) + "（北京时间）",
			Labels: map[string]string{
				"agent_id": transition.AgentID,
				"from":     string(transition.From),
				"to":       string(transition.To),
			},
		}
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if gate.superseded(ctx, transition) {
				log.InfoContextf(ctx, "host presence notification superseded agent_id=%s to=%s", transition.AgentID, transition.To)
				return nil
			}
			sender, err := provider(ctx)
			if err == nil && sender == nil {
				return nil // notifications are optional until a global channel is configured
			}
			if err == nil {
				err = sender.Send(ctx, message)
			}
			if err == nil {
				return nil
			}
			if notifier.IsPartial(err) {
				// 已有渠道送达：记录失败的渠道，不再重发，免得已收到的群重复收到。
				log.WarnContextf(ctx, "host presence notification partially failed agent_id=%s: %v", transition.AgentID, err)
				if onFailure != nil {
					onFailure(ctx, transition, err)
				}
				return nil
			}
			lastErr = err
			if attempt == 2 {
				break
			}
			delay := time.Duration(attempt+1) * time.Second
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				lastErr = ctx.Err()
				attempt = 2
			case <-timer.C:
			}
		}
		if lastErr != nil {
			log.ErrorContextf(ctx, "host presence notification failed agent_id=%s: %v", transition.AgentID, lastErr)
			if onFailure != nil {
				onFailure(ctx, transition, lastErr)
			}
		}
		return lastErr
	}
}

const (
	presenceLostReason = "主机采集代理停止上报，主机可能宕机、断网，或采集代理已停止"
	presenceBackReason = "主机重新开始上报"
)

// recordHostPresence 把主机上下线写进告警事件表，概览的告警经过里才能看到主机失联。
// 事件 ID 由主机、目标状态和观察时间决定，同一次转换重复投递只记一条；记录失败不影响通知。
func recordHostPresence(alerts *store.AlertRepository, next hostmetrics.PresenceTransitionFunc) hostmetrics.PresenceTransitionFunc {
	return func(ctx context.Context, transition hostmetrics.PresenceTransition) error {
		if alerts != nil {
			if err := alerts.CreateEventIdempotent(ctx, presenceEvent(transition)); err != nil {
				log.ErrorContextf(ctx, "record host presence event agent_id=%s: %v", transition.AgentID, err)
			}
		}
		return next(ctx, transition)
	}
}

func presenceEvent(transition hostmetrics.PresenceTransition) *domain.AlertEvent {
	eventType, status, reason := domain.AlertEventTriggered, domain.AlertStatusFiring, presenceLostReason
	if transition.To == hostmetrics.PresenceReachable {
		eventType, status, reason = domain.AlertEventResolved, domain.AlertStatusResolved, presenceBackReason
	}
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	observed := transition.ObservedAt.UTC()
	sum := sha256.Sum256([]byte(transition.AgentID + "\x00" + transition.To + "\x00" + observed.Format(time.RFC3339Nano)))
	return &domain.AlertEvent{
		EventID: "host-presence-" + hex.EncodeToString(sum[:16]), SpaceID: hostmetrics.SpaceID,
		RuleID: hostmetrics.PresenceRuleID, CheckID: hostmetrics.PresenceCheckID(transition.AgentID),
		EventType: eventType, Status: status, Message: reason, Payload: string(payload), CreatedAt: observed,
	}
}
