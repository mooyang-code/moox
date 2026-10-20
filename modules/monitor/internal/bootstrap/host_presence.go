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
//
// 门也负责区分失联原因：HostAgent 停止上报而该主机的网关心跳仍正常时，主机本身在线，只是采集代理停了，
// 告警标题改为“host-agent 停止上报”（警告），恢复时对应“host-agent 已恢复上报”。
type presenceGate struct {
	status func(ctx context.Context, agentID string) (string, error)
	// gatewayAlive 报告主机网关在给定时刻是否仍有心跳；为空时不区分原因。
	gatewayAlive func(ctx context.Context, hostID string, at time.Time) bool
	mu           sync.Mutex
	locks        map[string]*sync.Mutex
	// agentOnly 记录最近一次离线通知是否判为只有 HostAgent 停止，恢复通知据此选标题。
	agentOnly map[string]bool
}

func newPresenceGate(status func(ctx context.Context, agentID string) (string, error)) *presenceGate {
	return &presenceGate{status: status, locks: make(map[string]*sync.Mutex), agentOnly: make(map[string]bool)}
}

// classify 返回离线是否只是 HostAgent 停止（主机网关心跳仍正常），并记住结论供恢复通知使用。
func (g *presenceGate) classify(ctx context.Context, transition hostmetrics.PresenceTransition) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if transition.To == hostmetrics.PresenceReachable {
		agentOnly := g.agentOnly[transition.AgentID]
		delete(g.agentOnly, transition.AgentID)
		return agentOnly
	}
	agentOnly := g.gatewayAlive != nil && transition.HostID != "" && g.gatewayAlive(ctx, transition.HostID, transition.ObservedAt)
	g.agentOnly[transition.AgentID] = agentOnly
	return agentOnly
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
		agentOnly := gate.classify(ctx, transition)
		severity := notification.SeverityCritical
		title := "主机 " + host + " · 失联"
		body := "状态：新告警\n问题：" + presenceLostReason
		if gate != nil && gate.gatewayAlive != nil {
			body = "状态：新告警\n问题：" + hostLostReason
		}
		switch {
		case transition.To == hostmetrics.PresenceReachable && agentOnly:
			severity = notification.SeverityInfo
			title = "主机 " + host + " · host-agent 已恢复上报"
			body = "状态：已恢复\n当前：" + presenceBackReason
		case transition.To == hostmetrics.PresenceReachable:
			severity = notification.SeverityInfo
			title = "主机 " + host + " · 已恢复在线"
			body = "状态：已恢复\n当前：" + presenceBackReason
		case agentOnly:
			severity = notification.SeverityWarning
			title = "主机 " + host + " · host-agent 停止上报"
			body = "状态：新告警\n问题：" + agentStoppedReason
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
	hostLostReason     = "主机采集代理停止上报，主机网关心跳也已停止，主机可能宕机或断网"
	agentStoppedReason = "主机采集代理（host-agent）超过 3 分钟没有上报，但主机网关心跳正常，主机仍在线；主机守护应在 1 分钟内把它拉起，持续未恢复请查看该主机的 run/logs/host-agent.log"
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
