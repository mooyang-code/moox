package resample

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
)

// RunnerConfig controls the bounded one-minute local worker.
type RunnerConfig struct {
	SpaceID               string
	WorkerConcurrency     int
	WorkerJobTimeout      time.Duration
	WorkerPollInterval    time.Duration
	WorkerMaxSourceKeys   int
	StaleRunningAfter     time.Duration
	DefaultSettleDelay    time.Duration
	RepairLookbackBuckets int
}

// Runner is the Collector-local scanner/worker. Timer ticks only enqueue
// bounded work; network reads and writes happen in worker goroutines.
type Runner struct {
	Rules     *store.TaskRuleRepository
	Instances *store.TaskInstanceRepository
	Readiness *store.PeriodReadinessRepository
	Metrics   *Metrics
	Source    subjectSource
	Primary   PrimaryStorage
	Config    RunnerConfig
	mu        sync.Mutex
}

func (r *Runner) Tick(ctx context.Context, now time.Time) error {
	if r == nil || r.Rules == nil || r.Instances == nil || r.Source == nil || r.Primary == nil {
		return fmt.Errorf("resample runner dependencies are required")
	}
	if ctx == nil {
		return fmt.Errorf("resample runner context is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	cfg := r.Config
	if strings.TrimSpace(cfg.SpaceID) == "" {
		return fmt.Errorf("resample runner space_id is required")
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 1
	}
	if cfg.WorkerJobTimeout <= 0 {
		cfg.WorkerJobTimeout = 30 * time.Second
	}
	if cfg.WorkerMaxSourceKeys <= 0 {
		cfg.WorkerMaxSourceKeys = 10000
	}
	if cfg.RepairLookbackBuckets < 0 {
		cfg.RepairLookbackBuckets = 0
	}
	if cfg.StaleRunningAfter <= 0 {
		cfg.StaleRunningAfter = 2 * time.Minute
	}
	if _, err := r.Instances.RecoverExpiredResampleLeasesInSpace(ctx, cfg.SpaceID, now, cfg.WorkerConcurrency*4); err != nil {
		return err
	}
	rules, err := r.Rules.ListEnabled(ctx, cfg.SpaceID)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !strings.EqualFold(rule.DataType, "kline_resample") || rule.PrepareState != domain.PrepareStateReady {
			continue
		}
		if err := PlanRule(ctx, r.Source, r.Instances, rule, now); err != nil {
			return err
		}
		if r.Readiness != nil {
			if err := r.ensureReadiness(ctx, rule, now); err != nil {
				return err
			}
		}
	}
	// Realtime is always drained before repair and backfill.
	for {
		claims, claimErr := r.Instances.ClaimDueResampleTasksInSpace(ctx, cfg.SpaceID, now, domain.ResampleOriginRealtime, cfg.WorkerConcurrency, cfg.WorkerJobTimeout)
		if claimErr != nil {
			return claimErr
		}
		if len(claims) == 0 {
			break
		}
		if r.Metrics != nil {
			r.Metrics.Claims.Add(float64(len(claims)))
		}
		runClaims(ctx, claims, r.Instances, r.Readiness, r.Primary, r.Metrics, cfg)
	}
	if cfg.RepairLookbackBuckets > 0 {
		if err := r.scanRepair(ctx, rules, now, cfg); err != nil {
			return err
		}
	}
	for {
		claims, claimErr := r.Instances.ClaimDueResampleTasksInSpace(ctx, cfg.SpaceID, now, domain.ResampleOriginBackfill, cfg.WorkerConcurrency, cfg.WorkerJobTimeout)
		if claimErr != nil {
			return claimErr
		}
		if len(claims) == 0 {
			break
		}
		if r.Metrics != nil {
			r.Metrics.Claims.Add(float64(len(claims)))
		}
		runClaims(ctx, claims, r.Instances, r.Readiness, r.Primary, r.Metrics, cfg)
	}
	if err := r.completeBackfills(ctx, rules); err != nil {
		return err
	}
	return nil
}

func (r *Runner) completeBackfills(ctx context.Context, rules []domain.TaskRule) error {
	for _, rule := range rules {
		if !strings.EqualFold(rule.DataType, "kline_resample") {
			continue
		}
		instances, err := listAllResampleInstances(ctx, r.Instances, rule.SpaceID, rule.RuleID)
		if err != nil {
			return err
		}
		requestID := ""
		ready := len(instances) > 0
		for _, instance := range instances {
			result, parseErr := domain.ParseResampleTaskResult(instance.Result)
			if parseErr != nil || result.Backfill == nil {
				ready = false
				break
			}
			if requestID == "" {
				requestID = result.Backfill.RequestID
			}
			if result.Backfill.RequestID != requestID || result.Backfill.State != domain.ResampleBackfillSyncing {
				ready = false
				break
			}
		}
		if ready && requestID != "" {
			_, _ = r.Instances.CompleteResampleBackfillSync(ctx, rule.SpaceID, rule.RuleID, requestID)
		}
	}
	return nil
}

func (r *Runner) scanRepair(ctx context.Context, rules []domain.TaskRule, now time.Time, cfg RunnerConfig) error {
	for _, rule := range rules {
		if !strings.EqualFold(rule.DataType, "kline_resample") || rule.PrepareState != domain.PrepareStateReady {
			continue
		}
		params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
		if err != nil {
			return err
		}
		target, err := ParseFixedFrequency(params.TargetFrequency)
		if err != nil {
			return err
		}
		latestStart, _ := BucketAt(now.Add(-params.SettleDelay()), time.Unix(0, 0).UTC(), target)
		instances, err := listAllResampleInstances(ctx, r.Instances, rule.SpaceID, rule.RuleID)
		if err != nil {
			return err
		}
		for _, instance := range instances {
			for i := 0; i < cfg.RepairLookbackBuckets; i++ {
				bucket := latestStart.Add(-time.Duration(i) * target.Duration)
				current, getErr := r.Instances.Get(ctx, instance.SpaceID, instance.TaskID)
				if getErr != nil {
					return getErr
				}
				result, parseErr := domain.ParseResampleTaskResult(current.Result)
				if parseErr != nil || result.State != domain.ResampleTaskStateIdle || result.RealtimeNextBucket == nil {
					continue
				}
				taskClaim, claimed, claimErr := r.Instances.ClaimResampleTask(ctx, instance.SpaceID, instance.TaskID, result.StateVersion, domain.ResampleOriginRepair, bucket, now, cfg.WorkerJobTimeout)
				if claimErr != nil {
					return claimErr
				}
				if claimed {
					runClaims(ctx, []store.ResampleTaskClaim{taskClaim}, r.Instances, r.Readiness, r.Primary, r.Metrics, cfg)
				}
			}
		}
	}
	return nil
}

func runClaims(ctx context.Context, claims []store.ResampleTaskClaim, instances *store.TaskInstanceRepository, readiness *store.PeriodReadinessRepository, primary PrimaryStorage, metrics *Metrics, cfg RunnerConfig) {
	workers := cfg.WorkerConcurrency
	if workers > len(claims) {
		workers = len(claims)
	}
	if workers <= 0 {
		return
	}
	jobs := make(chan store.ResampleTaskClaim)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for claim := range jobs {
				processClaim(ctx, claim, instances, readiness, primary, metrics, cfg)
			}
		}()
	}
	for _, claim := range claims {
		jobs <- claim
	}
	close(jobs)
	wg.Wait()
}

func processClaim(parent context.Context, claim store.ResampleTaskClaim, instances *store.TaskInstanceRepository, readiness *store.PeriodReadinessRepository, primary PrimaryStorage, metrics *Metrics, cfg RunnerConfig) {
	params, err := domain.ParseCollectParams(claim.Instance.TaskParams, claim.Instance.Provider, claim.Instance.MarketType, "kline_resample")
	if err != nil {
		if metrics != nil {
			metrics.Retries.Inc()
		}
		_, _ = instances.FailResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, err.Error())
		return
	}
	sourceFreq, err := ParseFixedFrequency(params.SourceFrequency)
	if err != nil {
		_, _ = instances.FailResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, err.Error())
		return
	}
	targetFreq, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		_, _ = instances.FailResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, err.Error())
		return
	}
	bucket := timeValue(claim.Result.ActiveBucket)
	if bucket.IsZero() {
		_, _ = instances.FailResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, "active bucket is missing")
		return
	}
	spec := RuleSpec{RuleID: claim.Instance.RuleID, SpaceID: claim.Instance.SpaceID, SourceDatasetID: params.SourceDatasetID, SourceFrequency: sourceFreq, SourceSeriesTag: params.SourceSeriesTag, TargetDatasetID: params.TargetDatasetID, TargetFrequency: targetFreq, Alignment: params.Alignment}
	ctx, cancel := context.WithTimeout(parent, cfg.WorkerJobTimeout)
	defer cancel()
	var result Result
	var wrote bool
	err = retry.Do(func() error {
		var runErr error
		result, wrote, runErr = (BucketStorage{Primary: primary}).ProcessBucket(ctx, spec, claim.Instance.SubjectID, bucket, bucket.Add(targetFreq.Duration))
		return runErr
	}, retry.Attempts(4), retry.Delay(250*time.Millisecond), retry.DelayType(retry.BackOffDelay), retry.Context(ctx))
	if err != nil {
		attempt := claim.Result.Attempt + 1
		next := time.Now().UTC().Add(5 * time.Second)
		_, _ = instances.WaitResampleSource(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, attempt, next, err.Error())
		return
	}
	nextBucket := bucket.Add(targetFreq.Duration)
	// Backfill advances its own cursor one target bucket at a time. Repair is a
	// one-shot correction and deliberately leaves the realtime cursor intact.
	if claim.Result.ActiveOrigin == domain.ResampleOriginRepair {
		nextBucket = bucket
	}
	_, _ = instances.CompleteResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, bucket, nextBucket, result.SourceHash)
	if metrics != nil && wrote {
		metrics.Writes.Inc()
	}
	if readiness != nil && claim.Result.ActiveOrigin == domain.ResampleOriginRealtime {
		_ = readiness.MarkSubjectSuccess(parent, domain.PeriodKey{SpaceID: result.SpaceID, DatasetID: result.DatasetID, Frequency: result.Frequency, PeriodTime: result.DataTime}, claim.Instance.SubjectID, localResampleFunction, writeSource, time.Now().UTC())
	}
	_ = wrote
}

func (r *Runner) ensureReadiness(ctx context.Context, rule domain.TaskRule, now time.Time) error {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return err
	}
	target, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return err
	}
	start, _ := BucketAt(now.Add(-params.SettleDelay()), time.Unix(0, 0).UTC(), target)
	instances, err := listAllResampleInstances(ctx, r.Instances, rule.SpaceID, rule.RuleID)
	if err != nil {
		return err
	}
	tasks := make([]domain.PeriodTaskSeed, 0, len(instances))
	for _, instance := range instances {
		tasks = append(tasks, domain.PeriodTaskSeed{TaskID: instance.TaskID, SubjectID: instance.SubjectID, FunctionName: localResampleFunction, WriteSource: writeSource, RequiredFields: `["open","high","low","close","volume","quote_volume","trade_num"]`})
	}
	if len(tasks) == 0 {
		return nil
	}
	_, err = r.Readiness.EnsurePeriod(ctx, domain.PeriodSeed{PeriodKey: domain.PeriodKey{SpaceID: rule.SpaceID, DatasetID: params.TargetDatasetID, Frequency: target.Storage, PeriodTime: start}, DeadlineAt: start.Add(target.Duration).Add(2 * time.Minute), WorkType: "resample", Tasks: tasks})
	return err
}

func listAllResampleInstances(ctx context.Context, repo *store.TaskInstanceRepository, spaceID, ruleID string) ([]domain.TaskInstance, error) {
	const pageSize = 1000
	var all []domain.TaskInstance
	for page := 1; ; page++ {
		instances, total, err := repo.List(ctx, store.TaskInstanceFilter{SpaceID: spaceID, RuleID: ruleID, DataType: "kline_resample", IncludeDeleted: false, Page: page, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		all = append(all, instances...)
		if len(instances) == 0 || int64(len(all)) >= total || len(instances) < pageSize {
			return all, nil
		}
	}
}

func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
