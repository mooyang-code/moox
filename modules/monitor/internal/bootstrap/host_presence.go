package bootstrap

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
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
		body := "状态：新告警\n问题：主机采集代理停止上报，主机可能宕机、断网，或采集代理已停止"
		if transition.To == hostmetrics.PresenceReachable {
			severity = notification.SeverityInfo
			title = "主机 " + host + " · 已恢复在线"
			body = "状态：已恢复\n当前：主机重新开始上报"
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
