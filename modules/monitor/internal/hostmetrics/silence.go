package hostmetrics

import (
	"context"
	"errors"
	"time"
)

const DefaultHostStaleAfter = 90 * time.Second

type PresenceTransitionSink interface {
	HandlePresenceTransition(context.Context, PresenceTransition)
}

type PresenceTransitionFunc func(context.Context, PresenceTransition)

func (f PresenceTransitionFunc) HandlePresenceTransition(ctx context.Context, transition PresenceTransition) {
	if f != nil {
		f(ctx, transition)
	}
}

type SilenceScanner struct {
	registry   *Registry
	staleAfter time.Duration
	sink       PresenceTransitionSink
}

func NewSilenceScanner(registry *Registry, staleAfter time.Duration, sink PresenceTransitionSink) *SilenceScanner {
	if staleAfter <= 0 {
		staleAfter = DefaultHostStaleAfter
	}
	return &SilenceScanner{registry: registry, staleAfter: staleAfter, sink: sink}
}

func (s *SilenceScanner) SetTransitionSink(sink PresenceTransitionSink) {
	if s != nil {
		s.sink = sink
	}
}

func (s *SilenceScanner) Scan(ctx context.Context, now time.Time) error {
	if s == nil || s.registry == nil {
		return errors.New("host silence scanner is unavailable")
	}
	transitions, err := s.registry.MarkUnreachable(ctx, now, s.staleAfter)
	if err != nil {
		return err
	}
	for _, transition := range transitions {
		if s.sink != nil {
			s.sink.HandlePresenceTransition(ctx, transition)
		}
	}
	return nil
}
