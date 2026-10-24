package hostmetrics

import (
	"context"
	"errors"
	"time"
)

// DefaultHostStaleAfter marks a host unreachable after three missed samples
// of the host agent's one-minute cadence.
const DefaultHostStaleAfter = 3 * time.Minute

type PresenceTransitionSink interface {
	HandlePresenceTransition(context.Context, PresenceTransition) error
}

type PresenceTransitionFunc func(context.Context, PresenceTransition) error

func (f PresenceTransitionFunc) HandlePresenceTransition(ctx context.Context, transition PresenceTransition) error {
	if f != nil {
		return f(ctx, transition)
	}
	return nil
}

type SilenceScanner struct {
	registry   *Registry
	staleAfter time.Duration
	sink       PresenceTransitionSink
	pending    []PresenceTransition
}

func NewSilenceScanner(registry *Registry, staleAfter time.Duration, sink PresenceTransitionSink) *SilenceScanner {
	if staleAfter <= 0 {
		staleAfter = DefaultHostStaleAfter
	}
	return &SilenceScanner{registry: registry, staleAfter: staleAfter, sink: sink}
}

func (s *SilenceScanner) Scan(ctx context.Context, now time.Time) error {
	if s == nil || s.registry == nil {
		return errors.New("host silence scanner is unavailable")
	}
	transitions, err := s.registry.MarkUnreachable(ctx, now, s.staleAfter)
	if err != nil {
		return err
	}
	// 待重发的通知若已被更新的状态取代（例如离线通知失败后主机已恢复并发出恢复通知），直接丢弃，避免“已恢复 → 失联”的错误顺序。
	retained := make([]PresenceTransition, 0, len(s.pending)+len(transitions))
	for _, pending := range s.pending {
		status, err := s.registry.Status(ctx, pending.AgentID)
		if err != nil {
			return err
		}
		if status != pending.To {
			continue
		}
		// 登记主机上已有新采集器在线：这台主机并没有失联，待重发的失联通知作废。
		if pending.To == PresenceUnreachable && pending.HostID != "" {
			if registered := s.registry.registeredHosts(ctx); registered != nil && registered[pending.HostID] {
				if other, err := s.registry.HostHasOtherReachableAgent(ctx, pending.HostID, pending.AgentID); err == nil && other {
					continue
				}
			}
		}
		retained = append(retained, pending)
	}
	transitions = append(retained, transitions...)
	s.pending = nil
	for _, transition := range transitions {
		if s.sink != nil {
			if err := s.sink.HandlePresenceTransition(ctx, transition); err != nil {
				s.pending = append(s.pending, transition)
			}
		}
	}
	return nil
}
