package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/probe"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

type ResultHook func(context.Context, domain.Check, domain.CheckResult)

type Options struct {
	InstanceID     string
	MaxConcurrency int
	Runner         probe.Runner
	OnResult       ResultHook
	DueBatchSize   int
}

type Scheduler struct {
	checks     *store.CheckRepository
	results    *store.ResultRepository
	instanceID string
	maxConc    int
	runner     probe.Runner
	onResult   ResultHook
	dueBatch   int
}

func New(repos *store.Repositories, opts Options) *Scheduler {
	maxConc := opts.MaxConcurrency
	if maxConc <= 0 {
		maxConc = 16
	}
	instanceID := opts.InstanceID
	if instanceID == "" {
		instanceID = "monitor"
	}
	runner := opts.Runner
	if runner == nil {
		runner = probe.DefaultRunner()
	}
	dueBatch := opts.DueBatchSize
	if dueBatch <= 0 {
		dueBatch = 500
	}
	if repos == nil {
		repos = store.NewRepositories(nil)
	}
	return &Scheduler{
		checks:     repos.Checks,
		results:    repos.Results,
		instanceID: instanceID,
		maxConc:    maxConc,
		runner:     runner,
		onResult:   opts.OnResult,
		dueBatch:   dueBatch,
	}
}

func (s *Scheduler) RunDueOnce(ctx context.Context) (int, error) {
	checks, err := s.checks.ListDue(ctx, time.Now().UTC(), s.dueBatch)
	if err != nil {
		return 0, err
	}
	sem := make(chan struct{}, s.maxConc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var joined error
	for _, check := range checks {
		check := check
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if _, err := s.runAndPersist(ctx, check, true); err != nil {
				mu.Lock()
				joined = errors.Join(joined, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(checks), joined
}

func (s *Scheduler) RunCheckOnce(ctx context.Context, check domain.Check) (domain.CheckResult, error) {
	return s.runAndPersist(ctx, check, false)
}

func (s *Scheduler) runAndPersist(ctx context.Context, check domain.Check, advanceSchedule bool) (domain.CheckResult, error) {
	result := s.runner.Run(ctx, check)
	normalizeResult(&result, check, s.instanceID)
	if err := s.results.Insert(ctx, &result); err != nil {
		return result, err
	}
	if advanceSchedule {
		nextAt := result.CheckedAt.Add(time.Duration(check.IntervalSeconds) * time.Second)
		if err := s.checks.MarkChecked(ctx, check.SpaceID, check.CheckID, result.CheckedAt, nextAt); err != nil {
			return result, err
		}
	}
	if s.onResult != nil {
		s.onResult(ctx, check, result)
	}
	return result, nil
}

func normalizeResult(result *domain.CheckResult, check domain.Check, instanceID string) {
	if result.ResultID == "" {
		result.ResultID = newResultID()
	}
	if result.SpaceID == "" {
		result.SpaceID = check.SpaceID
	}
	if result.CheckID == "" {
		result.CheckID = check.CheckID
	}
	if result.InstanceID == "" {
		result.InstanceID = instanceID
	}
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	if result.Status == "" {
		if result.Success {
			result.Status = domain.CheckStatusOK
		} else {
			result.Status = domain.CheckStatusDown
		}
	}
}

func newResultID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "result-" + hex.EncodeToString(b[:])
	}
	return "result-" + time.Now().UTC().Format("20060102150405.000000000")
}
