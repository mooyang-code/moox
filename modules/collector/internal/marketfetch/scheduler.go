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
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultBatchSize         = MaxRealtimeItems
	DefaultMaxPlan           = 1000
	DefaultInvokeConcurrency = 20
	// 256 slots cover StockCN's timer wave and the bounded retry workload.
	StockCNTimerInvokeConcurrency = 256
	maxSchedulerInvokeConcurrency = 256
	// Keep stale retry work from monopolizing Collector's single-writer SQLite
	// pool. Production can raise the current-period quota to its SCF wave size.
	maxDueRecoveryBatchesPerTick = 16
	maxRetryBatchesPerTick       = 16
	maxHistoricalBatchesPerPass  = 2
	retryTargetLookupBatchSize   = 500
	maxRetryDispatchDuration     = 30 * time.Second
	maxDueRecoveryDuration       = 15 * time.Second
	retryMaintenanceWindow       = 55 * time.Second
	// One full bounded maintenance pass is budgeted per minute; faster passes
	// may catch up through a queued wake but are not required for capacity.
	RetryMaintenancePassesPerMinute = 1
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

type retryMaintenanceRequest struct {
	spaceID string
	nodes   []scfinvoker.Node
	now     time.Time
}

type retryDispatchCandidate struct {
	retry      domain.RetryItem
	item       domain.CollectionItem
	historical bool
}

type retryDispatchGroup struct {
	key                 string
	batchKind           domain.BatchKind
	periodTime          *time.Time
	periodDeadlineAt    *time.Time
	requirePeriodCommit bool
	historical          bool
	attempt             int
	retryScope          string
	writeTargetID       string
	retries             []domain.RetryItem
	items               []domain.CollectionItem
	targets             []domain.WriteTarget
	instanceIDs         []string
	seenInstances       map[string]struct{}
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
	SCFRegionBlacklists           map[string][]string
	ResolveSymbol                 SymbolResolver
	ResolveSourceID               func(string, string) string
	Tasks                         *store.TaskRepository
	Instances                     *store.TaskInstanceRepository
	Batches                       *store.FetchBatchRepository
	Runs                          *store.RunRepository
	Retries                       *store.FetchRetryRepository
	PeriodSeriesSnapshot          *store.PeriodSeriesSnapshotRepository
	PeriodStorageStates           *store.PeriodStorageStateRepository
	TimerPeriodBatches            *store.TimerPeriodBatchRepository
	TimerPeriodPlanner            *TimerPeriodPlanner
	TimerAssignments              func() []NodeAssignment
	TimerMeasuredSafeGroupSize    int
	Invoker                       MarketFetchInvoker
	Storage                       StorageFactory
	BatchSize                     int
	InvokeConcurrency             int
	MaintenanceBatchLimit         int
	MaintenanceRecoveryBatchLimit int
	MaxRetryAttempts              int
	WakePeriodFailureReporter     func()
	Metrics                       *Metrics
	SpaceID                       string
	Symbols                       datasetSource
	// InvokeNonRealtimeOnly keeps realtime K-lines out of the Invoke path when
	// Timer-triggered nodes own the live schedule.
	InvokeNonRealtimeOnly bool
	DNSCache              interface {
		Snapshot() map[string]sources.DNSResolution
	}
	Now                              func() time.Time
	mu                               sync.Mutex
	lastTaskID                       string
	planStates                       map[string]scheduleState
	invokeSem                        chan struct{}
	retryMaintenanceMu               sync.Mutex
	retryMaintenanceQueueMu          sync.Mutex
	retryMaintenanceWake             chan retryMaintenanceRequest
	invokeAttemptTimeout             time.Duration
	retryDispatchWaitTimeoutOverride time.Duration
	retryDispatchMu                  sync.Mutex
	retryDispatchInFlight            map[string]struct{}
}

const (
	defaultBatchCompletionDeadline = 70 * time.Second
	defaultSCFInvokeAttemptTimeout = 10 * time.Second
	retryDispatchDatabaseSlack     = 30 * time.Second
	retryDispatchScheduleSlack     = 5 * time.Second
	periodFailureReportingSlack    = 4 * time.Minute
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

func datasetPeriodCompletionWindow(kind domain.BatchKind) time.Duration {
	// Leave room for the initial request, all three retries, each bounded SCF
	// completion window, and the final failure report. Two minutes cover the
	// scheduler tick delay between backoff expiry and retry dispatch.
	return 5*batchCompletionDeadline(kind) + retryDelay(1) + retryDelay(2) + retryDelay(3) + periodFailureReportingSlack
}

func retryDispatchWaitTimeout(concurrency int, batchLimit ...int) time.Duration {
	if concurrency <= 0 {
		concurrency = DefaultInvokeConcurrency
	}
	if concurrency > maxSchedulerInvokeConcurrency {
		concurrency = maxSchedulerInvokeConcurrency
	}
	limit := maxRetryBatchesPerTick + maxHistoricalBatchesPerPass
	if len(batchLimit) > 0 && batchLimit[0] > 0 {
		limit = batchLimit[0]
	}
	waves := (limit + concurrency - 1) / concurrency
	return time.Duration(waves)*2*defaultSCFInvokeAttemptTimeout + retryDispatchDatabaseSlack
}

func (s *Scheduler) maintenanceBatchLimit() int {
	limit := maxDueRecoveryBatchesPerTick
	if s != nil && s.MaintenanceBatchLimit > 0 {
		limit = s.MaintenanceBatchLimit
	}
	return min(limit, 1000)
}

func (s *Scheduler) maintenanceRecoveryBatchLimit() int {
	limit := maxDueRecoveryBatchesPerTick
	if s != nil && s.MaintenanceRecoveryBatchLimit > 0 {
		limit = s.MaintenanceRecoveryBatchLimit
	} else if s != nil && s.MaintenanceBatchLimit > 0 {
		limit = s.MaintenanceBatchLimit
	}
	return min(limit, 1000)
}

func (s *Scheduler) historicalBatchLimit() int {
	return maxHistoricalBatchesPerPass
}

func (s *Scheduler) totalMaintenanceBatchLimit() int {
	return s.maintenanceBatchLimit() + s.historicalBatchLimit()
}

func retryDispatchReservationKeys(spaceID, batchID string, retryKeys []string) []string {
	spaceID, batchID = strings.TrimSpace(spaceID), strings.TrimSpace(batchID)
	keys := []string{spaceID + "\x00batch\x00" + batchID}
	for _, retryKey := range uniqueStrings(retryKeys) {
		keys = append(keys, spaceID+"\x00retry\x00"+retryKey)
	}
	return keys
}

func (s *Scheduler) beginRetryDispatch(spaceID, batchID string, retryKeys []string) bool {
	s.retryDispatchMu.Lock()
	defer s.retryDispatchMu.Unlock()
	if s.retryDispatchInFlight == nil {
		s.retryDispatchInFlight = make(map[string]struct{})
	}
	keys := retryDispatchReservationKeys(spaceID, batchID, retryKeys)
	for _, key := range keys {
		if _, exists := s.retryDispatchInFlight[key]; exists {
			return false
		}
	}
	for _, key := range keys {
		s.retryDispatchInFlight[key] = struct{}{}
	}
	return true
}

func (s *Scheduler) endRetryDispatch(spaceID, batchID string, retryKeys []string) {
	s.retryDispatchMu.Lock()
	for _, key := range retryDispatchReservationKeys(spaceID, batchID, retryKeys) {
		delete(s.retryDispatchInFlight, key)
	}
	s.retryDispatchMu.Unlock()
}

func (s *Scheduler) retryKeysInFlight(spaceID string) []string {
	prefix := strings.TrimSpace(spaceID) + "\x00retry\x00"
	s.retryDispatchMu.Lock()
	defer s.retryDispatchMu.Unlock()
	keys := make([]string, 0, len(s.retryDispatchInFlight))
	for key := range s.retryDispatchInFlight {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, strings.TrimPrefix(key, prefix))
		}
	}
	sort.Strings(keys)
	return keys
}

func (s *Scheduler) retryDispatchWaitBudget() time.Duration {
	if s.retryDispatchWaitTimeoutOverride > 0 {
		return s.retryDispatchWaitTimeoutOverride
	}
	return retryDispatchWaitTimeout(cap(s.invokeSem), s.totalMaintenanceBatchLimit())
}

func (s *Scheduler) ensureInvokeSemaphore() {
	if s.invokeSem != nil {
		return
	}
	limit := s.InvokeConcurrency
	if limit <= 0 {
		limit = DefaultInvokeConcurrency
	}
	if limit > maxSchedulerInvokeConcurrency {
		limit = maxSchedulerInvokeConcurrency
	}
	s.invokeSem = make(chan struct{}, limit)
}

func (s *Scheduler) startRetryMaintenance(spaceID string, nodes []scfinvoker.Node, now time.Time) {
	request := retryMaintenanceRequest{spaceID: strings.TrimSpace(spaceID), nodes: append([]scfinvoker.Node(nil), nodes...), now: now.UTC()}
	s.retryMaintenanceQueueMu.Lock()
	if s.retryMaintenanceWake == nil {
		s.retryMaintenanceWake = make(chan retryMaintenanceRequest, 1)
		go s.retryMaintenanceWorker(s.retryMaintenanceWake)
	}
	enqueueLatestRetryMaintenance(s.retryMaintenanceWake, request)
	s.retryMaintenanceQueueMu.Unlock()
}

func enqueueLatestRetryMaintenance(wake chan retryMaintenanceRequest, request retryMaintenanceRequest) {
	select {
	case wake <- request:
	default:
		select {
		case <-wake:
		default:
		}
		wake <- request
	}
}

func (s *Scheduler) retryMaintenanceWorker(wake <-chan retryMaintenanceRequest) {
	for request := range wake {
		s.retryMaintenanceQueueMu.Lock()
		select {
		case latest := <-wake:
			request = latest
		default:
		}
		s.retryMaintenanceQueueMu.Unlock()

		maintenanceCtx, maintenanceCancel := context.WithTimeout(context.Background(), retryMaintenanceWindow)
		passNow := time.Now().UTC()
		if !request.now.IsZero() && request.now.After(passNow) {
			passNow = request.now.UTC()
		}
		s.retryMaintenanceMu.Lock()
		err := s.runRetryMaintenancePass(maintenanceCtx, request.spaceID, request.nodes, passNow)
		s.retryMaintenanceMu.Unlock()
		if err != nil {
			log.WarnContextf(maintenanceCtx, "market fetch retry maintenance failed: %v", err)
		}
		maintenanceCancel()
	}
}

func (s *Scheduler) runRetryMaintenancePass(ctx context.Context, spaceID string, nodes []scfinvoker.Node, now time.Time) error {
	recoveryCtx, recoveryCancel := context.WithTimeout(ctx, maxDueRecoveryDuration)
	recoveryErr := s.recoverDue(recoveryCtx, spaceID, nodes, now)
	recoveryCancel()
	dispatchCtx, dispatchCancel := context.WithTimeout(ctx, maxRetryDispatchDuration)
	defer dispatchCancel()
	dispatchErr := s.dispatchDueRetries(dispatchCtx, spaceID, nodes, now)
	var errs []error
	if recoveryErr != nil {
		errs = append(errs, fmt.Errorf("recover due market fetch batches: %w", recoveryErr))
	}
	if dispatchErr != nil {
		errs = append(errs, fmt.Errorf("dispatch due market fetch retries: %w", dispatchErr))
	}
	return errors.Join(errs...)
}

func (s *Scheduler) Tick(ctx context.Context, spaceID string) (err error) {
	if s == nil || s.Tasks == nil || s.Batches == nil || s.Invoker == nil {
		return fmt.Errorf("market fetch scheduler is not initialized")
	}
	if !s.mu.TryLock() {
		return nil
	}
	defer s.mu.Unlock()
	s.ensureInvokeSemaphore()
	var finishRun func()
	var maintenanceSpaceID string
	var maintenanceNodes []scfinvoker.Node
	var maintenanceNow time.Time
	defer func() {
		if finishRun != nil {
			finishRun()
		}
		if maintenanceSpaceID != "" {
			s.startRetryMaintenance(maintenanceSpaceID, maintenanceNodes, maintenanceNow)
		}
	}()
	if s.planStates == nil {
		s.planStates = make(map[string]scheduleState)
	}
	if strings.TrimSpace(spaceID) == "" {
		spaceID = strings.TrimSpace(s.SpaceID)
	}
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	defer func() {
		if err == nil {
			s.Metrics.ObserveSchedulerSuccess(spaceID, "planning", now)
		}
	}()
	dnsRoutes := s.dnsSnapshot(ctx)
	allTasks, err := s.Tasks.ListEnabled(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list enabled collection tasks: %w", err)
	}
	// Freeze each current Dataset period before establishing the Run cutoff.
	// Existing periods are loaded without resolving Tags again, so membership
	// changes can only affect a period that has not been created yet.
	expansionCache := newTaskExpansionCache()
	if err := s.prepareCurrentPeriodSeriesSnapshots(ctx, now, filterMarketFetchTasks(allTasks), expansionCache); err != nil {
		return err
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
		finishRun = func() {
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
		}
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
	retryNodes := uniqueSCFNodes(append(append([]scfinvoker.Node(nil), invokeNodes...), timerNodes...))
	maintenanceSpaceID = spaceID
	maintenanceNodes = retryNodes
	maintenanceNow = now
	if len(invokeNodes) == 0 && requiresInvoke {
		return fmt.Errorf("no active Invoke market fetcher nodes")
	}
	// Planning is intentionally two-phase. Persist every task's destination
	// before the first batch is dispatched so a shared source request can fan
	// out to targets owned by tasks that appear later in the scheduler scan.
	// Without this pre-pass the result depended on goroutine timing: task A
	// could reach SCF before task B had attached its WriteTarget.
	blockedPeriods, err := s.primeSharedWriteTargets(ctx, spaceID, currentRunID, runCutoff, now, tasks)
	if err != nil {
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
		frequencies, err := s.taskFrequencies(task)
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
			if _, blocked := blockedPeriods[task.TaskID+"\x00"+frequency]; blocked {
				continue
			}
			target, err := targetDataTime(now, frequency)
			if err != nil {
				log.WarnContextf(ctx, "skip task=%s frequency=%s: %v", task.TaskID, frequency, err)
				continue
			}
			items, err := s.expandTaskForPeriod(ctx, task, frequency, target)
			if err != nil {
				log.WarnContextf(ctx, "skip task=%s frequency=%s period=%s: %v", task.TaskID, frequency, target.UTC().Format(time.RFC3339), err)
				continue
			}
			if s.ownsTimerTask(task) {
				if s.TimerPeriodPlanner == nil || s.TimerAssignments == nil {
					return fmt.Errorf("Timer period planning is not configured for task %s", task.TaskID)
				}
				created, planErr := s.planTimerPeriodForAssignments(ctx, spaceID, currentRunID, runCutoff, task, frequency, target, now, s.TimerAssignments(), timerNodes)
				if planErr != nil {
					return fmt.Errorf("plan Timer period task=%s frequency=%s: %w", task.TaskID, frequency, planErr)
				}
				planned += created
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
				req := Request{BatchID: batchID, SyncPointID: syncPointID, ScheduleID: scheduleID, BatchKind: batchKind, ShardIndex: shard, SpaceID: spaceID, MarketID: batchItems[start].MarketID, InstrumentType: batchItems[start].InstrumentType, DatasetID: batchItems[start].DatasetID, Frequency: frequency, Provider: batchProvider, SourceID: batchItems[start].SourceID, MarketType: batchMarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: dnsRoutes, RequirePeriodCommit: true, Items: batchItems[start:end]}
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
	if len(blockedPeriods) > 0 {
		keys := make([]string, 0, len(blockedPeriods))
		for key := range blockedPeriods {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		conflicts := make([]error, 0, len(keys))
		for _, key := range keys {
			conflicts = append(conflicts, blockedPeriods[key])
		}
		return errors.Join(conflicts...)
	}
	return nil
}

func (s *Scheduler) primeSharedWriteTargets(ctx context.Context, spaceID, runID string, runCutoff, now time.Time, tasks []domain.CollectionTask) (map[string]error, error) {
	blockedPeriods := make(map[string]error)
	if s == nil || s.Instances == nil {
		return blockedPeriods, nil
	}
	for _, task := range tasks {
		if s.ownsTimerTask(task) {
			continue
		}
		matches, err := s.taskIdentityMatchesRun(ctx, spaceID, task, runCutoff)
		if err != nil {
			return nil, fmt.Errorf("check collection task before shared planning: %w", err)
		}
		if !matches {
			return nil, fmt.Errorf("collector run %s became stale before planning task %s", runID, task.TaskID)
		}
		frequencies, err := s.taskFrequencies(task)
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
			items, periodErr := s.expandTaskForPeriod(ctx, task, frequency, target)
			if periodErr != nil {
				log.WarnContextf(ctx, "skip task=%s frequency=%s period=%s during shared planning: %v", task.TaskID, frequency, target.UTC().Format(time.RFC3339), periodErr)
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
				return nil, matchErr
			}
			if !matches {
				return nil, fmt.Errorf("collector run %s became stale before period initialization for task %s", runID, task.TaskID)
			}
			if err := s.ensureDatasetPeriod(ctx, task, items, frequency, target, now); err != nil {
				if errors.Is(err, marketstorage.ErrDatasetPeriodConflict) {
					blockedPeriods[task.TaskID+"\x00"+frequency] = fmt.Errorf("skip collection task for conflicting dataset period task=%s frequency=%s target=%s: %w", task.TaskID, frequency, target.UTC().Format(time.RFC3339), err)
					log.WarnContextf(ctx, "%v", blockedPeriods[task.TaskID+"\x00"+frequency])
					continue
				}
				return nil, fmt.Errorf("ensure dataset period task=%s frequency=%s: %w", task.TaskID, frequency, err)
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
				return nil, fmt.Errorf("persist shared collection instances task=%s frequency=%s: %w", task.TaskID, frequency, err)
			}
			if err := s.Instances.UpsertWriteTargets(ctx, targets); err != nil {
				return nil, fmt.Errorf("persist shared write targets task=%s frequency=%s: %w", task.TaskID, frequency, err)
			}
		}
	}
	return blockedPeriods, nil
}

func (s *Scheduler) ensureDatasetPeriod(ctx context.Context, task domain.CollectionTask, items []domain.CollectionItem, frequency string, period, now time.Time) error {
	if len(items) == 0 || strings.TrimSpace(items[0].SeriesHash) == "" || items[0].ExpectedCount == 0 {
		return fmt.Errorf("task %s has no materialized series expectation", task.TaskID)
	}
	if _, err := marketdata.ParseFrequency(frequency); err != nil {
		return fmt.Errorf("task %s has invalid period frequency %q: %w", task.TaskID, frequency, err)
	}
	periodFrequency := strings.TrimSpace(frequency)
	if s.Storage == nil {
		// Lightweight scheduler unit tests may not wire Storage. Production
		// bootstrap always provides it and integration tests cover this boundary.
		return nil
	}
	client, err := s.Storage(items[0].MarketType, "collector")
	if err != nil {
		return err
	}
	periodClient, ok := client.(periodStorage)
	if !ok {
		return fmt.Errorf("storage client does not support dataset period commits")
	}
	deadline := now.UTC().Add(datasetPeriodCompletionWindow(batchKindForTask(task)))
	snapshot := make([]*storagepb.DatasetPeriodSeries, 0, len(items))
	for index, item := range items {
		if item.SeriesIndex != uint32(index) || item.SeriesHash != items[0].SeriesHash || item.ExpectedCount != items[0].ExpectedCount || item.PeriodReservationID != items[0].PeriodReservationID || strings.TrimSpace(item.SubjectID) == "" {
			return fmt.Errorf("task %s has invalid immutable period series snapshot at index %d", task.TaskID, index)
		}
		snapshot = append(snapshot, &storagepb.DatasetPeriodSeries{SeriesIndex: item.SeriesIndex, SubjectId: item.SubjectID, SeriesTag: collectionItemSeriesTag(task.SpaceID, item)})
	}
	expectation := &storagepb.DatasetPeriodExpectation{
		SpaceId: task.SpaceID, DatasetId: items[0].DatasetID, Frequency: periodFrequency,
		PeriodTime: period.UTC().Unix(), SeriesHash: items[0].SeriesHash, ExpectedCount: items[0].ExpectedCount, DeadlineAt: deadline.Unix(), SeriesSnapshot: snapshot,
		ReservationId: items[0].PeriodReservationID,
	}
	state, err := periodClient.EnsureDatasetPeriod(ctx, expectation)
	if err != nil {
		return err
	}
	expectedKey := domain.PeriodKey{SpaceID: strings.TrimSpace(task.SpaceID), DatasetID: strings.TrimSpace(items[0].DatasetID), Frequency: periodFrequency, PeriodTime: period.UTC()}
	if strings.TrimSpace(state.Key.SpaceID) != expectedKey.SpaceID || strings.TrimSpace(state.Key.DatasetID) != expectedKey.DatasetID || strings.TrimSpace(state.Key.Frequency) != expectedKey.Frequency || !state.Key.PeriodTime.Equal(expectedKey.PeriodTime) || strings.ToLower(strings.TrimSpace(state.SeriesHash)) != strings.ToLower(strings.TrimSpace(items[0].SeriesHash)) || state.ExpectedCount != items[0].ExpectedCount || state.DeadlineAt.IsZero() {
		return fmt.Errorf("Storage returned period state that does not match the requested snapshot")
	}
	if s.PeriodStorageStates != nil {
		if err := s.PeriodStorageStates.ObservePeriodStorageState(ctx, state); err != nil {
			return fmt.Errorf("persist authoritative Storage period state: %w", err)
		}
	}
	return nil
}

func (s *Scheduler) ownsTimerTask(task domain.CollectionTask) bool {
	if s == nil {
		return false
	}
	spaceID := firstNonEmpty(task.SpaceID, s.SpaceID)
	return s.InvokeNonRealtimeOnly && strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) && isKlineTask(task)
}

func (s *Scheduler) planTimerPeriodForAssignments(ctx context.Context, spaceID, runID string, runCutoff time.Time, task domain.CollectionTask, frequency string, period, now time.Time, assignments []NodeAssignment, timerNodes []scfinvoker.Node) (int, error) {
	planner := s.TimerPeriodPlanner
	if planner == nil || planner.Snapshots == nil || planner.States == nil || planner.Batches == nil {
		return 0, fmt.Errorf("Timer period planner repositories are not configured")
	}
	if _, err := marketdata.ParseFrequency(frequency); err != nil {
		return 0, err
	}
	params, datasetID, _, err := s.taskPeriodDefinition(task)
	if err != nil {
		return 0, err
	}
	periodKey := domain.PeriodKey{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period.UTC()}
	snapshot, found, err := planner.Snapshots.GetPeriodSeriesSnapshot(ctx, periodKey)
	if err != nil {
		return 0, fmt.Errorf("load Timer period series snapshot: %w", err)
	}
	if !found {
		return 0, fmt.Errorf("Timer period series snapshot is missing")
	}
	if len(snapshot.Entries) != int(snapshot.ExpectedCount) {
		return 0, fmt.Errorf("Timer period full membership differs from the frozen snapshot")
	}
	// An existing manifest set is the immutable period owner. Do not consult
	// refreshed assignments or attempt to fill a missing group from new tags.
	existing, err := planner.Batches.ListByPeriod(ctx, periodKey)
	if err != nil {
		return 0, fmt.Errorf("read existing Timer period manifests: %w", err)
	}
	if len(existing) != 0 {
		return 0, nil
	}
	routeVersion, sources, err := stockCNAssignmentRoute()
	if err != nil {
		return 0, err
	}
	if s.TimerMeasuredSafeGroupSize <= 0 || s.TimerMeasuredSafeGroupSize > MaxRealtimeItems {
		return 0, fmt.Errorf("Timer measured safe group size must be between 1 and %d", MaxRealtimeItems)
	}
	entries := append([]domain.PeriodSeriesSnapshotEntry(nil), snapshot.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].SeriesIndex < entries[j].SeriesIndex })
	subjects := make([]string, len(entries))
	entryBySubject := make(map[string]domain.PeriodSeriesSnapshotEntry, len(entries))
	for index, entry := range entries {
		if entry.SeriesIndex != uint32(index) || entry.ExpectedCount != int(snapshot.ExpectedCount) || entry.SeriesHash != snapshot.SeriesHash ||
			strings.TrimSpace(entry.SubjectID) == "" || strings.TrimSpace(entry.Provider) == "" || strings.TrimSpace(entry.SourceID) == "" ||
			strings.TrimSpace(entry.MarketType) == "" || strings.TrimSpace(entry.ProviderSymbol) == "" || entry.SeriesTag == "" {
			return 0, fmt.Errorf("Timer period snapshot has invalid entry at series index %d", index)
		}
		if entry.Provider != entries[0].Provider || entry.MarketType != entries[0].MarketType {
			return 0, fmt.Errorf("Timer period snapshot crosses its static provider or market type")
		}
		subject := strings.ToUpper(strings.TrimSpace(entry.SubjectID))
		if _, exists := entryBySubject[subject]; exists {
			return 0, fmt.Errorf("Timer period snapshot repeats subject %s", subject)
		}
		subjects[index] = subject
		entryBySubject[subject] = entry
	}
	catalog := make(map[int]NodeAssignment)
	groupCount := 0
	for _, assignment := range assignments {
		if assignment.DatasetID != datasetID || assignment.Frequency != frequency {
			continue
		}
		if assignment.GroupCount <= 0 || assignment.GroupID < 0 || assignment.GroupID >= assignment.GroupCount ||
			assignment.RouteVersion != routeVersion || assignment.RouteProvider != entries[0].Provider ||
			assignment.MarketType != entries[0].MarketType || assignment.Provider == "" || assignment.SourceID == "" ||
			assignment.NodeID == "" || assignment.FunctionName == "" || assignment.Region == "" {
			return 0, fmt.Errorf("Timer assignment catalog has an invalid static binding for group %d", assignment.GroupID)
		}
		if groupCount == 0 {
			groupCount = assignment.GroupCount
		}
		if assignment.GroupCount != groupCount {
			return 0, fmt.Errorf("Timer assignment catalog has inconsistent group_count")
		}
		if _, exists := catalog[assignment.GroupID]; exists {
			return 0, fmt.Errorf("Timer assignment catalog repeats group %d", assignment.GroupID)
		}
		catalog[assignment.GroupID] = assignment
	}
	if groupCount == 0 || len(catalog) != groupCount {
		return 0, fmt.Errorf("Timer assignment catalog does not cover all %d groups", groupCount)
	}
	sourceGroups, err := assignStockCNSourceGroups(subjects, sources, groupCount, s.TimerMeasuredSafeGroupSize, routeVersion)
	if err != nil {
		return 0, err
	}
	frozenItems := make([]domain.CollectionItem, 0, len(entries))
	itemBySeriesIndex := make(map[uint32]domain.CollectionItem, len(entries))
	for _, entry := range entries {
		item := domain.CollectionItem{
			SubjectID: entry.SubjectID, Symbol: entry.ProviderSymbol, Provider: entry.Provider, SourceID: entry.SourceID,
			MarketID: StockCNSpaceID, InstrumentType: "equity", MarketType: entry.MarketType, DataType: task.DataType,
			DatasetID: datasetID, Frequency: frequency, OutputFields: append([]string(nil), params.OutputFields...),
			SeriesIndex: entry.SeriesIndex, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		}
		item = prepareCollectionItemRequest(item, frequency, period, MaxRealtimeRows)
		frozenItems = append(frozenItems, item)
		itemBySeriesIndex[item.SeriesIndex] = item
	}
	plans := make([]TimerPeriodPlan, 0, groupCount)
	for groupID, sourceGroup := range sourceGroups {
		if len(sourceGroup.Subjects) == 0 {
			continue
		}
		assignment, ok := catalog[groupID]
		if !ok {
			return 0, fmt.Errorf("Timer assignment catalog is missing group %d", groupID)
		}
		if !timerAssignmentNodePresent(assignment, timerNodes) {
			return 0, nil
		}
		if assignment.Provider != sourceGroup.Source.Provider || assignment.SourceID != sourceGroup.Source.SourceID {
			return 0, fmt.Errorf("Timer assignment group %d differs from the frozen route provider binding", groupID)
		}
		groupSubjects := make(map[string]struct{}, len(sourceGroup.Subjects))
		for _, subject := range sourceGroup.Subjects {
			groupSubjects[strings.ToUpper(strings.TrimSpace(subject))] = struct{}{}
		}
		instances := make([]domain.TaskInstance, 0, len(groupSubjects))
		targets := make([]domain.WriteTarget, 0, len(groupSubjects))
		requestItems := make([]domain.CollectionItem, 0, len(groupSubjects))
		for _, snapshotEntry := range entries {
			subject := strings.ToUpper(strings.TrimSpace(snapshotEntry.SubjectID))
			if _, selected := groupSubjects[subject]; !selected {
				continue
			}
			item := itemBySeriesIndex[snapshotEntry.SeriesIndex]
			item.InstanceID = collectionItemInstanceID(spaceID, runID, task.TaskID, item, frequency, period)
			targetTime, parseErr := time.Parse(time.RFC3339Nano, item.TargetDataTime)
			if parseErr != nil {
				return 0, fmt.Errorf("parse Timer item target_data_time: %w", parseErr)
			}
			requestItem := item
			requestItem.Provider = sourceGroup.Source.Provider
			requestItem.SourceID = sourceGroup.Source.SourceID
			instances = append(instances, domain.TaskInstance{
				SpaceID: spaceID, InstanceID: item.InstanceID, RunID: runID, RequestKey: sharedCollectionItemKey(item, frequency, period),
				Provider: item.Provider, ProviderSymbol: item.Symbol, SourceID: item.SourceID, MarketType: item.MarketType,
				SeriesTag: snapshotEntry.SeriesTag, DataType: item.DataType, SubjectID: item.SubjectID, Frequency: frequency,
				TargetDataTime: &targetTime, TaskParams: collectionItemRequestParams(item),
			})
			targets = append(targets, domain.WriteTarget{
				ID: stableID(spaceID, item.InstanceID, task.TaskID, datasetID), SpaceID: spaceID, InstanceID: item.InstanceID,
				TaskID: task.TaskID, DatasetID: datasetID, ViewID: task.ResultViewID,
				OutputFields: stringSliceJSON(params.OutputFields), SeriesIndex: item.SeriesIndex,
				SeriesHash: item.SeriesHash, ExpectedCount: item.ExpectedCount, Status: "pending",
			})
			requestItems = append(requestItems, requestItem)
		}
		if len(requestItems) == 0 || len(requestItems) > MaxRealtimeItems {
			return 0, fmt.Errorf("Timer assignment group=%d has %d items, exceeds cap %d", assignment.GroupID, len(requestItems), MaxRealtimeItems)
		}
		request := Request{
			BatchKind: domain.BatchKindRealtime, SpaceID: spaceID, MarketID: assignment.MarketID,
			InstrumentType: assignment.InstrumentType, DatasetID: datasetID, Frequency: frequency,
			Provider: firstNonEmpty(assignment.Provider, assignment.RouteProvider), SourceID: assignment.SourceID,
			MarketType: assignment.MarketType, Region: assignment.Region, NodeID: assignment.NodeID,
			FunctionName: assignment.FunctionName, ShardIndex: assignment.GroupID, GroupID: assignment.GroupID,
			GroupCount: assignment.GroupCount, RouteVersion: assignment.RouteVersion, RunID: runID, RequirePeriodCommit: true,
			DNSRoutes: s.dnsSnapshot(ctx), Items: requestItems, Targets: targets,
		}
		plans = append(plans, TimerPeriodPlan{Task: task, RunID: runID, RunCutoff: runCutoff, TaskModifyTime: task.ModifyTime, Snapshot: snapshot, Assignment: assignment,
			Request: request, Instances: instances, Targets: targets})
	}
	if len(plans) == 0 {
		return 0, nil
	}
	ensureStorage := planner.EnsureStorage
	if s.Storage != nil {
		if s.PeriodStorageStates == nil {
			return 0, fmt.Errorf("Timer period Storage state repository is not configured")
		}
		frozenStorageItems := append([]domain.CollectionItem(nil), frozenItems...)
		ensureStorage = func(storageCtx context.Context, frozen domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			if err := s.ensureDatasetPeriod(storageCtx, task, frozenStorageItems, frequency, period, now); err != nil {
				return domain.PeriodStorageState{}, err
			}
			state, found, stateErr := s.PeriodStorageStates.GetPeriodStorageState(storageCtx, frozen.Key)
			if stateErr != nil {
				return domain.PeriodStorageState{}, stateErr
			}
			if !found {
				return domain.PeriodStorageState{}, fmt.Errorf("Storage Ensure did not persist authoritative period state")
			}
			return state, nil
		}
	}
	for index := range plans {
		plans[index].EnsureStorage = ensureStorage
	}
	return planner.PlanMany(ctx, plans)
}

func timerAssignmentNodePresent(assignment NodeAssignment, nodes []scfinvoker.Node) bool {
	for _, node := range nodes {
		if node.NodeID == assignment.NodeID && node.FunctionName == assignment.FunctionName && node.Region == assignment.Region && strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer") {
			return true
		}
	}
	return false
}

func filterTasksForRunCutoff(tasks []domain.CollectionTask, cutoff time.Time) []domain.CollectionTask {
	if cutoff.IsZero() {
		return append([]domain.CollectionTask(nil), tasks...)
	}
	cutoff = cutoff.UTC()
	filtered := make([]domain.CollectionTask, 0, len(tasks))
	for _, task := range tasks {
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
	if !current.Enabled {
		return false, nil
	}
	if !cutoff.IsZero() {
		if (!current.CreateTime.IsZero() && current.CreateTime.UTC().After(cutoff.UTC())) ||
			(!current.ModifyTime.IsZero() && current.ModifyTime.UTC().After(cutoff.UTC())) {
			return false, nil
		}
	}
	return strings.TrimSpace(current.DefinitionHash) == strings.TrimSpace(snapshot.DefinitionHash), nil
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
			Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: dnsRoutes, RequirePeriodCommit: true,
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
		bindRequestTargetPeriodIdentity(req)
	} else {
		enabled, err := s.taskEnabled(ctx, req.SpaceID, task.TaskID)
		if err != nil {
			return false, fmt.Errorf("check collection task before planning: %w", err)
		}
		if !enabled {
			return false, nil
		}
	}
	periodTime, periodDeadlineAt, err := s.periodPriorityMetadata(ctx, *req)
	if err != nil {
		return false, fmt.Errorf("resolve market fetch period deadline: %w", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	if _, err := marketFetchEvent(*req); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	batch := &domain.BatchInvocation{SpaceID: req.SpaceID, BatchID: req.BatchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: req.ShardIndex, Frequency: req.Frequency, PeriodTime: periodTime, PeriodDeadlineAt: periodDeadlineAt, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: len(req.Items), PlannedAt: &now, DeadlineAt: timePtr(now.Add(batchCompletionDeadline(req.BatchKind)))}
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

func (s *Scheduler) periodPriorityMetadata(ctx context.Context, request Request) (*time.Time, *time.Time, error) {
	if len(request.Items) == 0 {
		return nil, nil, nil
	}
	period, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(request.Items[0].TargetDataTime))
	if err != nil || period.IsZero() {
		return nil, nil, nil
	}
	period = period.UTC()
	for _, item := range request.Items[1:] {
		other, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.TargetDataTime))
		if parseErr == nil && !other.UTC().Equal(period) {
			return nil, nil, fmt.Errorf("one fetch batch contains multiple target periods")
		}
	}
	deadline := period.Add(datasetPeriodCompletionWindow(request.BatchKind))
	if s.PeriodStorageStates != nil {
		state, found, stateErr := s.PeriodStorageStates.GetPeriodStorageState(ctx, domain.PeriodKey{
			SpaceID: strings.TrimSpace(request.SpaceID), DatasetID: firstNonEmpty(request.DatasetID, request.Items[0].DatasetID),
			Frequency: strings.TrimSpace(request.Frequency), PeriodTime: period,
		})
		if stateErr != nil {
			return nil, nil, stateErr
		}
		if found {
			if state.DeadlineAt.IsZero() {
				return nil, nil, fmt.Errorf("Storage period state has no deadline")
			}
			deadline = state.DeadlineAt.UTC()
		} else if s.Storage != nil {
			return nil, nil, fmt.Errorf("authoritative Storage period state is missing")
		}
	}
	return &period, &deadline, nil
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
	s.ensureInvokeSemaphore()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case s.invokeSem <- struct{}{}:
		defer func() { <-s.invokeSem }()
	case <-ctx.Done():
		return
	}
	batch, err := s.Batches.Get(ctx, req.SpaceID, req.BatchID)
	if err != nil || batch.Status != domain.BatchStatusPlanned {
		return
	}
	var persistedRequest Request
	if err := json.Unmarshal([]byte(batch.RequestJSON), &persistedRequest); err != nil || persistedRequest.BatchID != batch.BatchID || persistedRequest.SpaceID != batch.SpaceID {
		log.WarnContextf(ctx, "decode persisted market fetch request before dispatch failed batch=%s err=%v", req.BatchID, err)
		return
	}
	req = persistedRequest
	for attempt, candidate := range invocationCandidates(node, nodes) {
		event, err := marketFetchEvent(requestForNode(req, candidate))
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
		if updated, err := s.Batches.MarkDispatchedToNode(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(batchCompletionDeadline(req.BatchKind)), candidate.Region, candidate.NodeID, candidate.FunctionName); err != nil {
			log.WarnContextf(ctx, "mark market fetch batch dispatched failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
		} else if updated {
			s.Metrics.ObserveSchedulerSuccess(req.SpaceID, "dispatch", time.Now().UTC())
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

// MinRealtimeBatchSize 是实时批次的最小标的数。腾讯云 SCF Invoke 接口在每分钟开头的
// 并发突发下会排队，单次调用偶尔超过 10 秒；按函数数平均分摊时每批只有约 7 个标的，
// 每分钟调用次数翻倍。批次不小于 15 个标的，调用次数随之减半。
const MinRealtimeBatchSize = 15

// realtimeBatchSize spreads one minute's work across the current SCF fleet with
// at least MinRealtimeBatchSize items per batch, then applies the smallest
// per-function limit declared by that fleet so a stale or deliberately smaller
// function configuration never receives an oversized event.
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
	return min(max(perNode, MinRealtimeBatchSize), limit, itemCount)
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

// RunMaintenance performs scheduler-owned database reconciliation outside the
// foreground Tick path. The process-level maintenance runner calls this with a
// bounded context and serializes it with retention and period cleanup.
func (s *Scheduler) RunMaintenance(ctx context.Context, spaceID string, disabledTargetLimit int) (int64, error) {
	if s == nil {
		return 0, nil
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		spaceID = strings.TrimSpace(s.SpaceID)
	}
	if spaceID == "" {
		return 0, fmt.Errorf("space_id is required")
	}
	var maintenanceErrors []error
	var prunedTargets int64
	if s.InvokeNonRealtimeOnly && s.TimerPeriodBatches != nil {
		if _, err := s.TimerPeriodBatches.CancelStaleUnclaimed(ctx, spaceID); err != nil {
			maintenanceErrors = append(maintenanceErrors, fmt.Errorf("cancel stale unclaimed Timer batches: %w", err))
		}
	}
	if ctx.Err() != nil {
		return prunedTargets, errors.Join(append(maintenanceErrors, ctx.Err())...)
	}
	if s.Instances != nil {
		if pruned, err := s.Instances.PruneDisabledWriteTargets(ctx, spaceID, disabledTargetLimit); err != nil {
			maintenanceErrors = append(maintenanceErrors, fmt.Errorf("prune disabled collector write targets: %w", err))
		} else if prunedTargets = pruned; prunedTargets > 0 {
			log.InfoContextf(ctx, "pruned disabled collector write targets space=%s count=%d", spaceID, pruned)
		}
	}
	if ctx.Err() != nil {
		return prunedTargets, errors.Join(append(maintenanceErrors, ctx.Err())...)
	}
	if s.Runs != nil {
		if err := s.Runs.ReconcileOpenRuns(ctx, spaceID); err != nil {
			maintenanceErrors = append(maintenanceErrors, fmt.Errorf("reconcile previous collector runs: %w", err))
		}
	}
	return prunedTargets, errors.Join(maintenanceErrors...)
}

func (s *Scheduler) recoverDue(ctx context.Context, spaceID string, nodes []scfinvoker.Node, now time.Time) error {
	_ = nodes
	if s.Retries == nil {
		return nil
	}
	currentLimit := s.maintenanceRecoveryBatchLimit()
	recent, _, err := s.Batches.ListDuePrioritized(ctx, spaceID, now, currentLimit, 0)
	if err != nil {
		return err
	}
	historicalLimit := s.historicalBatchLimit() + max(0, currentLimit-len(recent))
	_, historical, err := s.Batches.ListDuePrioritized(ctx, spaceID, now, 0, historicalLimit)
	if err != nil {
		return err
	}
	due := append(recent, historical...)
	recoveryErrors := make([]error, 0)
	for _, batch := range due {
		batch.Status = domain.BatchStatusTimedOut
		batch.CompletedAt = &now
		batch.ErrorSummary = "SCF completion event deadline exceeded"
		var request Request
		if err := json.Unmarshal([]byte(batch.RequestJSON), &request); err != nil {
			poisonErr := fmt.Errorf("malformed timeout-recovery request for batch %s: %w", batch.BatchID, err)
			if quarantineErr := s.quarantineRecoveryBatch(ctx, &batch, now, poisonErr); quarantineErr != nil {
				recoveryErrors = append(recoveryErrors, quarantineErr)
			}
			recoveryErrors = append(recoveryErrors, poisonErr)
			continue
		}
		if err := request.validate(); err != nil {
			poisonErr := fmt.Errorf("invalid timeout-recovery request for batch %s: %w", batch.BatchID, err)
			if quarantineErr := s.quarantineRecoveryBatch(ctx, &batch, now, poisonErr); quarantineErr != nil {
				recoveryErrors = append(recoveryErrors, quarantineErr)
			}
			recoveryErrors = append(recoveryErrors, poisonErr)
			continue
		}
		identityError := error(nil)
		switch {
		case request.BatchID != batch.BatchID:
			identityError = fmt.Errorf("timeout-recovery request batch_id differs from stored batch %s", batch.BatchID)
		case request.SpaceID != batch.SpaceID:
			identityError = fmt.Errorf("timeout-recovery request space_id differs from stored batch %s", batch.BatchID)
		case request.BatchKind != batch.BatchKind:
			identityError = fmt.Errorf("timeout-recovery request batch_kind differs from stored batch %s", batch.BatchID)
		case request.Frequency != batch.Frequency:
			identityError = fmt.Errorf("timeout-recovery request frequency differs from stored batch %s", batch.BatchID)
		}
		if identityError != nil {
			poisonErr := identityError
			if quarantineErr := s.quarantineRecoveryBatch(ctx, &batch, now, poisonErr); quarantineErr != nil {
				recoveryErrors = append(recoveryErrors, quarantineErr)
			}
			recoveryErrors = append(recoveryErrors, poisonErr)
			continue
		}
		logicalSyncPointID := strings.TrimSpace(request.SyncPointID)
		if logicalSyncPointID == "" {
			logicalSyncPointID = batch.BatchID
		}
		effects := store.FetchCompletionEffects{}
		retryScope, writeTargetID := "fetch", ""
		if batch.RetryScope == "write_target" {
			retryScope, writeTargetID = batch.RetryScope, batch.WriteTargetID
		}
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
			effects.Retries = append(effects.Retries, &domain.RetryItem{SpaceID: spaceID, RetryKey: key, SourceBatchID: logicalSyncPointID, BatchKind: batch.BatchKind, InstanceID: item.InstanceID, WriteTargetID: writeTargetID, RetryScope: retryScope, SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: target, PeriodTime: batch.PeriodTime, PeriodDeadlineAt: batch.PeriodDeadlineAt, TaskJSON: string(raw), FailureTargetsJSON: retryFailureTargetsJSON(request, item.InstanceID, writeTargetID), Attempt: batch.Attempt, Status: "pending", NextRetryAt: &next, LastErrorType: "scf_completion_timeout", LastErrorSummary: "SCF completion event deadline exceeded", CreateTime: now, ModifyTime: now})
		}
		updated, err := s.Batches.CompleteWithEffects(ctx, &batch, effects)
		if err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("recover timeout for batch %s: %w", batch.BatchID, err))
			if errors.Is(err, store.ErrInvalidCompletionScope) {
				if quarantineErr := s.quarantineRecoveryBatch(ctx, &batch, now, err); quarantineErr != nil {
					recoveryErrors = append(recoveryErrors, quarantineErr)
				}
			}
			continue
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
		}
	}
	return errors.Join(recoveryErrors...)
}

func (s *Scheduler) quarantineRecoveryBatch(ctx context.Context, batch *domain.BatchInvocation, now time.Time, cause error) error {
	if batch == nil || s.Batches == nil {
		return errors.New("cannot quarantine timeout-recovery batch without batch storage")
	}
	batch.Status = domain.BatchStatusFailed
	batch.CompletedAt = &now
	batch.ErrorSummary = fmt.Sprintf("timeout recovery quarantined invalid batch request: %v", cause)
	updated, err := s.Batches.Complete(ctx, batch)
	if err != nil {
		return fmt.Errorf("quarantine timeout-recovery batch %s: %w", batch.BatchID, err)
	}
	if updated && s.Metrics != nil {
		duration := int64(0)
		if batch.DispatchedAt != nil && !batch.DispatchedAt.IsZero() {
			duration = now.Sub(batch.DispatchedAt.UTC()).Milliseconds()
		} else if batch.PlannedAt != nil && !batch.PlannedAt.IsZero() {
			duration = now.Sub(batch.PlannedAt.UTC()).Milliseconds()
		}
		s.Metrics.Observe(batch.SpaceID, &marketfetchpb.MarketFetchBatchCompleted{
			BatchId: batch.BatchID, Frequency: batch.Frequency, Status: "failed",
			DurationMs: duration, CompletedAt: timestamppb.New(now.UTC()),
		})
	}
	return nil
}

func (s *Scheduler) dispatchDueRetries(ctx context.Context, spaceID string, nodes []scfinvoker.Node, now time.Time) error {
	if s.Retries == nil {
		return nil
	}
	excludedRetryKeys := s.retryKeysInFlight(spaceID)
	currentRetryLimit := s.maintenanceBatchLimit() * MaxRealtimeItems
	recentDue, historicalDue, err := s.Retries.ListDuePrioritized(
		ctx, spaceID, now,
		currentRetryLimit,
		0,
		excludedRetryKeys...,
	)
	if err != nil {
		return err
	}
	historicalRetryLimit := s.historicalBatchLimit()*MaxRealtimeItems + max(0, currentRetryLimit-len(recentDue))
	_, historicalDue, err = s.Retries.ListDuePrioritized(ctx, spaceID, now, 0, historicalRetryLimit, excludedRetryKeys...)
	if err != nil {
		return err
	}
	due := append(append([]domain.RetryItem(nil), recentDue...), historicalDue...)
	maxAttempts := s.MaxRetryAttempts
	if maxAttempts <= 0 || maxAttempts > maxRetryAttempts() {
		maxAttempts = maxRetryAttempts()
	}
	markPermanent := func(retryKey, reason, summary string) error {
		if err := s.Retries.MarkPermanent(ctx, spaceID, retryKey, reason, summary); err != nil {
			return err
		}
		if s.WakePeriodFailureReporter != nil {
			s.WakePeriodFailureReporter()
		}
		return nil
	}
	if len(nodes) == 0 {
		queued := 0
		for _, retry := range due {
			if retry.Attempt > maxAttempts {
				if err := markPermanent(retry.RetryKey, "retry_budget_exhausted", retry.LastErrorSummary); err != nil {
					return err
				}
				continue
			}
			queued++
		}
		if queued > 0 {
			log.WarnContextf(ctx, "market fetch retries remain queued because no Invoke or Timer capacity is available space=%s count=%d", spaceID, queued)
		}
		return nil
	}
	candidates := make([]retryDispatchCandidate, 0, len(due))
	instanceIDs := make([]string, 0, len(due))
	for _, cohort := range []struct {
		retries    []domain.RetryItem
		historical bool
	}{{retries: recentDue}, {retries: historicalDue, historical: true}} {
		for _, retry := range cohort.retries {
			if retry.Attempt > maxAttempts {
				if err := markPermanent(retry.RetryKey, "retry_budget_exhausted", retry.LastErrorSummary); err != nil {
					return err
				}
				continue
			}
			var item domain.CollectionItem
			if err := json.Unmarshal([]byte(retry.TaskJSON), &item); err != nil {
				if markErr := markPermanent(retry.RetryKey, "invalid_retry_payload", err.Error()); markErr != nil {
					return markErr
				}
				continue
			}
			item.SourceEventID = retry.RetryKey
			instanceID := strings.TrimSpace(retry.InstanceID)
			if instanceID == "" {
				instanceID = strings.TrimSpace(item.InstanceID)
			}
			if instanceID == "" || s.Instances == nil {
				if err := markPermanent(retry.RetryKey, "invalid_retry_identity", "retry instance is unavailable"); err != nil {
					return err
				}
				continue
			}
			item.InstanceID = instanceID
			candidates = append(candidates, retryDispatchCandidate{retry: retry, item: item, historical: cohort.historical})
			instanceIDs = append(instanceIDs, instanceID)
		}
	}
	targetsByInstance, err := s.listEnabledRetryTargets(ctx, spaceID, instanceIDs)
	if err != nil {
		return err
	}
	groups := make([]retryDispatchGroup, 0, s.totalMaintenanceBatchLimit())
	lastGroupByKey := make(map[string]int)
	recentGroupCount, historicalGroupCount := 0, 0
	for _, candidate := range candidates {
		retry, item := candidate.retry, candidate.item
		targets := targetsByInstance[item.InstanceID]
		if retry.RetryScope == "write_target" {
			filtered := make([]domain.WriteTarget, 0, len(targets))
			for _, target := range targets {
				if target.ID == retry.WriteTargetID {
					filtered = append(filtered, target)
				}
			}
			targets = filtered
		}
		if len(targets) == 0 {
			if err := markPermanent(retry.RetryKey, "retry_target_unavailable", "no enabled write target remains"); err != nil {
				return err
			}
			continue
		}
		batchKind := retry.BatchKind
		if batchKind == "" {
			batchKind = domain.BatchKindRealtime
		}
		scope := firstNonEmpty(retry.RetryScope, "fetch")
		key := retryDispatchCompatibilityKey(retry, item, targets, scope, batchKind)
		if item.RequirePeriodCommit {
			key += "\x00period-commit:true"
		}
		cohortKey := key
		if candidate.historical {
			cohortKey += "\x00historical"
		}
		groupIndex, exists := lastGroupByKey[cohortKey]
		maxItems := MaxRealtimeItems
		if batchKind != domain.BatchKindRealtime {
			maxItems = 1
		}
		if exists {
			group := &groups[groupIndex]
			_, duplicateInstance := group.seenInstances[item.InstanceID]
			if len(group.items) >= maxItems || duplicateInstance {
				exists = false
			}
		}
		if !exists {
			groupLimit := s.maintenanceBatchLimit()
			if candidate.historical {
				groupLimit = s.historicalBatchLimit() + max(0, s.maintenanceBatchLimit()-recentGroupCount)
			}
			groupCount := recentGroupCount
			if candidate.historical {
				groupCount = historicalGroupCount
			}
			if groupCount >= groupLimit {
				continue
			}
			groupIndex = len(groups)
			groups = append(groups, retryDispatchGroup{
				key: key, batchKind: batchKind, requirePeriodCommit: item.RequirePeriodCommit, historical: candidate.historical, attempt: retry.Attempt, retryScope: scope,
				periodTime: retry.PeriodTime, periodDeadlineAt: retry.PeriodDeadlineAt,
				writeTargetID: retry.WriteTargetID, seenInstances: make(map[string]struct{}),
			})
			lastGroupByKey[cohortKey] = groupIndex
			if candidate.historical {
				historicalGroupCount++
			} else {
				recentGroupCount++
			}
		}
		group := &groups[groupIndex]
		group.retries = append(group.retries, retry)
		group.items = append(group.items, item)
		group.instanceIDs = append(group.instanceIDs, item.InstanceID)
		group.seenInstances[item.InstanceID] = struct{}{}
		group.targets = append(group.targets, targets...)
	}
	if len(groups) > 0 {
		s.ensureInvokeSemaphore()
	}
	for index := range groups {
		group := groups[index]
		if len(group.items) == 0 {
			continue
		}
		node := nodes[index%len(nodes)]
		retryKeys := make([]string, len(group.retries))
		parts := []string{spaceID, "retry"}
		for retryIndex, retry := range group.retries {
			retryKeys[retryIndex] = retry.RetryKey
		}
		sort.Strings(retryKeys)
		if len(retryKeys) == 1 {
			parts = append(parts, retryKeys[0], fmt.Sprintf("%d", group.attempt+1))
		} else {
			parts = append(parts, group.key, fmt.Sprintf("%d", group.attempt+1))
			parts = append(parts, retryKeys...)
		}
		batchID := stableID(parts...)
		req := Request{
			BatchID: batchID, SyncPointID: group.retries[0].SourceBatchID, ScheduleID: "retry:" + batchID,
			BatchKind: group.batchKind, SpaceID: spaceID, DatasetID: group.targets[0].DatasetID,
			Frequency: group.items[0].Frequency, Provider: group.items[0].Provider, SourceID: group.items[0].SourceID,
			MarketID: group.items[0].MarketID, InstrumentType: group.items[0].InstrumentType, MarketType: group.items[0].MarketType,
			Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: s.dnsSnapshot(ctx), RequirePeriodCommit: group.requirePeriodCommit,
			ShardIndex: index, Items: group.items, Targets: group.targets,
		}
		bindRequestTargetPeriodIdentity(&req)
		if err := req.validate(); err != nil {
			for _, retryKey := range retryKeys {
				if markErr := markPermanent(retryKey, "invalid_retry_request", err.Error()); markErr != nil {
					return markErr
				}
			}
			continue
		}
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if _, err := marketFetchEvent(req); err != nil {
			for _, retryKey := range retryKeys {
				if markErr := markPermanent(retryKey, "invalid_retry_request", err.Error()); markErr != nil {
					return markErr
				}
			}
			continue
		}
		instanceID := ""
		if len(group.instanceIDs) == 1 {
			instanceID = group.instanceIDs[0]
		}
		queueDeadlineStart := time.Now().UTC()
		batch := &domain.BatchInvocation{
			SpaceID: spaceID, BatchID: batchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: index,
			InstanceID: instanceID, WriteTargetID: group.writeTargetID, RetryScope: group.retryScope,
			Frequency: req.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName,
			Status: domain.BatchStatusPlanned, Attempt: group.attempt + 1, RequestJSON: string(raw), PlannedCount: len(group.items),
			PeriodTime: group.periodTime, PeriodDeadlineAt: group.periodDeadlineAt,
			PlannedAt: &now, DeadlineAt: timePtr(queueDeadlineStart.Add(retryDispatchWaitTimeout(cap(s.invokeSem), s.totalMaintenanceBatchLimit()) + retryDispatchScheduleSlack)),
		}
		if !s.beginRetryDispatch(spaceID, batchID, retryKeys) {
			continue
		}
		created, err := s.Batches.CreatePlannedWithItemsForEnabledTargets(ctx, batch, group.instanceIDs)
		if err != nil {
			s.endRetryDispatch(spaceID, batchID, retryKeys)
			return err
		}
		if !created {
			persisted, getErr := s.Batches.Get(ctx, spaceID, batchID)
			if errors.Is(getErr, gorm.ErrRecordNotFound) {
				s.endRetryDispatch(spaceID, batchID, retryKeys)
				continue
			}
			if getErr != nil {
				s.endRetryDispatch(spaceID, batchID, retryKeys)
				return getErr
			}
			if persisted.Status != domain.BatchStatusPlanned {
				s.endRetryDispatch(spaceID, batchID, retryKeys)
				continue
			}
			if decodeErr := json.Unmarshal([]byte(persisted.RequestJSON), &req); decodeErr != nil {
				s.endRetryDispatch(spaceID, batchID, retryKeys)
				return fmt.Errorf("decode persisted retry batch %s: %w", batchID, decodeErr)
			}
		}
		if !created {
			deadline := time.Now().UTC().Add(retryDispatchWaitTimeout(cap(s.invokeSem), s.totalMaintenanceBatchLimit()) + retryDispatchScheduleSlack)
			refreshed, refreshErr := s.Batches.RefreshPlannedRetryDeadline(ctx, spaceID, batchID, deadline)
			if refreshErr != nil {
				s.endRetryDispatch(spaceID, batchID, retryKeys)
				return refreshErr
			}
			if !refreshed {
				s.endRetryDispatch(spaceID, batchID, retryKeys)
				continue
			}
		}
		go func(req Request, node scfinvoker.Node, nodes []scfinvoker.Node, retryKeys []string, batchID, spaceID string) {
			defer s.endRetryDispatch(spaceID, batchID, retryKeys)
			s.dispatchRetry(req, node, nodes, retryKeys)
		}(req, node, nodes, retryKeys, batchID, spaceID)
	}
	return nil
}

func (s *Scheduler) listEnabledRetryTargets(ctx context.Context, spaceID string, instanceIDs []string) (map[string][]domain.WriteTarget, error) {
	targetsByInstance := make(map[string][]domain.WriteTarget)
	instanceIDs = uniqueStrings(instanceIDs)
	for start := 0; start < len(instanceIDs); start += retryTargetLookupBatchSize {
		end := min(start+retryTargetLookupBatchSize, len(instanceIDs))
		targets, err := s.Instances.ListEnabledWriteTargetsForInstances(ctx, spaceID, instanceIDs[start:end])
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			targetsByInstance[target.InstanceID] = append(targetsByInstance[target.InstanceID], target)
		}
	}
	return targetsByInstance, nil
}

func retryDispatchCompatibilityKey(retry domain.RetryItem, item domain.CollectionItem, targets []domain.WriteTarget, scope string, kind domain.BatchKind) string {
	datasets := make([]string, 0, len(targets))
	for _, target := range targets {
		datasets = append(datasets, target.DatasetID)
	}
	sort.Strings(datasets)
	datasets = uniqueStrings(datasets)
	sourceBatchID := strings.TrimSpace(retry.SourceBatchID)
	if sourceBatchID == "" {
		sourceBatchID = retry.RetryKey
	}
	periodTime, periodDeadlineAt := "", ""
	if retry.PeriodTime != nil {
		periodTime = retry.PeriodTime.UTC().Format(time.RFC3339Nano)
	}
	if retry.PeriodDeadlineAt != nil {
		periodDeadlineAt = retry.PeriodDeadlineAt.UTC().Format(time.RFC3339Nano)
	}
	return stableID(
		sourceBatchID, string(kind), fmt.Sprintf("%d", retry.Attempt), scope, retry.WriteTargetID,
		item.Provider, item.SourceID, item.MarketID, item.InstrumentType, item.MarketType, item.DataType,
		item.DatasetID, item.Frequency, periodTime, periodDeadlineAt, fmt.Sprintf("%d", item.CandidateIndex), strings.Join(datasets, "\x00"),
	)
}

func uniqueSCFNodes(nodes []scfinvoker.Node) []scfinvoker.Node {
	seen := make(map[string]struct{}, len(nodes))
	result := make([]scfinvoker.Node, 0, len(nodes))
	for _, node := range nodes {
		nodeID := strings.TrimSpace(node.NodeID)
		if nodeID == "" {
			continue
		}
		if _, exists := seen[nodeID]; exists {
			continue
		}
		seen[nodeID] = struct{}{}
		result = append(result, node)
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Scheduler) dispatchRetry(req Request, node scfinvoker.Node, nodes []scfinvoker.Node, retryKeys []string) {
	s.ensureInvokeSemaphore()
	ctx, cancel := context.WithTimeout(context.Background(), s.retryDispatchWaitBudget())
	defer cancel()
	select {
	case s.invokeSem <- struct{}{}:
		defer func() { <-s.invokeSem }()
	case <-ctx.Done():
		log.WarnContextf(ctx, "market fetch retry remains planned and queued because Invoke capacity wait timed out batch=%s", req.BatchID)
		return
	}
	batch, err := s.Batches.Get(ctx, req.SpaceID, req.BatchID)
	if err != nil || batch.Status != domain.BatchStatusPlanned {
		return
	}
	if len(retryKeys) > 0 {
		eligible, prepareErr := s.Batches.PrepareRetryBatchDispatch(ctx, req.SpaceID, req.BatchID, retryKeys)
		if prepareErr != nil {
			log.WarnContextf(ctx, "validate market fetch retry keys before invoke failed batch=%s err=%v", req.BatchID, prepareErr)
			return
		}
		if !eligible {
			return
		}
	}
	attemptTimeout := s.invokeAttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = defaultSCFInvokeAttemptTimeout
	}
	for attempt, candidate := range invocationCandidates(node, nodes) {
		event, err := marketFetchEvent(requestForNode(req, candidate))
		if err != nil {
			log.WarnContextf(ctx, "build SCF market fetch retry failover event failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
			return
		}
		invokeCtx, invokeCancel := context.WithTimeout(ctx, attemptTimeout)
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
		var updated bool
		if len(retryKeys) > 0 {
			updated, err = s.Batches.MarkRetryBatchDispatched(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(batchCompletionDeadline(req.BatchKind)), candidate.Region, candidate.NodeID, candidate.FunctionName, retryKeys)
		} else {
			updated, err = s.Batches.MarkDispatchedToNode(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(batchCompletionDeadline(req.BatchKind)), candidate.Region, candidate.NodeID, candidate.FunctionName)
		}
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
		s.Metrics.ObserveSchedulerSuccess(req.SpaceID, "dispatch", time.Now().UTC())
		if attempt > 0 {
			log.InfoContextf(ctx, "SCF market fetch retry failover succeeded batch=%s node=%s", req.BatchID, candidate.NodeID)
		}
		return
	}
	log.WarnContextf(ctx, "SCF market fetch retry invoke exhausted failover batch=%s original_node=%s", req.BatchID, node.NodeID)
	forceCtx, forceCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer forceCancel()
	forced, forceErr := s.Batches.MakePlannedRetryBatchDue(forceCtx, req.SpaceID, req.BatchID, time.Now().UTC())
	if forceErr != nil {
		log.WarnContextf(forceCtx, "mark failed Invoke retry batch due for recovery failed batch=%s err=%v", req.BatchID, forceErr)
	} else if forced {
		log.WarnContextf(forceCtx, "all Invoke candidates failed; retry batch queued for bounded recovery batch=%s", req.BatchID)
	}
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

func (s *Scheduler) taskPeriodDefinition(task domain.CollectionTask) (*domain.CollectParams, string, []string, error) {
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return nil, "", nil, err
	}
	dataType := strings.ToLower(firstNonEmpty(params.Collector.DataType, task.DataType))
	if dataType != "kline" {
		return nil, "", nil, fmt.Errorf("unsupported data_type %q", dataType)
	}
	targetDataset := firstNonEmpty(params.Target.DatasetID, task.ResultDatasetID)
	if strings.TrimSpace(targetDataset) == "" {
		return nil, "", nil, fmt.Errorf("target dataset is required")
	}
	frequencies := append([]string(nil), params.Collector.Intervals...)
	if len(frequencies) == 0 && params.Schedule.Interval != "" {
		frequencies = []string{params.Schedule.Interval}
	}
	if len(frequencies) == 0 {
		return nil, "", nil, fmt.Errorf("collection task %s has no frequency", task.TaskID)
	}
	return params, targetDataset, frequencies, nil
}

func (s *Scheduler) taskFrequencies(task domain.CollectionTask) ([]string, error) {
	_, _, frequencies, err := s.taskPeriodDefinition(task)
	return frequencies, err
}

func periodSeriesSnapshotFromItems(task domain.CollectionTask, datasetID, frequency string, period time.Time, items []domain.CollectionItem) (domain.PeriodSeriesSnapshot, error) {
	if len(items) == 0 {
		return domain.PeriodSeriesSnapshot{}, fmt.Errorf("collection task %s expands to no series", task.TaskID)
	}
	if _, err := marketdata.ParseFrequency(frequency); err != nil {
		return domain.PeriodSeriesSnapshot{}, fmt.Errorf("collection task %s has invalid period frequency %q: %w", task.TaskID, frequency, err)
	}
	periodFrequency := strings.TrimSpace(frequency)
	snapshot := domain.PeriodSeriesSnapshot{
		Key:        domain.PeriodKey{SpaceID: task.SpaceID, DatasetID: datasetID, Frequency: periodFrequency, PeriodTime: period.UTC()},
		SeriesHash: items[0].SeriesHash, ExpectedCount: uint32(len(items)),
		Entries: make([]domain.PeriodSeriesSnapshotEntry, 0, len(items)),
	}
	for _, item := range items {
		if item.SeriesHash == "" || item.ExpectedCount != uint32(len(items)) {
			return domain.PeriodSeriesSnapshot{}, fmt.Errorf("collection task %s has invalid materialized series expectation", task.TaskID)
		}
		seriesTag := collectionItemSeriesTag(task.SpaceID, item)
		snapshot.Entries = append(snapshot.Entries, domain.PeriodSeriesSnapshotEntry{
			SpaceID: snapshot.Key.SpaceID, DatasetID: snapshot.Key.DatasetID, Frequency: snapshot.Key.Frequency, PeriodTime: snapshot.Key.PeriodTime,
			SeriesIndex: item.SeriesIndex, SeriesKey: domain.CanonicalSeriesKey(item.Provider, item.SourceID, item.MarketType, item.SubjectID, seriesTag),
			SubjectID: item.SubjectID, Provider: item.Provider, SourceID: item.SourceID, MarketType: item.MarketType,
			ProviderSymbol: item.Symbol, SeriesTag: seriesTag, SeriesHash: item.SeriesHash, ExpectedCount: int(item.ExpectedCount),
		})
	}
	return snapshot, nil
}

func (s *Scheduler) prepareCurrentPeriodSeriesSnapshots(ctx context.Context, now time.Time, tasks []domain.CollectionTask, cache *taskExpansionCache) error {
	// Unit tests that do not wire persistence retain the previous in-memory
	// behavior. Production bootstrap always provides PeriodSeriesSnapshot.
	if s.PeriodSeriesSnapshot == nil {
		for _, task := range tasks {
			if _, _, err := s.expandTaskWithCache(ctx, task, cache); err != nil {
				log.WarnContextf(ctx, "collection task series refresh failed task=%s: %v", task.TaskID, err)
			}
		}
		return nil
	}
	for _, task := range tasks {
		_, datasetID, frequencies, err := s.taskPeriodDefinition(task)
		if err != nil {
			log.WarnContextf(ctx, "skip invalid collection task before period freeze task=%s: %v", task.TaskID, err)
			continue
		}
		type missingPeriod struct {
			frequency string
			period    time.Time
		}
		missing := make([]missingPeriod, 0, len(frequencies))
		for _, frequency := range frequencies {
			period, periodErr := targetDataTime(now, frequency)
			if periodErr != nil {
				log.WarnContextf(ctx, "skip invalid collection period task=%s frequency=%s: %v", task.TaskID, frequency, periodErr)
				continue
			}
			_, found, readErr := s.PeriodSeriesSnapshot.GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: task.SpaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period})
			if readErr != nil {
				return fmt.Errorf("load period series snapshot task=%s frequency=%s: %w", task.TaskID, frequency, readErr)
			}
			if !found {
				missing = append(missing, missingPeriod{frequency: frequency, period: period})
			}
		}
		if len(missing) == 0 {
			continue
		}
		items, _, expandErr := s.expandTaskWithCache(ctx, task, cache)
		if expandErr != nil {
			// One malformed/empty task must not stop unrelated tasks. Existing
			// periods remain usable because they never reach this branch.
			log.WarnContextf(ctx, "collection task period series snapshot creation deferred task=%s: %v", task.TaskID, expandErr)
			continue
		}
		for _, entry := range missing {
			snapshot, snapshotErr := periodSeriesSnapshotFromItems(task, datasetID, entry.frequency, entry.period, items)
			if snapshotErr != nil {
				log.WarnContextf(ctx, "skip invalid period series snapshot task=%s frequency=%s: %v", task.TaskID, entry.frequency, snapshotErr)
				continue
			}
			if _, _, createErr := s.PeriodSeriesSnapshot.CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot); createErr != nil {
				if errors.Is(createErr, store.ErrPeriodSeriesSnapshotConflict) {
					log.WarnContextf(ctx, "period series snapshot race kept existing immutable winner task=%s dataset=%s frequency=%s period=%s", task.TaskID, datasetID, entry.frequency, entry.period.UTC().Format(time.RFC3339))
					continue
				}
				return fmt.Errorf("create period series snapshot task=%s frequency=%s: %w", task.TaskID, entry.frequency, createErr)
			}
		}
	}
	return nil
}

func (s *Scheduler) expandTaskForPeriod(ctx context.Context, task domain.CollectionTask, frequency string, period time.Time) ([]domain.CollectionItem, error) {
	if s.PeriodSeriesSnapshot == nil {
		items, _, err := s.expandTaskForPlanning(ctx, task)
		return items, err
	}
	params, datasetID, _, err := s.taskPeriodDefinition(task)
	if err != nil {
		return nil, err
	}
	snapshot, found, err := s.PeriodSeriesSnapshot.GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: task.SpaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period})
	if err != nil {
		return nil, fmt.Errorf("load immutable period series snapshot: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("immutable period series snapshot is missing")
	}
	marketID := strings.ToLower(firstNonEmpty(params.MarketID, task.SpaceID, s.SpaceID))
	dataType := strings.ToLower(firstNonEmpty(params.Collector.DataType, task.DataType))
	items := make([]domain.CollectionItem, 0, len(snapshot.Entries))
	for index, row := range snapshot.Entries {
		if row.SeriesIndex != uint32(index) || uint32(row.ExpectedCount) != snapshot.ExpectedCount || row.SeriesHash != snapshot.SeriesHash {
			return nil, fmt.Errorf("immutable period series snapshot failed dense/hash/count validation")
		}
		instrumentType := strings.ToLower(firstNonEmpty(params.InstrumentType, defaultInstrumentTypeForMarket(marketID, row.MarketType)))
		items = append(items, domain.CollectionItem{
			SubjectID: row.SubjectID, Symbol: row.ProviderSymbol, Provider: row.Provider, SourceID: row.SourceID,
			MarketID: marketID, InstrumentType: instrumentType, MarketType: row.MarketType, DataType: dataType,
			DatasetID: row.DatasetID, Frequency: frequency, OutputFields: append([]string(nil), params.OutputFields...),
			SeriesIndex: row.SeriesIndex, SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
		})
	}
	return items, nil
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
	return frequencypkg.Normalize(frequency)
}

func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:])[:32]
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

func marketFetchEvent(req Request) (map[string]any, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	event := map[string]any{
		"action":     "market_fetch",
		"source":     model.EventSourceCollectorScheduler,
		"request_id": req.BatchID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
		"data":       data,
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
