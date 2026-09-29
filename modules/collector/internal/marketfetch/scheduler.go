package marketfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultBatchSize = MaxRealtimeItems
	DefaultMaxPlan   = 1000
)

type scheduleState struct {
	target      time.Time
	fingerprint string
}

type plannedDispatch struct {
	req   Request
	node  scfinvoker.Node
	nodes []scfinvoker.Node
}

type taskExpansionCache struct {
	tags     map[string]*storagepb.Tag
	subjects map[string][]domain.Subject
}

func newTaskExpansionCache() *taskExpansionCache {
	return &taskExpansionCache{tags: make(map[string]*storagepb.Tag), subjects: make(map[string][]domain.Subject)}
}

func expansionCacheKey(spaceID string, tagIDs []string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.Join(normalizedTaskTagIDs(tagIDs), "\x00")
}

// Scheduler scans enabled tasks and creates stable SCF batches. Realtime work
// fans out across the available SCF fleet before the per-function item limit
// applies. It is intentionally a single process timer handler; SQLite unique
// indexes provide the only idempotency needed by this single-user system.
type Scheduler struct {
	SCFRegionBlacklists map[string][]string
	ResolveSymbol       SymbolResolver
	ResolveSourceID     func(string, string) string
	Tasks               *store.TaskRepository
	Instances           *store.TaskInstanceRepository
	Batches             *store.FetchBatchRepository
	Runs                *store.RunRepository
	Retries             *store.FetchRetryRepository
	Invoker             MarketFetchInvoker
	Storage             func(string, string, string) (Storage, error)
	StorageTarget       string
	// InvokeStorageTarget is sent in SCF invoke payloads. Leave empty to reuse
	// StorageTarget. Collector on a mainland host may talk to Storage over a
	// private IP while overseas functions still need the public native gateway.
	InvokeStorageTarget string
	BatchSize           int
	InvokeConcurrency   int
	MaxRetryAttempts    int
	Metrics             *Metrics
	SpaceID             string
	Symbols             datasetSource
	// InvokeNonRealtimeOnly keeps realtime K-lines out of the Invoke path when
	// Timer-triggered nodes own the live schedule.
	InvokeNonRealtimeOnly bool
	DNSCache              interface {
		Snapshot() map[string]sources.DNSResolution
	}
	Now         func() time.Time
	mu          sync.Mutex
	lastTaskID  string
	lastCleanup time.Time
	planStates  map[string]scheduleState
	invokeSem   chan struct{}
}

const (
	defaultBatchCompletionDeadline = 70 * time.Second
	defaultSCFInvokeAttemptTimeout = 10 * time.Second
)

// MarketFetchInvoker is the CloudNode list/invoke surface used by the scheduler.
type MarketFetchInvoker interface {
	ListMarketFetchers(ctx context.Context, spaceID string) ([]scfinvoker.Node, error)
	ListTimerMarketFetchers(ctx context.Context, spaceID string) ([]scfinvoker.Node, error)
	Invoke(ctx context.Context, spaceID, nodeID string, event map[string]any, invokeType cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error)
}

func batchCompletionDeadline(kind domain.BatchKind) time.Duration {
	_ = kind
	return defaultBatchCompletionDeadline
}

func (s *Scheduler) Tick(ctx context.Context, spaceID string) (err error) {
	if s == nil || s.Tasks == nil || s.Batches == nil || s.Invoker == nil {
		return fmt.Errorf("market fetch scheduler is not initialized")
	}
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	if s.invokeSem == nil {
		limit := s.InvokeConcurrency
		if limit <= 0 || limit > 20 {
			limit = 20
		}
		s.invokeSem = make(chan struct{}, limit)
	}
	if s.planStates == nil {
		s.planStates = make(map[string]scheduleState)
	}
	if strings.TrimSpace(spaceID) == "" {
		spaceID = strings.TrimSpace(s.SpaceID)
	}
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	if s.Instances != nil {
		if pruned, pruneErr := s.Instances.PruneDisabledWriteTargets(ctx, spaceID); pruneErr != nil {
			log.WarnContextf(ctx, "prune disabled collector write targets failed space=%s: %v", spaceID, pruneErr)
		} else if pruned > 0 {
			log.InfoContextf(ctx, "pruned disabled collector write targets space=%s count=%d", spaceID, pruned)
		}
	}
	if s.Runs != nil {
		if reconcileErr := s.Runs.ReconcileOpenRuns(ctx, spaceID); reconcileErr != nil {
			log.WarnContextf(ctx, "reconcile previous collector runs failed space=%s: %v", spaceID, reconcileErr)
		}
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	dnsRoutes := s.dnsSnapshot(ctx)
	allTasks, err := s.Tasks.ListEnabled(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list enabled collection tasks: %w", err)
	}
	// Refresh the single durable TaskSeries copy before establishing this
	// scheduler round's cutoff. If another scheduler already created the Run,
	// a changed series set receives c_mtime > run.CreateTime and is therefore
	// deferred to the next Run instead of being appended to this one.
	expansionCache := newTaskExpansionCache()
	for _, task := range filterMarketFetchTasks(allTasks) {
		if _, _, refreshErr := s.expandTaskWithCache(ctx, task, expansionCache); refreshErr != nil {
			// Metadata failover may finish after the scheduler budget has expired.
			// Invalidation must still fail closed, so give that small local write an
			// independent bounded context instead of reusing an already-cancelled one.
			invalidateCtx, invalidateCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			invalidateErr := s.Tasks.InvalidateTaskSeries(invalidateCtx, task.SpaceID, task.TaskID)
			invalidateCancel()
			if invalidateErr != nil {
				return fmt.Errorf("refresh task series task=%s: %v; invalidate after refresh failure: %w", task.TaskID, refreshErr, invalidateErr)
			}
			log.WarnContextf(ctx, "collection task series refresh failed task=%s; defer until a later run: %v", task.TaskID, refreshErr)
		}
	}
	allTasks, err = s.Tasks.ListEnabled(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("reload enabled collection tasks after series refresh: %w", err)
	}
	currentRunID := ""
	runCutoff := time.Now().UTC()
	if s.Runs != nil {
		runTime := now.Truncate(time.Minute)
		runKey := fmt.Sprintf("scheduled:%s", runTime.Format(time.RFC3339))
		run, runErr := s.Runs.GetOrCreateScheduled(ctx, spaceID, runKey, "scheduled", "1m", runTime)
		if runErr != nil {
			return fmt.Errorf("create collector run: %w", runErr)
		}
		currentRunID = run.RunID
		runCutoff = run.CreateTime.UTC()
		defer func() {
			finishCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err != nil {
				if updateErr := s.Runs.UpdateStatus(finishCtx, spaceID, currentRunID, domain.RunStatusFailed, err.Error()); updateErr != nil {
					log.WarnContextf(finishCtx, "mark collector run failed space=%s run=%s: %v", spaceID, currentRunID, updateErr)
				}
				return
			}
			if _, reconcileErr := s.Runs.Reconcile(finishCtx, spaceID, currentRunID); reconcileErr != nil {
				log.WarnContextf(finishCtx, "reconcile current collector run failed space=%s run=%s: %v", spaceID, currentRunID, reconcileErr)
			}
		}()
	}
	activeTaskIDs := make(map[string]struct{}, len(allTasks))
	for _, task := range allTasks {
		activeTaskIDs[task.TaskID] = struct{}{}
	}
	for key := range s.planStates {
		if taskID, _, ok := strings.Cut(key, "\x00"); ok {
			if _, exists := activeTaskIDs[taskID]; !exists {
				delete(s.planStates, key)
			}
		}
	}
	if _, exists := activeTaskIDs[s.lastTaskID]; !exists {
		s.lastTaskID = ""
	}
	// Local collector jobs (for example kline_resample) are driven by their
	// own timer workers. The market-fetch scheduler only owns cloud-invoked
	// collection tasks. Run membership is fixed by the immutable create-time
	// cutoff; later creates, re-enables, definition changes and series changes
	// are intentionally deferred to the next scheduled Run.
	tasks := filterTasksForRunCutoff(filterMarketFetchTasks(allTasks), runCutoff)
	requiresInvoke := !s.InvokeNonRealtimeOnly && len(tasks) > 0
	if s.InvokeNonRealtimeOnly {
		for _, task := range tasks {
			if !isKlineTask(task) {
				requiresInvoke = true
				break
			}
		}
	}
	tasks = rotateTasksAfter(tasks, s.lastTaskID)
	// Crypto and other Scheduler-owned tasks execute on Invoke nodes so their
	// asynchronous completion can return through EventBus. stockcn keeps the
	// Timer fleet for realtime K-lines and therefore still needs both catalogs.
	invokeNodes, err := s.Invoker.ListMarketFetchers(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list market fetcher nodes: %w", err)
	}
	timerNodes, err := s.Invoker.ListTimerMarketFetchers(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list timer market fetcher nodes: %w", err)
	}
	nodes := append(append([]scfinvoker.Node(nil), invokeNodes...), timerNodes...)
	nodes, _ = filterSCFRegions(nodes, spaceID, s.SCFRegionBlacklists)
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Region != nodes[j].Region {
			return nodes[i].Region < nodes[j].Region
		}
		return nodes[i].FunctionName < nodes[j].FunctionName
	})
	invokeNodes = filterNodesByTrigger(nodes, "invoke")
	timerNodes = filterNodesByTrigger(nodes, "timer")
	if len(invokeNodes) == 0 && requiresInvoke {
		return fmt.Errorf("no active Invoke market fetcher nodes")
	}
	if err := s.recoverDue(ctx, spaceID, invokeNodes, now); err != nil {
		log.WarnContextf(ctx, "recover market fetch batches failed: %v", err)
	}
	if err := s.dispatchDueRetries(ctx, spaceID, invokeNodes, now); err != nil {
		log.WarnContextf(ctx, "dispatch market fetch retries failed: %v", err)
	}
	// Planning is intentionally two-phase. Persist every task's destination
	// before the first batch is dispatched so a shared source request can fan
	// out to targets owned by tasks that appear later in the scheduler scan.
	// Without this pre-pass the result depended on goroutine timing: task A
	// could reach SCF before task B had attached its WriteTarget.
	if err := s.primeSharedWriteTargets(ctx, spaceID, currentRunID, runCutoff, now, tasks); err != nil {
		return err
	}
	planned := 0
	deferredDispatches := make([]plannedDispatch, 0, 256)
	// Do not let SCF completions contend with SQLite while this tick is still
	// persisting its batch plan. Preserve the old partial-plan semantics on
	// errors by launching every successfully persisted batch when Tick returns.
	defer func() {
		for _, dispatch := range deferredDispatches {
			go s.dispatchPlanned(dispatch.req, dispatch.node, dispatch.nodes)
		}
	}()
	// A fetch request is shared across CollectionTasks when its source-side
	// identity is identical.  The task-specific destination is recorded in
	// WriteTarget; only the first task gets an executable batch in this pass.
	sharedScheduled := make(map[string]struct{})
	for _, task := range tasks {
		if planned >= DefaultMaxPlan {
			break
		}
		matches, matchErr := s.taskIdentityMatchesRun(ctx, spaceID, task, runCutoff)
		if matchErr != nil {
			return matchErr
		}
		if !matches {
			return fmt.Errorf("collector run %s became stale before dispatch planning for task %s", currentRunID, task.TaskID)
		}
		items, frequencies, err := s.expandTaskForPlanning(ctx, task)
		if err != nil {
			log.WarnContextf(ctx, "skip invalid collection task=%s: %v", task.TaskID, err)
			continue
		}
		taskNodes := invokeNodes
		if s.InvokeNonRealtimeOnly && isKlineTask(task) {
			taskNodes = timerNodes
		}
		if len(taskNodes) == 0 {
			log.WarnContextf(ctx, "skip collection task=%s: no nodes for trigger type", task.TaskID)
			continue
		}
		for _, frequency := range frequencies {
			target, err := targetDataTime(now, frequency)
			if err != nil {
				log.WarnContextf(ctx, "skip task=%s frequency=%s: %v", task.TaskID, frequency, err)
				continue
			}
			frequencyRequestKeys := make([]string, 0, len(items))
			for index := range items {
				items[index] = prepareCollectionItemRequest(items[index], frequency, target, MaxRealtimeRows)
				frequencyRequestKeys = append(frequencyRequestKeys, sharedCollectionItemKey(items[index], frequency, target))
				items[index].InstanceID = collectionItemInstanceID(spaceID, currentRunID, task.TaskID, items[index], frequency, target)
			}
			stateKey := task.TaskID + "\x00" + frequency
			state := s.planStates[stateKey]
			frequencyFingerprint := taskFingerprint(frequencyRequestKeys, task.CollectParams)
			if s.InvokeNonRealtimeOnly && isKlineTask(task) {
				priorityItems := filterSharedCollectionItems(priorityCryptoMinuteItems(items, frequency), frequency, target, sharedScheduled)
				if len(priorityItems) == 0 {
					s.planStates[stateKey] = scheduleState{target: target, fingerprint: frequencyFingerprint}
					continue
				}
				priorityNodes := priorityNodesForItems(priorityItems, invokeNodes)
				if len(priorityNodes) == 0 {
					// Reuse timer-capable nodes when there is no dedicated invoke fleet,
					// but run them through the same egress filter. This keeps Binance
					// on overseas nodes instead of disabling priority collection merely
					// because the compatible node happens to be timer-classified.
					priorityNodes = priorityNodesForItems(priorityItems, taskNodes)
				}
				if err := s.dispatchPriorityCryptoMinute(ctx, spaceID, currentRunID, task, priorityItems, frequency, target, priorityNodes); err != nil {
					return err
				}
				// Keep the TaskInstance inventory while realtime K-line execution is
				// owned by Timer-triggered functions. Priority BTC/ETH still get a
				// one-item invoke so they are not starved inside a 40-subject timer.
				s.planStates[stateKey] = scheduleState{fingerprint: frequencyFingerprint}
				continue
			}
			if !state.target.IsZero() && state.target.Equal(target) && state.fingerprint == frequencyFingerprint {
				continue
			}
			// Do not dispatch an identical source request twice when another
			// CollectionTask already planned it during this scheduler pass. Its
			// WriteTarget was persisted above and will be fanned out by the writer.
			batchItems := filterSharedCollectionItems(items, frequency, target, sharedScheduled)
			if len(batchItems) == 0 {
				s.planStates[stateKey] = scheduleState{target: target, fingerprint: frequencyFingerprint}
				continue
			}
			sort.SliceStable(batchItems, func(i, j int) bool {
				left, right := collectionRouteKey(batchItems[i]), collectionRouteKey(batchItems[j])
				if left != right {
					return left < right
				}
				return batchItems[i].SubjectID < batchItems[j].SubjectID
			})
			batchSize := s.realtimeBatchSize(len(batchItems), taskNodes)
			for start, shard := 0, 0; start < len(batchItems); shard++ {
				end := start + batchSize
				if end > len(batchItems) {
					end = len(batchItems)
				}
				// A single SCF request must have one provider/source/market
				// identity. Tag expansion can produce multiple identities in one
				// task, so never let a shard straddle two groups.
				groupKey := collectionRouteKey(batchItems[start])
				for end > start+1 && collectionRouteKey(batchItems[end-1]) != groupKey {
					end--
				}
				// planned is the global batch cursor. Do not add shard again: the
				// loop already increments planned after every shard, and adding it
				// would skip every other node when the fleet size is even.
				node := taskNodes[planned%len(taskNodes)]
				scheduleID := fmt.Sprintf("%s:%s:%s", task.TaskID, frequency, target.Format(time.RFC3339Nano))
				batchKind := batchKindForTask(task)
				// Batch identity is scoped to the CollectorRun. A scheduler restart may
				// legitimately revisit the same closed higher-frequency target once,
				// but its completion must never be shared with TaskInstances from a
				// different Run.
				batchID := stableID(spaceID, currentRunID, scheduleID, string(batchKind), fmt.Sprintf("%d", shard), "1")
				syncPointID := stableID(spaceID, currentRunID, scheduleID, string(batchKind), fmt.Sprintf("%d", shard), "write")
				// The expanded item is the normalized request identity. Provider and
				// market routing come from the bound Tag, never from CollectionTask.
				batchProvider, batchMarketType := normalizedBatchIdentity(batchItems[start])
				req := Request{BatchID: batchID, SyncPointID: syncPointID, ScheduleID: scheduleID, BatchKind: batchKind, ShardIndex: shard, SpaceID: spaceID, MarketID: batchItems[start].MarketID, InstrumentType: batchItems[start].InstrumentType, DatasetID: batchItems[start].DatasetID, Frequency: frequency, Provider: batchProvider, SourceID: batchItems[start].SourceID, MarketType: batchMarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: dnsRoutes, Items: batchItems[start:end]}
				// planOne loads all enabled WriteTargets for this shard in one query
				// immediately before the durable batch race barrier. Do not pre-load
				// them one instance at a time here: that N+1 query is redundant and
				// can consume the entire scheduler budget for full-market runs.
				created, err := s.planOneDeferred(ctx, task, &req, node)
				if err != nil {
					return err
				}
				if created {
					planned++
					deferredDispatches = append(deferredDispatches, plannedDispatch{req: req, node: node, nodes: taskNodes})
				}
				start = end
				if planned >= DefaultMaxPlan {
					break
				}
			}
			s.planStates[stateKey] = scheduleState{target: target, fingerprint: frequencyFingerprint}
			if planned >= DefaultMaxPlan {
				break
			}
		}
		s.lastTaskID = task.TaskID
	}
	if s.Retries != nil && (s.lastCleanup.IsZero() || now.Sub(s.lastCleanup) >= time.Hour) {
		s.lastCleanup = now
		if err := s.Batches.Cleanup(ctx, now.Add(-48*time.Hour), now.Add(-7*24*time.Hour)); err != nil {
			log.WarnContextf(ctx, "market fetch batch cleanup failed: %v", err)
		} else if err := s.Retries.Cleanup(ctx, now.Add(-7*24*time.Hour)); err != nil {
			log.WarnContextf(ctx, "market fetch retry cleanup failed: %v", err)
		}
	}
	return nil
}

func (s *Scheduler) primeSharedWriteTargets(ctx context.Context, spaceID, runID string, runCutoff, now time.Time, tasks []domain.CollectionTask) error {
	if s == nil || s.Instances == nil {
		return nil
	}
	for _, task := range tasks {
		matches, err := s.taskIdentityMatchesRun(ctx, spaceID, task, runCutoff)
		if err != nil {
			return fmt.Errorf("check collection task before shared planning: %w", err)
		}
		if !matches {
			return fmt.Errorf("collector run %s became stale before planning task %s", runID, task.TaskID)
		}
		items, frequencies, err := s.expandTaskForPlanning(ctx, task)
		if err != nil {
			// Keep the existing scheduler contract: one malformed task must not
			// block unrelated tasks in the same run.
			log.WarnContextf(ctx, "skip invalid collection task during shared planning task=%s: %v", task.TaskID, err)
			continue
		}
		params, _ := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
		outputFields := []string(nil)
		if params != nil {
			outputFields = append(outputFields, params.OutputFields...)
		}
		for _, frequency := range frequencies {
			target, targetErr := targetDataTime(now, frequency)
			if targetErr != nil {
				log.WarnContextf(ctx, "skip task=%s frequency=%s during shared planning: %v", task.TaskID, frequency, targetErr)
				continue
			}
			stateKey := task.TaskID + "\x00" + frequency
			frequencyFingerprint := taskScheduleFingerprint(items, frequency, target, task.CollectParams)
			state := s.planStates[stateKey]
			if !state.target.IsZero() && state.target.Equal(target) && state.fingerprint == frequencyFingerprint {
				continue
			}
			matches, matchErr := s.taskIdentityMatchesRun(ctx, spaceID, task, runCutoff)
			if matchErr != nil {
				return matchErr
			}
			if !matches {
				return fmt.Errorf("collector run %s became stale before period initialization for task %s", runID, task.TaskID)
			}
			if err := s.ensureDatasetPeriod(ctx, task, items, frequency, target, now); err != nil {
				return fmt.Errorf("ensure dataset period task=%s frequency=%s: %w", task.TaskID, frequency, err)
			}
			instances := make([]domain.TaskInstance, 0, len(items))
			targets := make([]domain.WriteTarget, 0, len(items))
			for _, item := range items {
				item = prepareCollectionItemRequest(item, frequency, target, MaxRealtimeRows)
				instanceID := collectionItemInstanceID(spaceID, runID, task.TaskID, item, frequency, target)
				targetTime := target.UTC()
				instances = append(instances, domain.TaskInstance{
					SpaceID: spaceID, InstanceID: instanceID, RunID: runID,
					RequestKey: sharedCollectionItemKey(item, frequency, target),
					Provider:   item.Provider, ProviderSymbol: item.Symbol, SourceID: item.SourceID, MarketType: item.MarketType,
					SeriesTag: collectionItemSeriesTag(spaceID, item),
					DataType:  item.DataType, SubjectID: item.SubjectID,
					Frequency: frequency, TargetDataTime: &targetTime, TaskParams: collectionItemRequestParams(item),
				})
				targets = append(targets, domain.WriteTarget{
					ID:            stableID(spaceID, instanceID, task.TaskID, item.DatasetID),
					SpaceID:       spaceID,
					InstanceID:    instanceID,
					TaskID:        task.TaskID,
					DatasetID:     item.DatasetID,
					ViewID:        task.ResultViewID,
					OutputFields:  stringSliceJSON(outputFields),
					SeriesIndex:   item.SeriesIndex,
					SeriesHash:    item.SeriesHash,
					ExpectedCount: item.ExpectedCount,
					Status:        "pending",
				})
			}
			if err := s.Instances.UpsertMany(ctx, instances); err != nil {
				return fmt.Errorf("persist shared collection instances task=%s frequency=%s: %w", task.TaskID, frequency, err)
			}
			if err := s.Instances.UpsertWriteTargets(ctx, targets); err != nil {
				return fmt.Errorf("persist shared write targets task=%s frequency=%s: %w", task.TaskID, frequency, err)
			}
		}
	}
	return nil
}

func (s *Scheduler) ensureDatasetPeriod(ctx context.Context, task domain.CollectionTask, items []domain.CollectionItem, frequency string, period, now time.Time) error {
	if len(items) == 0 || strings.TrimSpace(items[0].SeriesHash) == "" || items[0].ExpectedCount == 0 {
		return fmt.Errorf("task %s has no materialized series expectation", task.TaskID)
	}
	if s.Storage == nil || strings.TrimSpace(s.StorageTarget) == "" {
		// Lightweight scheduler unit tests may not wire Storage. Production
		// bootstrap always provides it and integration tests cover this boundary.
		return nil
	}
	client, err := s.Storage(s.StorageTarget, items[0].MarketType, "collector")
	if err != nil {
		return err
	}
	periodClient, ok := client.(periodStorage)
	if !ok {
		return fmt.Errorf("storage client does not support dataset period commits")
	}
	deadline := now.UTC().Add(2 * batchCompletionDeadline(batchKindForTask(task)))
	return periodClient.EnsureDatasetPeriod(ctx, &storagepb.DatasetPeriodExpectation{
		SpaceId: task.SpaceID, DatasetId: items[0].DatasetID, Frequency: strings.ToLower(strings.TrimSpace(frequency)),
		PeriodTime: period.UTC().Unix(), SeriesHash: items[0].SeriesHash, ExpectedCount: items[0].ExpectedCount, DeadlineAt: deadline.Unix(),
	})
}

func filterTasksForRunCutoff(tasks []domain.CollectionTask, cutoff time.Time) []domain.CollectionTask {
	if cutoff.IsZero() {
		return append([]domain.CollectionTask(nil), tasks...)
	}
	cutoff = cutoff.UTC()
	filtered := make([]domain.CollectionTask, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.SeriesHash) == "" {
			continue
		}
		if !task.CreateTime.IsZero() && task.CreateTime.UTC().After(cutoff) {
			continue
		}
		if !task.ModifyTime.IsZero() && task.ModifyTime.UTC().After(cutoff) {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func (s *Scheduler) taskIdentityMatchesRun(ctx context.Context, spaceID string, snapshot domain.CollectionTask, cutoff time.Time) (bool, error) {
	if s == nil || s.Tasks == nil {
		return true, nil
	}
	current, err := s.Tasks.GetByTaskID(ctx, spaceID, snapshot.TaskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if !current.Enabled || strings.TrimSpace(current.SeriesHash) == "" {
		return false, nil
	}
	if !cutoff.IsZero() {
		if (!current.CreateTime.IsZero() && current.CreateTime.UTC().After(cutoff.UTC())) ||
			(!current.ModifyTime.IsZero() && current.ModifyTime.UTC().After(cutoff.UTC())) {
			return false, nil
		}
	}
	return strings.TrimSpace(current.DefinitionHash) == strings.TrimSpace(snapshot.DefinitionHash) &&
		strings.TrimSpace(current.SeriesHash) == strings.TrimSpace(snapshot.SeriesHash), nil
}

func filterMarketFetchTasks(tasks []domain.CollectionTask) []domain.CollectionTask {
	filtered := make([]domain.CollectionTask, 0, len(tasks))
	for _, task := range tasks {
		dataType := strings.ToLower(strings.TrimSpace(task.DataType))
		if dataType == "" {
			params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
			if err == nil {
				dataType = strings.ToLower(strings.TrimSpace(params.Collector.DataType))
			}
		}
		if dataType == "kline_resample" {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func filterSCFRegions(nodes []scfinvoker.Node, spaceID string, blacklists map[string][]string) (allowed, blocked []scfinvoker.Node) {
	regions := make(map[string]struct{})
	for space, values := range blacklists {
		if !strings.EqualFold(strings.TrimSpace(space), strings.TrimSpace(spaceID)) {
			continue
		}
		for _, region := range values {
			if region = strings.ToLower(strings.TrimSpace(region)); region != "" {
				regions[region] = struct{}{}
			}
		}
	}
	for _, node := range nodes {
		if _, excluded := regions[strings.ToLower(strings.TrimSpace(node.Region))]; excluded {
			blocked = append(blocked, node)
		} else {
			allowed = append(allowed, node)
		}
	}
	return allowed, blocked
}

func filterNodesByTrigger(nodes []scfinvoker.Node, trigger string) []scfinvoker.Node {
	filtered := make([]scfinvoker.Node, 0, len(nodes))
	for _, node := range nodes {
		if strings.EqualFold(strings.TrimSpace(node.TriggerType), trigger) {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func isKlineTask(task domain.CollectionTask) bool {
	dataType := strings.ToLower(strings.TrimSpace(task.DataType))
	if dataType != "" {
		return dataType == "kline"
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	return err == nil && strings.EqualFold(strings.TrimSpace(params.Collector.DataType), "kline")
}

type dnsRefreshable interface {
	Due(time.Time) bool
	Refresh(context.Context) error
}

func (s *Scheduler) dnsSnapshot(ctx context.Context) map[string]sources.DNSResolution {
	if s == nil || s.DNSCache == nil {
		return nil
	}
	snapshot := s.DNSCache.Snapshot()
	// DNS refresh and market scheduling are independent timer services. At the
	// TTL boundary the scheduler can otherwise observe an empty snapshot just
	// before the refresh timer runs and persist an invocation without fapi/api
	// routes. Refresh once when the snapshot is empty and the coordinator is due;
	// the SCF still has hostname fallback if this bounded refresh fails.
	if len(snapshot) == 0 {
		if refreshable, ok := s.DNSCache.(dnsRefreshable); ok && refreshable.Due(time.Now().UTC()) {
			if err := refreshable.Refresh(ctx); err != nil {
				log.WarnContextf(ctx, "market_fetch_dns_refresh_before_schedule_failed error=%v", err)
			}
			snapshot = s.DNSCache.Snapshot()
		}
	}
	return snapshot
}

func collectionItemSeriesTag(spaceID string, item domain.CollectionItem) string {
	if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
		return "default"
	}
	if strings.TrimSpace(item.Provider) == "" && strings.TrimSpace(item.SourceID) == "" {
		return ""
	}
	return defaultMarketSeriesTag(item.Provider, item.SourceID, item.MarketType)
}

func normalizedBatchIdentity(item domain.CollectionItem) (string, string) {
	return strings.ToLower(strings.TrimSpace(item.Provider)), strings.ToLower(strings.TrimSpace(item.MarketType))
}

func taskScheduleFingerprint(items []domain.CollectionItem, frequency string, target time.Time, params string) string {
	requestKeys := make([]string, 0, len(items))
	for _, item := range items {
		item = prepareCollectionItemRequest(item, frequency, target, MaxRealtimeRows)
		requestKeys = append(requestKeys, sharedCollectionItemKey(item, frequency, target))
	}
	return taskFingerprint(requestKeys, params)
}

func taskFingerprint(instanceIDs []string, params string) string {
	ids := append([]string(nil), instanceIDs...)
	sort.Strings(ids)
	h := sha256.New()
	for _, taskID := range ids {
		_, _ = h.Write([]byte(taskID))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte(params))
	return hex.EncodeToString(h.Sum(nil))
}

// The public stock endpoints currently expose only a bounded latest page and
// have no safe cursor shared by all active providers. Reject older history at
// planning time instead of repeatedly requesting a page that cannot cover the
// requested start. A future cursor-paginated feed can raise this deliberately.
const stockCNHistoryMaxLookback = 24 * time.Hour

func prepareCollectionItemRequest(item domain.CollectionItem, frequency string, target time.Time, barLimit int) domain.CollectionItem {
	item.TargetDataTime = target.UTC().Format(time.RFC3339Nano)
	item.Frequency = strings.TrimSpace(frequency)
	if barLimit > 0 {
		item.BarLimit = barLimit
	}
	return item
}

func collectionItemRequestParams(item domain.CollectionItem) string {
	params := map[string]any{}
	putString := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			params[key] = value
		}
	}
	putString("market_id", item.MarketID)
	putString("instrument_type", item.InstrumentType)
	putString("start_time", item.StartTime)
	putString("end_time", item.EndTime)
	putString("snapshot_at", item.SnapshotAt)
	if item.BarLimit != 0 {
		params["bar_limit"] = item.BarLimit
	}
	if item.Canary {
		params["canary"] = true
	}
	if item.SnapshotShardIndex != 0 || item.SnapshotShardCount != 0 {
		params["snapshot_shard_index"] = item.SnapshotShardIndex
		params["snapshot_shard_count"] = item.SnapshotShardCount
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func collectionItemInstanceID(spaceID, runID, collectionTaskID string, item domain.CollectionItem, frequency string, target time.Time) string {
	// A market-fetch TaskInstance is one execution of one semantic request in
	// one CollectorRun. The CollectionTask/Dataset are deliberately excluded;
	// they are represented by WriteTargets.
	_ = collectionTaskID
	requestKey := sharedCollectionItemKey(item, frequency, target)
	if strings.TrimSpace(runID) != "" {
		return stableID(spaceID, runID, requestKey)
	}
	// Tests and maintenance callers may run without a Run repository. Keep a
	// deterministic fallback while production scheduling always supplies runID.
	return stableID(spaceID, requestKey)
}

func sharedCollectionItemKey(item domain.CollectionItem, frequency string, target time.Time) string {
	// request_key identifies the Provider request semantics only. Target task,
	// Dataset/View and output fields are deliberately excluded because they are
	// represented by WriteTarget and must not prevent fetch sharing. Fields that
	// can change the upstream response must be included. Retry-attempt state
	// (candidate_index/source_event_id) is excluded so retries stay on the same
	// logical TaskInstance.
	parts := []string{
		item.Provider,
		item.SourceID,
		item.MarketID,
		item.InstrumentType,
		item.MarketType,
		item.DataType,
		item.SubjectID,
		item.Symbol,
		frequency,
		target.UTC().Format(time.RFC3339Nano),
		item.StartTime,
		item.EndTime,
		item.SnapshotAt,
		strconv.Itoa(item.BarLimit),
		strconv.FormatBool(item.Canary),
		strconv.Itoa(item.SnapshotShardIndex),
		strconv.Itoa(item.SnapshotShardCount),
	}
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func collectionRouteKey(item domain.CollectionItem) string {
	return strings.Join([]string{strings.ToLower(strings.TrimSpace(item.Provider)), strings.ToLower(strings.TrimSpace(item.SourceID)), strings.ToLower(strings.TrimSpace(item.MarketType)), strings.ToLower(strings.TrimSpace(item.DataType))}, "\x00")
}

func filterSharedCollectionItems(items []domain.CollectionItem, frequency string, target time.Time, seen map[string]struct{}) []domain.CollectionItem {
	unique := make([]domain.CollectionItem, 0, len(items))
	for _, item := range items {
		key := sharedCollectionItemKey(item, frequency, target)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, item)
	}
	return unique
}

func stringSliceJSON(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func priorityCryptoMinuteItems(items []domain.CollectionItem, frequency string) []domain.CollectionItem {
	if !strings.EqualFold(strings.TrimSpace(frequency), "1m") {
		return nil
	}
	priority := make(map[string]struct{}, len(priorityCryptoSubjects))
	for _, subject := range priorityCryptoSubjects {
		priority[subject] = struct{}{}
	}
	selected := make([]domain.CollectionItem, 0, len(priorityCryptoSubjects))
	for _, item := range items {
		if _, ok := priority[strings.ToUpper(strings.TrimSpace(item.SubjectID))]; !ok {
			continue
		}
		selected = append(selected, item)
	}
	return selected
}

func itemsRequireOverseasEgress(items []domain.CollectionItem) bool {
	for _, item := range items {
		if requiresOverseasEgress(TaskGroup{Provider: item.Provider, MarketType: item.MarketType, DatasetID: item.DatasetID}) {
			return true
		}
	}
	return false
}

func priorityNodesForItems(items []domain.CollectionItem, nodes []scfinvoker.Node) []scfinvoker.Node {
	if !itemsRequireOverseasEgress(items) {
		return nodes
	}
	overseas := make([]scfinvoker.Node, 0, len(nodes))
	for _, node := range nodes {
		if isOverseasSCFRegion(node.Region) {
			overseas = append(overseas, node)
		}
	}
	return overseas
}

func (s *Scheduler) dispatchPriorityCryptoMinute(ctx context.Context, spaceID, runID string, task domain.CollectionTask, items []domain.CollectionItem, frequency string, target time.Time, nodes []scfinvoker.Node) error {
	if s == nil || s.Batches == nil || len(nodes) == 0 {
		return nil
	}
	selected := priorityCryptoMinuteItems(items, frequency)
	if len(selected) == 0 {
		return nil
	}
	dnsRoutes := s.dnsSnapshot(ctx)
	for index, item := range selected {
		// Limit 1 is usually the still-open minute at :00, which Binance then
		// rejects as "no closed bar". Two bars keep the last closed minute
		// without dumping a 10-bar history page into Merge. Request identity is
		// computed only after this response-affecting value is fixed.
		item = prepareCollectionItemRequest(item, frequency, target, 2)
		item.InstanceID = collectionItemInstanceID(spaceID, runID, task.TaskID, item, frequency, target)
		if s.Instances != nil {
			targetTime := target.UTC()
			if err := s.Instances.UpsertMany(ctx, []domain.TaskInstance{{
				SpaceID: spaceID, InstanceID: item.InstanceID, RunID: runID, RequestKey: sharedCollectionItemKey(item, frequency, target),
				Provider: item.Provider, ProviderSymbol: item.Symbol, SourceID: item.SourceID, MarketType: item.MarketType, SeriesTag: collectionItemSeriesTag(spaceID, item),
				DataType: item.DataType, SubjectID: item.SubjectID, Frequency: frequency, TargetDataTime: &targetTime, TaskParams: collectionItemRequestParams(item),
			}}); err != nil {
				return err
			}
			if err := s.Instances.UpsertWriteTargets(ctx, []domain.WriteTarget{{
				ID: stableID(spaceID, item.InstanceID, task.TaskID, item.DatasetID), SpaceID: spaceID, InstanceID: item.InstanceID, TaskID: task.TaskID,
				DatasetID: item.DatasetID, ViewID: task.ResultViewID, OutputFields: stringSliceJSON(item.OutputFields), SeriesIndex: item.SeriesIndex, SeriesHash: item.SeriesHash, ExpectedCount: item.ExpectedCount, Status: "pending",
			}}); err != nil {
				return err
			}
		}
		node := nodes[index%len(nodes)]
		scheduleID := fmt.Sprintf("%s:%s:%s:priority:%s", task.TaskID, frequency, target.Format(time.RFC3339Nano), item.SubjectID)
		batchKind := batchKindForTask(task)
		batchID := stableID(spaceID, scheduleID, string(batchKind), item.DatasetID, "1")
		syncPointID := stableID(spaceID, scheduleID, string(batchKind), item.DatasetID, "write")
		batchProvider, batchMarketType := normalizedBatchIdentity(item)
		req := Request{
			BatchID: batchID, SyncPointID: syncPointID, ScheduleID: scheduleID, BatchKind: batchKind, ShardIndex: 0,
			SpaceID: spaceID, MarketID: item.MarketID, InstrumentType: item.InstrumentType, DatasetID: item.DatasetID,
			Frequency: frequency, Provider: batchProvider, SourceID: item.SourceID, MarketType: batchMarketType,
			Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: dnsRoutes,
			Items: []domain.CollectionItem{item},
		}
		if created, err := s.planOne(ctx, task, req, node, nodes); err != nil {
			return err
		} else if created {
			log.InfoContextf(ctx, "priority_crypto_minute_planned subject=%s dataset=%s frequency=%s target=%s node=%s", item.SubjectID, item.DatasetID, frequency, target.UTC().Format(time.RFC3339), node.FunctionName)
		}
	}
	return nil
}

func (s *Scheduler) planOne(ctx context.Context, task domain.CollectionTask, req Request, node scfinvoker.Node, nodes []scfinvoker.Node) (bool, error) {
	created, err := s.planOneDeferred(ctx, task, &req, node)
	if err != nil || !created {
		return created, err
	}
	go s.dispatchPlanned(req, node, nodes)
	return true, nil
}

func (s *Scheduler) planOneDeferred(ctx context.Context, task domain.CollectionTask, req *Request, node scfinvoker.Node) (bool, error) {
	if req == nil {
		return false, errors.New("market fetch request is nil")
	}
	instanceIDs := make([]string, 0, len(req.Items))
	for _, item := range req.Items {
		if id := strings.TrimSpace(item.InstanceID); id != "" {
			instanceIDs = append(instanceIDs, id)
		}
	}
	if s.Instances != nil {
		targets, err := s.Instances.ListEnabledWriteTargetsForInstances(ctx, req.SpaceID, instanceIDs)
		if err != nil {
			return false, fmt.Errorf("load enabled write targets before planning: %w", err)
		}
		if len(targets) == 0 {
			return false, nil
		}
		req.Targets = targets
	} else {
		enabled, err := s.taskEnabled(ctx, req.SpaceID, task.TaskID)
		if err != nil {
			return false, fmt.Errorf("check collection task before planning: %w", err)
		}
		if !enabled {
			return false, nil
		}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	if _, err := marketFetchEvent(*req, s.eventStorageTarget()); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	batch := &domain.BatchInvocation{SpaceID: req.SpaceID, BatchID: req.BatchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: req.ShardIndex, Frequency: req.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: len(req.Items), PlannedAt: &now, DeadlineAt: timePtr(now.Add(batchCompletionDeadline(req.BatchKind)))}
	var created bool
	if s.Instances != nil {
		created, err = s.Batches.CreatePlannedWithItemsForEnabledTargets(ctx, batch, instanceIDs)
	} else {
		created, err = s.Batches.CreatePlanned(ctx, batch)
	}
	if err != nil {
		return false, err
	}
	if !created {
		return false, nil
	}
	return true, nil
}

func rotateTasksAfter(tasks []domain.CollectionTask, lastTaskID string) []domain.CollectionTask {
	if len(tasks) < 2 || strings.TrimSpace(lastTaskID) == "" {
		return tasks
	}
	for index, task := range tasks {
		if task.TaskID == lastTaskID {
			return append(append([]domain.CollectionTask(nil), tasks[index+1:]...), tasks[:index+1]...)
		}
	}
	return tasks
}

func (s *Scheduler) dispatchPlanned(req Request, node scfinvoker.Node, nodes []scfinvoker.Node) {
	if s.invokeSem == nil {
		s.invokeSem = make(chan struct{}, 20)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case s.invokeSem <- struct{}{}:
		defer func() { <-s.invokeSem }()
	case <-ctx.Done():
		return
	}
	if batch, err := s.Batches.Get(ctx, req.SpaceID, req.BatchID); err != nil || batch.Status != domain.BatchStatusPlanned {
		return
	}
	for attempt, candidate := range invocationCandidates(node, nodes) {
		if s.Instances != nil {
			instanceIDs := make([]string, 0, len(req.Items))
			for _, item := range req.Items {
				instanceIDs = append(instanceIDs, item.InstanceID)
			}
			targets, err := s.Instances.ListEnabledWriteTargetsForInstances(ctx, req.SpaceID, instanceIDs)
			if err != nil || len(targets) == 0 {
				return
			}
			req.Targets = targets
		}
		event, err := marketFetchEvent(requestForNode(req, candidate), s.eventStorageTarget())
		if err != nil {
			log.WarnContextf(ctx, "build SCF market fetch failover event failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
			return
		}
		invokeCtx, invokeCancel := context.WithTimeout(ctx, defaultSCFInvokeAttemptTimeout)
		result, invokeErr := s.Invoker.Invoke(invokeCtx, req.SpaceID, candidate.NodeID, event, cloudnodepb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT)
		invokeCancel()
		if invokeErr != nil {
			if attempt == 0 {
				log.WarnContextf(ctx, "SCF market fetch invoke failed; trying failover batch=%s from_node=%s err=%v", req.BatchID, candidate.NodeID, invokeErr)
			} else {
				log.WarnContextf(ctx, "SCF market fetch failover invoke failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, invokeErr)
			}
			continue
		}
		if _, err := s.Batches.MarkDispatchedToNode(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(batchCompletionDeadline(req.BatchKind)), candidate.Region, candidate.NodeID, candidate.FunctionName); err != nil {
			log.WarnContextf(ctx, "mark market fetch batch dispatched failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
		}
		if attempt > 0 {
			log.InfoContextf(ctx, "SCF market fetch failover succeeded batch=%s node=%s", req.BatchID, candidate.NodeID)
		}
		return
	}
	log.WarnContextf(ctx, "SCF market fetch invoke exhausted failover batch=%s original_node=%s", req.BatchID, node.NodeID)
}

func (s *Scheduler) taskEnabled(ctx context.Context, spaceID, taskID string) (bool, error) {
	if s == nil || s.Tasks == nil {
		return false, errors.New("collection task repository is not initialized")
	}
	task, err := s.Tasks.GetByTaskID(ctx, spaceID, taskID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return task != nil && task.Enabled, nil
}

// realtimeBatchSize makes one minute's work fan out to the current SCF fleet
// first, then applies the smallest per-function limit declared by that fleet.
// This keeps each node at one invocation for the common case while preventing
// a stale or deliberately smaller function configuration from receiving an
// oversized event.
func (s *Scheduler) realtimeBatchSize(itemCount int, nodes []scfinvoker.Node) int {
	if itemCount <= 0 || len(nodes) == 0 {
		return 1
	}
	limit := DefaultBatchSize
	if s.BatchSize > 0 && s.BatchSize < limit {
		limit = s.BatchSize
	}
	for _, node := range nodes {
		if configured, ok := nodeMetadataInt(node.Metadata, "realtime_batch_size"); ok && configured < limit {
			limit = configured
		}
	}
	if limit <= 0 {
		limit = 1
	}
	perNode := (itemCount + len(nodes) - 1) / len(nodes)
	if perNode < limit {
		return perNode
	}
	return limit
}

func nodeMetadataInt(metadata map[string]any, key string) (int, bool) {
	if metadata == nil {
		return 0, false
	}
	switch value := metadata[key].(type) {
	case int:
		return value, value > 0
	case int32:
		return int(value), value > 0
	case int64:
		return int(value), value > 0
	case float64:
		integer := int(value)
		return integer, value == float64(integer) && integer > 0
	case string:
		integer, err := strconv.Atoi(strings.TrimSpace(value))
		return integer, err == nil && integer > 0
	default:
		return 0, false
	}
}

func (s *Scheduler) recoverDue(ctx context.Context, spaceID string, nodes []scfinvoker.Node, now time.Time) error {
	if s.Retries == nil || len(nodes) == 0 {
		return nil
	}
	due, err := s.Batches.ListDue(ctx, spaceID, now, 100)
	if err != nil {
		return err
	}
	for _, batch := range due {
		batch.Status = domain.BatchStatusTimedOut
		batch.CompletedAt = &now
		batch.ErrorSummary = "SCF completion event deadline exceeded"
		var request Request
		if err := json.Unmarshal([]byte(batch.RequestJSON), &request); err != nil {
			continue
		}
		logicalSyncPointID := strings.TrimSpace(request.SyncPointID)
		if logicalSyncPointID == "" {
			logicalSyncPointID = batch.BatchID
		}
		effects := store.FetchCompletionEffects{}
		for _, item := range request.Items {
			raw, _ := json.Marshal(item)
			target, _ := time.Parse(time.RFC3339Nano, item.TargetDataTime)
			if target.IsZero() {
				target = now
			}
			next := now.Add(5 * time.Second)
			key := item.SourceEventID
			if key == "" {
				key = retryKey(batch.BatchID, item.SubjectID, item.TargetDataTime)
			}
			effects.Retries = append(effects.Retries, &domain.RetryItem{SpaceID: spaceID, RetryKey: key, SourceBatchID: logicalSyncPointID, BatchKind: batch.BatchKind, InstanceID: item.InstanceID, RetryScope: "fetch", SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: target, TaskJSON: string(raw), Attempt: batch.Attempt, Status: "pending", NextRetryAt: &next, LastErrorType: "scf_completion_timeout", LastErrorSummary: "SCF completion event deadline exceeded", CreateTime: now, ModifyTime: now})
		}
		updated, err := s.Batches.CompleteWithEffects(ctx, &batch, effects)
		if err != nil {
			return err
		}
		if updated && s.Metrics != nil {
			duration := int64(0)
			if batch.DispatchedAt != nil && !batch.DispatchedAt.IsZero() {
				duration = now.Sub(batch.DispatchedAt.UTC()).Milliseconds()
			} else if batch.PlannedAt != nil && !batch.PlannedAt.IsZero() {
				duration = now.Sub(batch.PlannedAt.UTC()).Milliseconds()
			}
			s.Metrics.Observe(spaceID, &marketfetchpb.MarketFetchBatchCompleted{
				BatchId: batch.BatchID, DatasetId: request.DatasetID, Frequency: batch.Frequency,
				Status: "timed_out", DurationMs: duration, CompletedAt: timestamppb.New(now.UTC()),
			})
			if count, countErr := s.Retries.CountPending(ctx, spaceID, request.DatasetID, batch.Frequency); countErr == nil {
				s.Metrics.SetRetryPending(spaceID, request.DatasetID, batch.Frequency, int(count))
			}
		}
	}
	return nil
}

func (s *Scheduler) dispatchDueRetries(ctx context.Context, spaceID string, nodes []scfinvoker.Node, now time.Time) error {
	if s.Retries == nil || len(nodes) == 0 {
		return nil
	}
	items, err := s.Retries.ListDue(ctx, spaceID, now, 20)
	if err != nil {
		return err
	}
	for index, retry := range items {
		maxAttempts := s.MaxRetryAttempts
		if maxAttempts <= 0 {
			maxAttempts = envInt("MOOX_FETCH_MAX_RETRY_ATTEMPTS", 3)
		}
		if retry.Attempt > maxAttempts {
			if err := s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed"); err != nil {
				return err
			}
			continue
		}
		var item domain.CollectionItem
		if err := json.Unmarshal([]byte(retry.TaskJSON), &item); err != nil {
			_ = s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed")
			continue
		}
		item.SourceEventID = retry.RetryKey
		instanceID := strings.TrimSpace(retry.InstanceID)
		if instanceID == "" {
			instanceID = strings.TrimSpace(item.InstanceID)
		}
		if instanceID == "" || s.Instances == nil {
			_ = s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed")
			continue
		}
		item.InstanceID = instanceID
		targets, targetErr := s.Instances.ListEnabledWriteTargetsForInstances(ctx, spaceID, []string{instanceID})
		if targetErr != nil {
			return targetErr
		}
		if retry.RetryScope == "write_target" {
			filtered := targets[:0]
			for _, target := range targets {
				if target.ID == retry.WriteTargetID {
					filtered = append(filtered, target)
				}
			}
			targets = filtered
		}
		if len(targets) == 0 {
			_ = s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed")
			continue
		}
		node := nodes[index%len(nodes)]
		batchID := stableID(spaceID, "retry", retry.RetryKey, fmt.Sprintf("%d", retry.Attempt+1))
		batchKind := retry.BatchKind
		if batchKind == "" {
			batchKind = domain.BatchKindRealtime
		}
		req := Request{BatchID: batchID, SyncPointID: retry.SourceBatchID, ScheduleID: "retry:" + retry.RetryKey, BatchKind: batchKind, SpaceID: spaceID, DatasetID: targets[0].DatasetID, Frequency: item.Frequency, Provider: item.Provider, SourceID: item.SourceID, MarketType: item.MarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: s.dnsSnapshot(ctx), Items: []domain.CollectionItem{item}, Targets: targets}
		raw, _ := json.Marshal(req)
		if _, err := marketFetchEvent(req, s.eventStorageTarget()); err != nil {
			_ = s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed")
			continue
		}
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: batchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: index, InstanceID: instanceID, WriteTargetID: retry.WriteTargetID, RetryScope: retry.RetryScope, Frequency: item.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: retry.Attempt + 1, RequestJSON: string(raw), PlannedCount: 1, PlannedAt: &now, DeadlineAt: timePtr(now.Add(batchCompletionDeadline(req.BatchKind)))}
		created, err := s.Batches.CreatePlannedWithItemsForEnabledTargets(ctx, batch, []string{instanceID})
		if err != nil {
			return err
		}
		if !created {
			continue
		}
		go s.dispatchRetry(req, node, nodes, retry.RetryKey)
	}
	return nil
}

func (s *Scheduler) dispatchRetry(req Request, node scfinvoker.Node, nodes []scfinvoker.Node, retryKey string) {
	if s.invokeSem == nil {
		s.invokeSem = make(chan struct{}, 20)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case s.invokeSem <- struct{}{}:
		defer func() { <-s.invokeSem }()
	case <-ctx.Done():
		return
	}
	for attempt, candidate := range invocationCandidates(node, nodes) {
		event, err := marketFetchEvent(requestForNode(req, candidate), s.eventStorageTarget())
		if err != nil {
			log.WarnContextf(ctx, "build SCF market fetch retry failover event failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
			return
		}
		invokeCtx, invokeCancel := context.WithTimeout(ctx, defaultSCFInvokeAttemptTimeout)
		result, invokeErr := s.Invoker.Invoke(invokeCtx, req.SpaceID, candidate.NodeID, event, cloudnodepb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT)
		invokeCancel()
		if invokeErr != nil {
			if attempt == 0 {
				log.WarnContextf(ctx, "SCF market fetch retry invoke failed; trying failover batch=%s from_node=%s err=%v", req.BatchID, candidate.NodeID, invokeErr)
			} else {
				log.WarnContextf(ctx, "SCF market fetch retry failover invoke failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, invokeErr)
			}
			continue
		}
		updated, err := s.Batches.MarkDispatchedToNode(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(batchCompletionDeadline(req.BatchKind)), candidate.Region, candidate.NodeID, candidate.FunctionName)
		if err != nil {
			log.WarnContextf(ctx, "mark market fetch retry dispatched failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
			return
		}
		// A completion can arrive before the invoke RPC returns. In that case the
		// batch CAS is intentionally false; do not move an already completed retry
		// item back to dispatched, or it can remain stuck forever.
		if !updated {
			return
		}
		if err := s.Retries.MarkStatus(ctx, req.SpaceID, retryKey, "dispatched"); err != nil {
			log.WarnContextf(ctx, "mark market fetch retry status failed key=%s err=%v", retryKey, err)
			return
		}
		if attempt > 0 {
			log.InfoContextf(ctx, "SCF market fetch retry failover succeeded batch=%s node=%s", req.BatchID, candidate.NodeID)
		}
		if s.Metrics != nil {
			if count, countErr := s.Retries.CountPending(ctx, req.SpaceID, req.DatasetID, req.Frequency); countErr == nil {
				s.Metrics.SetRetryPending(req.SpaceID, req.DatasetID, req.Frequency, int(count))
			}
		}
		return
	}
	log.WarnContextf(ctx, "SCF market fetch retry invoke exhausted failover batch=%s original_node=%s", req.BatchID, node.NodeID)
}

// invocationCandidates returns the original node followed by one deterministic
// alternate. A single alternate is enough to bypass a bad SCF node while
// keeping a control-plane outage from multiplying calls across the fleet.
func invocationCandidates(primary scfinvoker.Node, nodes []scfinvoker.Node) []scfinvoker.Node {
	if strings.TrimSpace(primary.NodeID) == "" || len(nodes) == 0 {
		return []scfinvoker.Node{primary}
	}
	for index, node := range nodes {
		if node.NodeID != primary.NodeID {
			continue
		}
		if len(nodes) == 1 {
			return []scfinvoker.Node{primary}
		}
		return []scfinvoker.Node{primary, nodes[(index+1)%len(nodes)]}
	}
	return []scfinvoker.Node{primary, nodes[0]}
}

func requestForNode(req Request, node scfinvoker.Node) Request {
	req.Region = node.Region
	req.NodeID = node.NodeID
	req.FunctionName = node.FunctionName
	return req
}

func (s *Scheduler) expandTask(ctx context.Context, task domain.CollectionTask) ([]domain.CollectionItem, []string, error) {
	return s.expandTaskWithCache(ctx, task, nil)
}

func (s *Scheduler) expandTaskWithCache(ctx context.Context, task domain.CollectionTask, cache *taskExpansionCache) ([]domain.CollectionItem, []string, error) {
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return nil, nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(params.Provider))
	marketType := strings.ToLower(strings.TrimSpace(params.MarketType))
	marketID := strings.ToLower(firstNonEmpty(params.MarketID, task.SpaceID, s.SpaceID))
	instrumentType := strings.ToLower(firstNonEmpty(params.InstrumentType, defaultInstrumentTypeForMarket(marketID, marketType)))
	sourceID := strings.ToLower(strings.TrimSpace(params.SourceID))
	dataType := strings.ToLower(firstNonEmpty(params.Collector.DataType, task.DataType))
	targetDataset := params.Target.DatasetID
	if targetDataset == "" {
		return nil, nil, fmt.Errorf("target dataset is required")
	}
	frequencies := append([]string(nil), params.Collector.Intervals...)
	if len(frequencies) == 0 && params.Schedule.Interval != "" {
		frequencies = []string{params.Schedule.Interval}
	}
	if dataType != "kline" {
		return nil, nil, fmt.Errorf("unsupported data_type %q", dataType)
	}
	if s.Symbols == nil {
		return nil, nil, fmt.Errorf("dataset subject resolver is not initialized")
	}
	spaceID := strings.TrimSpace(task.SpaceID)
	if spaceID == "" {
		spaceID = strings.TrimSpace(s.SpaceID)
	}
	// A collection task may contain tags owned by different providers. Resolve
	// each tag independently so its source and market_type become the request
	// identity. Current tasks do not own Provider/market; params are used only as
	// a compatibility fallback when an old metadata client cannot load Tag routes.
	type taggedSubjects struct {
		subjects []domain.Subject
		source   string
		market   string
	}
	tagIDs := normalizedTaskTagIDs(task.TagIDs)
	if len(tagIDs) == 0 {
		// Current CollectionTasks persist their Tag relation directly. Dataset
		// lookup is only a compatibility fallback for legacy tasks; avoiding it
		// on the normal path removes one cross-node Metadata RPC per task/tick.
		dataset, datasetErr := s.Symbols.GetDataset(ctx, spaceID, targetDataset)
		if datasetErr != nil {
			return nil, nil, fmt.Errorf("get target dataset %s: %w", targetDataset, datasetErr)
		}
		tagIDs = normalizedTaskTagIDs(dataset.SubjectTags)
	}
	groups := make([]taggedSubjects, 0, len(tagIDs))
	if tagSource, ok := s.Symbols.(interface {
		GetTag(context.Context, string, string) (*storagepb.Tag, error)
	}); ok && len(tagIDs) > 0 {
		for _, tagID := range tagIDs {
			tagID = strings.TrimSpace(tagID)
			if tagID == "" {
				continue
			}
			cacheKey := expansionCacheKey(spaceID, []string{tagID})
			var tag *storagepb.Tag
			if cache != nil {
				tag = cache.tags[cacheKey]
			}
			if tag == nil {
				var tagErr error
				tag, tagErr = tagSource.GetTag(ctx, spaceID, tagID)
				if tagErr != nil {
					return nil, nil, fmt.Errorf("get target tag %s: %w", tagID, tagErr)
				}
				if cache != nil {
					cache.tags[cacheKey] = tag
				}
			}
			var subjects []domain.Subject
			subjectsCached := false
			if cache != nil {
				if cached, ok := cache.subjects[cacheKey]; ok {
					subjects = append([]domain.Subject(nil), cached...)
					subjectsCached = true
				}
			}
			if !subjectsCached {
				var resolveErr error
				subjects, resolveErr = s.Symbols.ResolveSubjects(ctx, spaceID, []string{tagID})
				if resolveErr != nil {
					return nil, nil, fmt.Errorf("resolve target tag %s subjects: %w", tagID, resolveErr)
				}
				if cache != nil {
					cache.subjects[cacheKey] = append([]domain.Subject(nil), subjects...)
				}
			}
			source := strings.ToLower(strings.TrimSpace(tag.GetSource()))
			market := strings.ToLower(strings.TrimSpace(tag.GetMarketType()))
			if source == "" {
				return nil, nil, fmt.Errorf("target tag %s has no source", tagID)
			}
			if market == "" {
				return nil, nil, fmt.Errorf("target tag %s has no market_type", tagID)
			}
			groups = append(groups, taggedSubjects{subjects: subjects, source: source, market: market})
		}
	} else {
		cacheKey := expansionCacheKey(spaceID, tagIDs)
		var subjects []domain.Subject
		subjectsCached := false
		if cache != nil {
			if cached, ok := cache.subjects[cacheKey]; ok {
				subjects = append([]domain.Subject(nil), cached...)
				subjectsCached = true
			}
		}
		if !subjectsCached {
			var resolveErr error
			subjects, resolveErr = s.Symbols.ResolveSubjects(ctx, spaceID, tagIDs)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("resolve target dataset subjects: %w", resolveErr)
			}
			if cache != nil {
				cache.subjects[cacheKey] = append([]domain.Subject(nil), subjects...)
			}
		}
		groups = append(groups, taggedSubjects{subjects: subjects, source: strings.ToLower(sourceID), market: marketType})
	}
	items := make([]domain.CollectionItem, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		// Tag.source is the immutable Provider/DataSource binding. source_id is
		// the concrete adapter route (spot_http/swap_http/...) and is derived
		// separately from provider + instrument type.
		groupProvider := strings.ToLower(firstNonEmpty(group.source, provider))
		groupMarket := strings.ToLower(firstNonEmpty(group.market, marketType))
		for _, subject := range group.subjects {
			if strings.TrimSpace(subject.SubjectID) == "" {
				continue
			}
			subjectID := strings.ToUpper(strings.TrimSpace(subject.SubjectID))
			symbol, symbolErr := resolveProviderSymbol(s.ResolveSymbol, groupProvider, marketID, groupMarket, subjectID)
			if symbolErr != nil {
				log.WarnContextf(ctx, "skip market symbol without valid provider symbol subject=%q error=%v", subject.SubjectID, symbolErr)
				continue
			}
			groupInstrumentType := instrumentType
			if strings.TrimSpace(params.InstrumentType) == "" {
				groupInstrumentType = defaultInstrumentTypeForMarket(marketID, groupMarket)
			}
			groupSourceID := strings.TrimSpace(sourceID)
			if groupSourceID == "" && s.ResolveSourceID != nil {
				groupSourceID = strings.TrimSpace(s.ResolveSourceID(groupProvider, groupInstrumentType))
			}
			if groupSourceID == "" {
				return nil, nil, fmt.Errorf("no source_id route for provider=%s instrument_type=%s", groupProvider, groupInstrumentType)
			}
			identity := strings.Join([]string{groupProvider, groupSourceID, groupMarket, subjectID}, "\x00")
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			items = append(items, domain.CollectionItem{SubjectID: subjectID, Symbol: symbol, Provider: groupProvider, SourceID: groupSourceID, MarketID: marketID, InstrumentType: groupInstrumentType, MarketType: groupMarket, DataType: "kline", DatasetID: targetDataset, OutputFields: append([]string(nil), params.OutputFields...)})
		}
	}
	items, seriesHash, err := s.materializeTaskSeries(ctx, task, items)
	if err != nil {
		return nil, nil, err
	}
	_ = seriesHash
	return items, frequencies, nil
}

func (s *Scheduler) materializeTaskSeries(ctx context.Context, task domain.CollectionTask, items []domain.CollectionItem) ([]domain.CollectionItem, string, error) {
	series := make([]domain.TaskSeries, 0, len(items))
	for _, item := range items {
		series = append(series, domain.TaskSeries{
			SpaceID: task.SpaceID, TaskID: task.TaskID, SubjectID: item.SubjectID, Provider: item.Provider, SourceID: item.SourceID,
			MarketType: item.MarketType, ProviderSymbol: item.Symbol, SeriesTag: collectionItemSeriesTag(task.SpaceID, item),
		})
	}
	// Canonicalize locally even when the scheduler is used in a unit test
	// without a TaskRepository. Production additionally persists the materialized
	// union so later planning never needs to infer Dataset membership from a Batch.
	byKey := make(map[string]domain.TaskSeries, len(series))
	for _, row := range series {
		row.SeriesKey = domain.CanonicalSeriesKey(row.Provider, row.SourceID, row.MarketType, row.SubjectID, row.SeriesTag)
		byKey[row.SeriesKey] = row
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("collection task %s expands to no series", task.TaskID)
	}
	hash := domain.SeriesSetHash(keys)
	if s.Tasks != nil && strings.TrimSpace(task.TaskID) != "" {
		persisted := make([]domain.TaskSeries, 0, len(keys))
		for _, key := range keys {
			persisted = append(persisted, byKey[key])
		}
		persistedHash, _, persistErr := s.Tasks.ReplaceTaskSeries(ctx, task.SpaceID, task.TaskID, persisted)
		if persistErr != nil {
			return nil, "", fmt.Errorf("materialize task series task=%s: %w", task.TaskID, persistErr)
		}
		hash = persistedHash
	}
	indexByKey := make(map[string]uint32, len(keys))
	for index, key := range keys {
		indexByKey[key] = uint32(index)
	}
	count := uint32(len(keys))
	for i := range items {
		key := domain.CanonicalSeriesKey(items[i].Provider, items[i].SourceID, items[i].MarketType, items[i].SubjectID, collectionItemSeriesTag(task.SpaceID, items[i]))
		items[i].SeriesIndex = indexByKey[key]
		items[i].SeriesHash = hash
		items[i].ExpectedCount = count
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].SeriesIndex != items[j].SeriesIndex {
			return items[i].SeriesIndex < items[j].SeriesIndex
		}
		return items[i].SubjectID < items[j].SubjectID
	})
	return items, hash, nil
}

func (s *Scheduler) expandTaskForPlanning(ctx context.Context, task domain.CollectionTask) ([]domain.CollectionItem, []string, error) {
	if s == nil || s.Tasks == nil {
		return s.expandTask(ctx, task)
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return nil, nil, err
	}
	dataType := strings.ToLower(firstNonEmpty(params.Collector.DataType, task.DataType))
	if dataType != "kline" {
		return nil, nil, fmt.Errorf("unsupported data_type %q", dataType)
	}
	targetDataset := firstNonEmpty(params.Target.DatasetID, task.ResultDatasetID)
	if strings.TrimSpace(targetDataset) == "" {
		return nil, nil, fmt.Errorf("target dataset is required")
	}
	frequencies := append([]string(nil), params.Collector.Intervals...)
	if len(frequencies) == 0 && params.Schedule.Interval != "" {
		frequencies = []string{params.Schedule.Interval}
	}
	rows, err := s.Tasks.ListTaskSeries(ctx, task.SpaceID, task.TaskID)
	if err != nil {
		return nil, nil, fmt.Errorf("load materialized task series: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("collection task %s has no materialized task series", task.TaskID)
	}
	keys := make([]string, 0, len(rows))
	for index, row := range rows {
		if row.SeriesIndex != uint32(index) {
			return nil, nil, fmt.Errorf("collection task %s has non-dense series_index at %d: %d", task.TaskID, index, row.SeriesIndex)
		}
		key := domain.CanonicalSeriesKey(row.Provider, row.SourceID, row.MarketType, row.SubjectID, row.SeriesTag)
		if key != row.SeriesKey {
			return nil, nil, fmt.Errorf("collection task %s series key mismatch at index %d", task.TaskID, index)
		}
		keys = append(keys, key)
	}
	hash := domain.SeriesSetHash(keys)
	if strings.TrimSpace(task.SeriesHash) == "" || hash != strings.TrimSpace(task.SeriesHash) {
		return nil, nil, fmt.Errorf("collection task %s series_hash changed during planning", task.TaskID)
	}
	marketID := strings.ToLower(firstNonEmpty(params.MarketID, task.SpaceID, s.SpaceID))
	count := uint32(len(rows))
	items := make([]domain.CollectionItem, 0, len(rows))
	for _, row := range rows {
		instrumentType := strings.ToLower(firstNonEmpty(params.InstrumentType, defaultInstrumentTypeForMarket(marketID, row.MarketType)))
		items = append(items, domain.CollectionItem{
			SubjectID: row.SubjectID, Symbol: row.ProviderSymbol, Provider: row.Provider, SourceID: row.SourceID,
			MarketID: marketID, InstrumentType: instrumentType, MarketType: row.MarketType, DataType: dataType,
			DatasetID: targetDataset, OutputFields: append([]string(nil), params.OutputFields...), SeriesIndex: row.SeriesIndex,
			SeriesHash: hash, ExpectedCount: count,
		})
	}
	return items, frequencies, nil
}

func batchKindForTask(domain.CollectionTask) domain.BatchKind { return domain.BatchKindRealtime }

func targetDataTime(now time.Time, frequency string) (time.Time, error) {
	times, err := report.RecentDatasetTimes(frequency, now.UTC(), 2)
	if err != nil {
		return time.Time{}, err
	}
	return times[1], nil
}

func normalizeStorageFrequency(frequency string) (string, error) {
	return report.NormalizeDatasetFrequency(strings.TrimSpace(frequency))
}

func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:])[:32]
}

func stableRouteID(marketType, datasetID, frequency string) string {
	return strings.Join([]string{strings.ToLower(strings.TrimSpace(marketType)), strings.TrimSpace(datasetID), strings.ToLower(strings.TrimSpace(frequency))}, ":")
}

func timePtr(value time.Time) *time.Time { return &value }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Scheduler) eventStorageTarget() string {
	if s == nil {
		return ""
	}
	if target := strings.TrimSpace(s.InvokeStorageTarget); target != "" {
		return target
	}
	return strings.TrimSpace(s.StorageTarget)
}

func marketFetchEvent(req Request, storageTarget string) (map[string]any, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	event := map[string]any{
		"action":                     "market_fetch",
		"source":                     model.EventSourceCollectorScheduler,
		"request_id":                 req.BatchID,
		"timestamp":                  time.Now().UTC().Format(time.RFC3339Nano),
		"storage_rpc_gateway_target": strings.TrimSpace(storageTarget),
		"data":                       data,
	}
	// Validate the exact JSON object sent as Tencent ClientContext, not only
	// the inner Request. Keep headroom below SCF's 128KB limit for provider
	// serialization differences.
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 120*1024 {
		return nil, fmt.Errorf("market_fetch client context exceeds 120KB")
	}
	return event, nil
}
