package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/msgbox"
	"trpc.group/trpc-go/trpc-go/log"
)

func hostPresenceTransitionSink(sender msgbox.Sender) hostmetrics.PresenceTransitionFunc {
	return func(ctx context.Context, transition hostmetrics.PresenceTransition) error {
		log.InfoContextf(ctx, "host presence transition agent_id=%s from=%s to=%s observed_at=%s",
			transition.AgentID, transition.From, transition.To, transition.ObservedAt.Format(time.RFC3339Nano))
		if sender == nil {
			return nil
		}
		severity := msgbox.SeverityCritical
		title := "MooX host unreachable"
		if transition.To == hostmetrics.PresenceReachable {
			severity = msgbox.SeverityInfo
			title = "MooX host recovered"
		}
		if err := sender.Send(ctx, msgbox.Message{
			Key:      "host-presence:" + transition.AgentID,
			Severity: severity,
			Title:    title,
			Body:     fmt.Sprintf("HostAgent %s changed from %s to %s", transition.AgentID, transition.From, transition.To),
			Labels: map[string]string{
				"agent_id": transition.AgentID,
				"from":     string(transition.From),
				"to":       string(transition.To),
			},
		}); err != nil {
			log.ErrorContextf(ctx, "host presence notification failed agent_id=%s: %v", transition.AgentID, err)
			return err
		}
		return nil
	}
}
