package trigger

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/robfig/cron"
)

// Scheduler is the small in-process schedule trigger. It deliberately emits
// a callback only; evaluation and persistence stay in Processor, so schedule
// and event triggers share exactly the same idempotent path.
type Scheduler struct {
	mu      sync.Mutex
	cron    *cron.Cron
	cancel  context.CancelFunc
	refresh time.Duration
	OnError func(error)
}

type ScheduleJob struct {
	Cron     string
	Timezone string
	Run      func(context.Context, time.Time) error
}

func (s *Scheduler) Start(ctx context.Context, jobs []ScheduleJob) error {
	if s == nil {
		return errors.New("strategy scheduler is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		return errors.New("strategy scheduler is already started")
	}
	c, cancel := context.WithCancel(ctx)
	cronRunner, err := buildCron(c, jobs, s.OnError, true)
	if err != nil {
		cancel()
		return err
	}
	cronRunner.Start()
	s.cron = cronRunner
	s.cancel = cancel
	return nil
}

// StartDynamic refreshes schedule jobs periodically. Definitions and
// instances are editable while the process is running, so a static cron list
// would require an unnecessary restart. The callback is expected to return
// only valid enabled jobs; refresh failures keep the previous schedule alive.
func (s *Scheduler) StartDynamic(ctx context.Context, interval time.Duration, load func(context.Context) ([]ScheduleJob, error)) error {
	if s == nil || load == nil {
		return errors.New("strategy dynamic scheduler is not configured")
	}
	if interval <= 0 {
		interval = time.Minute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil || s.cancel != nil {
		return errors.New("strategy scheduler is already started")
	}
	jobs, err := load(ctx)
	if err != nil {
		return err
	}
	c, cancel := context.WithCancel(ctx)
	cronRunner, err := buildCron(c, jobs, s.OnError, false)
	if err != nil {
		cancel()
		return err
	}
	cronRunner.Start()
	s.cron, s.cancel, s.refresh = cronRunner, cancel, interval
	go s.refreshLoop(c, interval, load)
	return nil
}

func buildCron(ctx context.Context, jobs []ScheduleJob, onError func(error), strict bool) (*cron.Cron, error) {
	cronRunner := cron.New()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	for _, job := range jobs {
		if job.Run == nil || job.Cron == "" {
			if strict {
				return nil, errors.New("strategy schedule job is incomplete")
			}
			if onError != nil {
				onError(errors.New("strategy schedule job is incomplete"))
			}
			continue
		}
		location := time.UTC
		if job.Timezone != "" {
			loc, err := time.LoadLocation(job.Timezone)
			if err != nil {
				if strict {
					return nil, err
				}
				if onError != nil {
					onError(err)
				}
				continue
			}
			location = loc
		}
		spec, err := parser.Parse(job.Cron)
		if err != nil {
			if strict {
				return nil, err
			}
			if onError != nil {
				onError(err)
			}
			continue
		}
		planned := &cronSpecWithLocation{Schedule: spec, Location: location}
		jobCopy := job
		cronRunner.Schedule(planned, cron.FuncJob(func() {
			// robfig/cron computes the next occurrence before starting the
			// asynchronous Job.  Read the occurrence that just fired, not the
			// newly computed one.
			at := planned.PreviousTime()
			if at.IsZero() {
				at = planned.PlannedTime()
			}
			if at.IsZero() {
				at = time.Now().UTC()
			}
			if err := runScheduledJob(ctx, jobCopy.Run, at); err != nil && onError != nil {
				onError(err)
			}
		}))
	}
	return cronRunner, nil
}

func runScheduledJob(ctx context.Context, run func(context.Context, time.Time) error, at time.Time) error {
	if run == nil {
		return errors.New("strategy schedule job is incomplete")
	}
	var err error
	// A scheduled wake-up may arrive just before Storage/Factor publishes its
	// final row. Keep the same bar identity while giving the pipeline a bounded
	// retry window; a later cron run must not silently consume that bar.
	for attempt := 0; attempt < 6; attempt++ {
		err = run(ctx, at)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < 5 {
			delay := time.Duration(1<<attempt) * time.Second
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}

func (s *Scheduler) refreshLoop(ctx context.Context, interval time.Duration, load func(context.Context) ([]ScheduleJob, error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobs, err := load(ctx)
			if err != nil {
				if s.OnError != nil {
					s.OnError(err)
				}
				continue
			}
			next, err := buildCron(ctx, jobs, s.OnError, false)
			if err != nil {
				if s.OnError != nil {
					s.OnError(err)
				}
				continue
			}
			next.Start()
			s.mu.Lock()
			if s.cancel == nil {
				s.mu.Unlock()
				next.Stop()
				return
			}
			old := s.cron
			s.cron = next
			s.mu.Unlock()
			if old != nil {
				old.Stop()
			}
		}
	}
}

type cronSpecWithLocation struct {
	cron.Schedule
	Location *time.Location
	mu       sync.Mutex
	planned  time.Time
	previous time.Time
}

func (s *cronSpecWithLocation) Next(t time.Time) time.Time {
	next := s.Schedule.Next(t.In(s.Location)).In(time.UTC)
	s.mu.Lock()
	s.previous = s.planned
	s.planned = next
	s.mu.Unlock()
	return next
}

func (s *cronSpecWithLocation) PlannedTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planned
}

func (s *cronSpecWithLocation) PreviousTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previous
}

func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.cron != nil {
		s.cron.Stop()
	}
	s.cancel = nil
	s.cron = nil
	s.refresh = 0
}
