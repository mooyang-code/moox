package resample

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// RunnerConfig controls the bounded one-minute local worker.
type RunnerConfig struct {
	SpaceID               string
	ScanTimeout           time.Duration
	WorkerConcurrency     int
	MaxClaimsPerTick      int
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
	Rules        *store.TaskRuleRepository
	Instances    *store.TaskInstanceRepository
	Readiness    *store.PeriodReadinessRepository
	Metrics      *Metrics
	Source       subjectSource
	Primary      PrimaryStorage
	Config       RunnerConfig
	mu           sync.Mutex
	repairCursor int
}

func (r *Runner) Tick(ctx context.Context, now time.Time) error {
	if r == nil || r.Rules == nil || r.Instances == nil || r.Source == nil || r.Primary == nil {
		return fmt.Errorf("resample runner dependencies are required")
	}
	if ctx == nil {
		return fmt.Errorf("resample runner context is required")
	}
	if !r.mu.TryLock() {
		// Timer callbacks are best-effort. Never queue behind a slow previous
		// scan; the next minute will retry the durable due work.
		return nil
	}
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
	if cfg.ScanTimeout <= 0 {
		cfg.ScanTimeout = 8 * time.Second
	}
	if cfg.MaxClaimsPerTick < 3 {
		cfg.MaxClaimsPerTick = 100
	}
	if cfg.MaxClaimsPerTick > 1000 {
		cfg.MaxClaimsPerTick = 1000
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
	newScanCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, cfg.ScanTimeout)
	}
	scanCtx, cancel := newScanCtx()
	defer cancel()
	recoverLimit := cfg.WorkerConcurrency
	if recoverLimit > 250 {
		recoverLimit = 250
	}
	if _, err := r.Instances.RecoverExpiredResampleLeasesInSpace(scanCtx, cfg.SpaceID, now, recoverLimit*4); err != nil {
		return err
	}
	rules, err := r.Rules.ListEnabled(scanCtx, cfg.SpaceID)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !strings.EqualFold(rule.DataType, "kline_resample") || rule.PrepareState != domain.PrepareStateReady {
			continue
		}
		if err := PlanRule(scanCtx, r.Source, r.Instances, rule, now); err != nil {
			return err
		}
		if r.Readiness != nil {
			if err := r.ensureReadiness(scanCtx, rule, now); err != nil {
				return err
			}
		}
	}
	cancel()
	// Realtime is prioritized, but every phase shares a per-tick claim budget so
	// a large subject inventory cannot monopolize the one-minute timer.
	claimsRemaining := cfg.MaxClaimsPerTick
	backfillReserve := 0
	repairReserve := 0
	if cfg.MaxClaimsPerTick > 2 {
		backfillReserve = 1
		if cfg.RepairLookbackBuckets > 0 {
			repairReserve = 1
		}
	}
	realtimeBudget := cfg.MaxClaimsPerTick - backfillReserve - repairReserve
	for realtimeBudget > 0 {
		limit := cfg.WorkerConcurrency
		if limit > realtimeBudget {
			limit = realtimeBudget
		}
		claimCtx, claimCancel := newScanCtx()
		claims, claimErr := r.Instances.ClaimDueResampleTasksInSpace(claimCtx, cfg.SpaceID, now, domain.ResampleOriginRealtime, limit, cfg.WorkerJobTimeout)
		claimCancel()
		if claimErr != nil {
			return claimErr
		}
		if len(claims) == 0 {
			break
		}
		if r.Metrics != nil {
			r.Metrics.Claims.Add(float64(len(claims)))
		}
		if r.Readiness != nil {
			readinessCtx, readinessCancel := newScanCtx()
			err := r.ensureReadinessForClaims(readinessCtx, claims, rules)
			readinessCancel()
			if err != nil {
				return err
			}
		}
		runClaims(ctx, claims, r.Instances, r.Readiness, r.Primary, r.Source, r.Metrics, cfg)
		realtimeBudget -= len(claims)
		claimsRemaining -= len(claims)
	}
	if cfg.RepairLookbackBuckets > 0 {
		// Realtime keeps the minimum lower-phase reserves, but any unused
		// realtime capacity is returned to repair so idle minutes can drain the
		// moving lookback window instead of wasting claims.
		repairBudget := claimsRemaining - backfillReserve
		if repairBudget < 0 {
			repairBudget = 0
		}
		if repairBudget > cfg.WorkerConcurrency {
			// Repair is best-effort and automatic. Keep one scan bounded to a
			// single worker batch so a Storage outage cannot hold the timer and
			// ordinary market-fetch scheduling for many job timeouts.
			repairBudget = cfg.WorkerConcurrency
		}
		beforeRepair := repairBudget
		repairCtx, repairCancel := newScanCtx()
		err := r.scanRepair(repairCtx, ctx, rules, now, cfg, &repairBudget)
		repairCancel()
		if err != nil {
			return err
		}
		claimsRemaining -= beforeRepair - repairBudget
	}
	for claimsRemaining > 0 {
		limit := cfg.WorkerConcurrency
		if limit > claimsRemaining {
			limit = claimsRemaining
		}
		claimCtx, claimCancel := newScanCtx()
		claims, claimErr := r.Instances.ClaimDueResampleTasksInSpace(claimCtx, cfg.SpaceID, now, domain.ResampleOriginBackfill, limit, cfg.WorkerJobTimeout)
		claimCancel()
		if claimErr != nil {
			return claimErr
		}
		if len(claims) == 0 {
			break
		}
		if r.Metrics != nil {
			r.Metrics.Claims.Add(float64(len(claims)))
		}
		runClaims(ctx, claims, r.Instances, r.Readiness, r.Primary, r.Source, r.Metrics, cfg)
		claimsRemaining -= len(claims)
	}
	completeCtx, completeCancel := newScanCtx()
	err = r.completeBackfills(completeCtx, rules)
	completeCancel()
	if err != nil {
		return err
	}
	return nil
}

func (r *Runner) completeBackfills(ctx context.Context, rules []domain.TaskRule) error {
	var firstErr error
	for _, rule := range rules {
		if !strings.EqualFold(rule.DataType, "kline_resample") {
			continue
		}
		instances, err := listAllResampleInstances(ctx, r.Instances, rule.SpaceID, rule.RuleID)
		if err != nil {
			return err
		}
		requestID, ready := resampleBackfillSyncRequest(instances)
		if ready && requestID != "" {
			if err := r.completeBackfillWithViewFence(ctx, rule, requestID); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func (r *Runner) completeBackfillWithViewFence(ctx context.Context, rule domain.TaskRule, requestID string) error {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return err
	}
	syncer, ok := r.Primary.(SyncPointStorage)
	if !ok {
		return fmt.Errorf("resample backfill View fence is unavailable")
	}
	if err := syncer.AppendDatasetSyncPoint(ctx, rule.SpaceID, params.TargetDatasetID, requestID, "catchup"); err != nil {
		return fmt.Errorf("append resample backfill sync point: %w", err)
	}
	viewID := DefaultTargetViewID(params.TargetDatasetID)
	response, err := syncer.WaitViewSyncPoint(ctx, &storagepb.WaitViewSyncPointReq{
		SpaceId: rule.SpaceID, ViewId: viewID, RequestId: requestID,
		DatasetIds: []string{params.TargetDatasetID}, WaitTimeoutMs: 5000,
	})
	if err != nil {
		return fmt.Errorf("wait resample backfill View fence: %w", err)
	}
	if response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		if response == nil || response.GetRetInfo() == nil {
			return fmt.Errorf("resample backfill View fence returned an empty response")
		}
		return fmt.Errorf("resample backfill View fence rejected: %s", response.GetRetInfo().GetMsg())
	}
	if !response.GetReady() {
		return fmt.Errorf("resample backfill View fence is not ready")
	}
	if _, err := r.Instances.CompleteResampleBackfillSync(ctx, rule.SpaceID, rule.RuleID, requestID); err != nil {
		return fmt.Errorf("complete resample backfill: %w", err)
	}
	return nil
}

func resampleBackfillSyncRequest(instances []domain.TaskInstance) (string, bool) {
	requestID := ""
	participants := 0
	for _, instance := range instances {
		result, err := domain.ParseResampleTaskResult(instance.Result)
		if err != nil {
			return "", false
		}
		// A subject added while a backfill is running has no Backfill state
		// yet. It is not part of this request snapshot and must not block
		// completion; the next explicit request can include it.
		if result.Backfill == nil {
			continue
		}
		if requestID == "" {
			requestID = result.Backfill.RequestID
		}
		if result.Backfill.RequestID != requestID || result.Backfill.State != domain.ResampleBackfillSyncing {
			return "", false
		}
		participants++
	}
	return requestID, participants > 0 && requestID != ""
}

func (r *Runner) scanRepair(scanCtx, workerCtx context.Context, rules []domain.TaskRule, now time.Time, cfg RunnerConfig, claimsRemaining *int) error {
	if claimsRemaining == nil {
		return fmt.Errorf("repair claim budget is required")
	}
	if *claimsRemaining <= 0 {
		return nil
	}
	type repairCandidate struct {
		instance    domain.TaskInstance
		oldestStart time.Time
		latestStart time.Time
		target      time.Duration
	}
	candidates := make([]repairCandidate, 0)
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
		oldestStart := latestStart.Add(-time.Duration(cfg.RepairLookbackBuckets-1) * target.Duration)
		instances, err := listAllResampleInstances(scanCtx, r.Instances, rule.SpaceID, rule.RuleID)
		if err != nil {
			return err
		}
		for _, instance := range instances {
			candidates = append(candidates, repairCandidate{instance: instance, oldestStart: oldestStart, latestStart: latestStart, target: target.Duration})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	// Advance a local scan cursor rather than deriving the start from wall-clock
	// minutes. A skipped/slow timer tick must not alias to the same subset when
	// the candidate count shares a factor with the elapsed minutes.
	start := repairScanStart(r.repairCursor, len(candidates))
	inspected := 0
	defer func() { r.repairCursor = repairScanAdvance(start, inspected, len(candidates)) }()
	claimCapacity := *claimsRemaining
	if claimCapacity > len(candidates) {
		claimCapacity = len(candidates)
	}
	claims := make([]store.ResampleTaskClaim, 0, claimCapacity)
	for offset := 0; offset < len(candidates) && *claimsRemaining > 0; offset++ {
		inspected = offset + 1
		candidate := candidates[(start+offset)%len(candidates)]
		current, getErr := r.Instances.Get(scanCtx, candidate.instance.SpaceID, candidate.instance.TaskID)
		if getErr != nil {
			return getErr
		}
		result, parseErr := domain.ParseResampleTaskResult(current.Result)
		if parseErr != nil || result.State != domain.ResampleTaskStateIdle || result.RealtimeNextBucket == nil {
			continue
		}
		bucket := chooseRepairBucket(result, candidate.oldestStart, candidate.latestStart, candidate.target)
		taskClaim, claimed, claimErr := r.Instances.ClaimResampleTask(scanCtx, candidate.instance.SpaceID, candidate.instance.TaskID, result.StateVersion, domain.ResampleOriginRepair, bucket, now, cfg.WorkerJobTimeout)
		if claimErr != nil {
			return claimErr
		}
		if !claimed {
			continue
		}
		if r.Metrics != nil {
			r.Metrics.Claims.Add(1)
		}
		claims = append(claims, taskClaim)
		*claimsRemaining--
	}
	if len(claims) > 0 {
		runClaims(workerCtx, claims, r.Instances, r.Readiness, r.Primary, r.Source, r.Metrics, cfg)
	}
	return nil
}

func repairScanStart(cursor, candidateCount int) int {
	if candidateCount <= 0 {
		return 0
	}
	start := cursor % candidateCount
	if start < 0 {
		start += candidateCount
	}
	return start
}

func repairScanAdvance(start, inspected, candidateCount int) int {
	if candidateCount <= 0 {
		return 0
	}
	if inspected < 0 {
		inspected = 0
	}
	return (start + inspected) % candidateCount
}

func chooseRepairBucket(result domain.ResampleTaskResult, oldestStart, latestStart time.Time, target time.Duration) time.Time {
	if result.RepairNextBucket == nil || result.RepairNextBucket.Before(oldestStart) || result.RepairNextBucket.After(latestStart) || !domain.IsEpochTimeAligned(*result.RepairNextBucket, target) {
		return oldestStart.UTC()
	}
	return result.RepairNextBucket.UTC()
}

func runClaims(ctx context.Context, claims []store.ResampleTaskClaim, instances *store.TaskInstanceRepository, readiness *store.PeriodReadinessRepository, primary PrimaryStorage, source subjectSource, metrics *Metrics, cfg RunnerConfig) {
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
				processClaim(ctx, claim, instances, readiness, primary, source, metrics, cfg)
			}
		}()
	}
	for _, claim := range claims {
		jobs <- claim
	}
	close(jobs)
	wg.Wait()
}

func failResampleClaim(parent context.Context, instances *store.TaskInstanceRepository, claim store.ResampleTaskClaim, lastError string) {
	var err error
	if claim.Result.ActiveOrigin == domain.ResampleOriginBackfill {
		_, err = instances.FailResampleBackfillTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, lastError)
	} else {
		_, err = instances.FailResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, lastError)
	}
	if err != nil {
		log.Printf("resample task failure state update failed task=%s: %v", claim.Instance.TaskID, err)
	}
}

func processClaim(parent context.Context, claim store.ResampleTaskClaim, instances *store.TaskInstanceRepository, readiness *store.PeriodReadinessRepository, primary PrimaryStorage, source subjectSource, metrics *Metrics, cfg RunnerConfig) {
	params, err := domain.ParseCollectParams(claim.Instance.TaskParams, claim.Instance.Provider, claim.Instance.MarketType, "kline_resample")
	if err != nil {
		if metrics != nil {
			metrics.Retries.Inc()
		}
		failResampleClaim(parent, instances, claim, err.Error())
		return
	}
	sourceFreq, err := ParseFixedFrequency(params.SourceFrequency)
	if err != nil {
		failResampleClaim(parent, instances, claim, err.Error())
		return
	}
	targetFreq, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		failResampleClaim(parent, instances, claim, err.Error())
		return
	}
	bucket := timeValue(claim.Result.ActiveBucket)
	if bucket.IsZero() {
		failResampleClaim(parent, instances, claim, "active bucket is missing")
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
		if errors.Is(err, ErrResampleSourceIncomplete) {
			if expired, reason := sourceRetentionExpired(parent, source, claim.Instance.SpaceID, params.SourceDatasetID, bucket); expired {
				if claim.Result.ActiveOrigin == domain.ResampleOriginRepair {
					if _, skipErr := instances.SkipResampleRepairTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, bucket, bucket.Add(targetFreq.Duration), reason); skipErr != nil {
						log.Printf("resample repair skip state update failed task=%s: %v", claim.Instance.TaskID, skipErr)
					}
					return
				}
				failResampleClaim(parent, instances, claim, reason)
				return
			}
		}
		attempt := claim.Result.Attempt + 1
		next := time.Now().UTC().Add(5 * time.Second)
		if _, waitErr := instances.WaitResampleSource(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, attempt, next, err.Error()); waitErr != nil {
			log.Printf("resample task retry state update failed task=%s: %v", claim.Instance.TaskID, waitErr)
		}
		return
	}
	nextBucket := bucket.Add(targetFreq.Duration)
	// Backfill and repair advance their own cursors one target bucket at a time;
	// repair deliberately leaves the realtime cursor intact.
	if claim.Result.ActiveOrigin == domain.ResampleOriginRepair {
		nextBucket = bucket.Add(targetFreq.Duration)
	}
	// Publish readiness before advancing the durable cursor. If the process
	// crashes between these operations, lease recovery retries the same bucket;
	// the readiness write is idempotent and no success marker is lost.
	if readiness != nil && claim.Result.ActiveOrigin == domain.ResampleOriginRealtime {
		if markErr := readiness.MarkSubjectSuccess(parent, domain.PeriodKey{SpaceID: result.SpaceID, DatasetID: result.DatasetID, Frequency: result.Frequency, PeriodTime: result.DataTime}, claim.Instance.SubjectID, localResampleFunction, writeSource, time.Now().UTC()); markErr != nil {
			log.Printf("resample readiness state update failed task=%s: %v", claim.Instance.TaskID, markErr)
			attempt := claim.Result.Attempt + 1
			nextRetry := time.Now().UTC().Add(5 * time.Second)
			if _, waitErr := instances.WaitResampleSource(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, attempt, nextRetry, "period readiness: "+markErr.Error()); waitErr != nil {
				log.Printf("resample readiness retry state update failed task=%s: %v", claim.Instance.TaskID, waitErr)
			}
			return
		}
	}
	completed, completeErr := instances.CompleteResampleTask(parent, claim.Instance.SpaceID, claim.Instance.TaskID, claim.Result.StateVersion, bucket, nextBucket, result.SourceHash)
	if completeErr != nil {
		log.Printf("resample task completion state update failed task=%s: %v", claim.Instance.TaskID, completeErr)
		return
	}
	if !completed {
		log.Printf("resample task completion lost CAS race task=%s", claim.Instance.TaskID)
		return
	}
	if metrics != nil && wrote {
		metrics.Writes.Inc()
	}
	_ = wrote
}

func sourceRetentionExpired(ctx context.Context, source subjectSource, spaceID, datasetID string, bucket time.Time) (bool, string) {
	if source == nil {
		return false, ""
	}
	info, err := source.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return false, ""
	}
	raw := strings.TrimSpace(info.KeepDuration)
	if raw == "" || raw == "0" {
		return false, ""
	}
	keep, err := time.ParseDuration(raw)
	if err != nil || keep <= 0 {
		return false, ""
	}
	if !bucket.Before(time.Now().UTC().Add(-keep)) {
		return false, ""
	}
	return true, fmt.Sprintf("source Dataset retention expired for bucket %s (keep_duration=%s)", bucket.UTC().Format(time.RFC3339), raw)
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
	latest, _ := BucketAt(now.Add(-params.SettleDelay()), time.Unix(0, 0).UTC(), target)
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
	periods := map[time.Time]struct{}{latest.UTC(): {}}
	for _, instance := range instances {
		result, parseErr := domain.ParseResampleTaskResult(instance.Result)
		if parseErr != nil || result.RealtimeNextBucket == nil || result.RealtimeNextBucket.IsZero() {
			continue
		}
		bucket := result.RealtimeNextBucket.UTC()
		if !domain.IsEpochTimeAligned(bucket, target.Duration) || bucket.After(latest) {
			continue
		}
		periods[bucket] = struct{}{}
	}
	for period := range periods {
		if _, err := r.Readiness.EnsurePeriod(ctx, domain.PeriodSeed{PeriodKey: domain.PeriodKey{SpaceID: rule.SpaceID, DatasetID: params.TargetDatasetID, Frequency: target.Storage, PeriodTime: period}, DeadlineAt: period.Add(target.Duration).Add(2 * time.Minute), WorkType: "resample", Tasks: tasks}); err != nil {
			return err
		}
	}
	return nil
}

// ensureReadinessForClaims creates the immutable subject snapshot for the
// exact realtime buckets claimed in this batch. This is needed when a task is
// catching up multiple buckets in one tick: a single latest-bucket snapshot
// would leave intermediate buckets without a parent to mark complete.
func (r *Runner) ensureReadinessForClaims(ctx context.Context, claims []store.ResampleTaskClaim, rules []domain.TaskRule) error {
	if r.Readiness == nil || len(claims) == 0 {
		return nil
	}
	ruleByID := make(map[string]domain.TaskRule, len(rules))
	for _, rule := range rules {
		ruleByID[rule.SpaceID+"\x00"+rule.RuleID] = rule
	}
	type readinessPlan struct {
		rule   domain.TaskRule
		params *domain.CollectParams
		target FixedFrequency
		tasks  []domain.PeriodTaskSeed
		period map[time.Time]struct{}
	}
	plans := make(map[string]*readinessPlan)
	for _, claim := range claims {
		if claim.Result.ActiveOrigin != domain.ResampleOriginRealtime || claim.Result.ActiveBucket == nil {
			continue
		}
		rule, ok := ruleByID[claim.Instance.SpaceID+"\x00"+claim.Instance.RuleID]
		if !ok {
			continue
		}
		key := rule.SpaceID + "\x00" + rule.RuleID
		plan := plans[key]
		if plan == nil {
			params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
			if err != nil {
				return err
			}
			target, err := ParseFixedFrequency(params.TargetFrequency)
			if err != nil {
				return err
			}
			instances, err := listAllResampleInstances(ctx, r.Instances, rule.SpaceID, rule.RuleID)
			if err != nil {
				return err
			}
			tasks := make([]domain.PeriodTaskSeed, 0, len(instances))
			for _, instance := range instances {
				tasks = append(tasks, domain.PeriodTaskSeed{TaskID: instance.TaskID, SubjectID: instance.SubjectID, FunctionName: localResampleFunction, WriteSource: writeSource, RequiredFields: `["open","high","low","close","volume","quote_volume","trade_num"]`})
			}
			plan = &readinessPlan{rule: rule, params: params, target: target, tasks: tasks, period: make(map[time.Time]struct{})}
			plans[key] = plan
		}
		bucket := claim.Result.ActiveBucket.UTC()
		if domain.IsEpochTimeAligned(bucket, plan.target.Duration) {
			plan.period[bucket] = struct{}{}
		}
	}
	for _, plan := range plans {
		if len(plan.tasks) == 0 {
			continue
		}
		for period := range plan.period {
			if _, err := r.Readiness.EnsurePeriod(ctx, domain.PeriodSeed{PeriodKey: domain.PeriodKey{SpaceID: plan.rule.SpaceID, DatasetID: plan.params.TargetDatasetID, Frequency: plan.target.Storage, PeriodTime: period}, DeadlineAt: period.Add(plan.target.Duration).Add(2 * time.Minute), WorkType: "resample", Tasks: plan.tasks}); err != nil {
				return err
			}
		}
	}
	return nil
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
