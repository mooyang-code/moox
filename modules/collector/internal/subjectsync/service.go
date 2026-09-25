package subjectsync

import (
	"context"
	"time"
)

type Service struct {
	Tags       *TagRunner
	Attributes *AttributeRunner
	Poll       time.Duration
}

// Run executes both control loops serially. A failed tag or attribute job is
// isolated inside its runner, so one bad source cannot stop the other loop.
func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	poll := s.Poll
	if poll <= 0 {
		poll = time.Minute
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) RunOnce(ctx context.Context) {
	if s == nil {
		return
	}
	if s.Tags != nil {
		s.Tags.RunOnce(ctx)
	}
	if s.Attributes != nil {
		s.Attributes.RunOnce(ctx)
	}
}
