package hostmetrics

import (
	"context"
	"errors"
	"time"
)

const DefaultHostStaleAfter = 90 * time.Second

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
	transitions = append(s.pending, transitions...)
	s.pending = s.pending[:0]
	for _, transition := range transitions {
		if s.sink != nil {
			if err := s.sink.HandlePresenceTransition(ctx, transition); err != nil {
				s.pending = append(s.pending, transition)
			}
		}
	}
	return nil
}
