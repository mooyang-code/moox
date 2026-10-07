package bootstrap

import (
	"context"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

func hostPresenceTransitionSink(sender notification.Sender) hostmetrics.PresenceTransitionFunc {
	return hostPresenceTransitionSinkProvider(func(context.Context) (notification.Sender, error) { return sender, nil })
}

func hostPresenceTransitionSinkProvider(provider func(context.Context) (notification.Sender, error)) hostmetrics.PresenceTransitionFunc {
	return hostPresenceTransitionSinkProviderWithFailure(provider, nil)
}

func hostPresenceTransitionSinkProviderWithFailure(provider func(context.Context) (notification.Sender, error), onFailure func(context.Context, hostmetrics.PresenceTransition, error)) hostmetrics.PresenceTransitionFunc {
	return func(ctx context.Context, transition hostmetrics.PresenceTransition) error {
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
