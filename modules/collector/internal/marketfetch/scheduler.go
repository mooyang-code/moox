package marketfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultBatchSize = MaxRealtimeItems
	DefaultMaxPlan   = 1000
	// 80 items / 16 workers yields at most five 1.5-second storage-read waves,
	// leaving room inside the fixed 10-second tRPC timer deadline.
	gapAuditPageSize = 40
	gapAuditWorkers  = 4
	gapAuditInterval = 5 * time.Minute
)

type scheduleState struct {
	target      time.Time
	fingerprint string
}

// Scheduler scans enabled rules and creates stable SCF batches. Realtime work
// fans out across the available SCF fleet before the per-function item limit
// applies. It is intentionally a single process timer handler; SQLite unique
// indexes provide the only idempotency needed by this single-user system.
type Scheduler struct {
	Rules             *store.TaskRuleRepository
	Instances         *store.TaskInstanceRepository
	Batches           *store.FetchBatchRepository
	Retries           *store.FetchRetryRepository
	Invoker           *scfinvoker.Client
	Storage           func(string, string, string) (binance.BatchStorage, error)
	StorageTarget     string
	BatchSize         int
	InvokeConcurrency int
	MaxRetryAttempts  int
	Metrics           *Metrics
	SpaceID           string
	Symbols           datasetSource
	// InvokeNonRealtimeOnly keeps the old Invoke path for symbol snapshots and
	// bounded catch-up while realtime K-lines run from Timer-triggered nodes.
	InvokeNonRealtimeOnly bool
	DNSCache              interface {
		Snapshot() map[string]sources.DNSResolution
	}
	Now              func() time.Time
	mu               sync.Mutex
	lastGapAudit     time.Time
	gapAuditCursorID int
	lastRuleID       string
	lastCleanup      time.Time
	planStates       map[string]scheduleState
	ruleFingerprints map[string]string
	invokeSem        chan struct{}
}

// fullSymbolSnapshotShards keeps each SCF's SQLite metadata registration
// small while still sourcing the complete exchange snapshot. Binance's active
// USDT catalogue is currently well below 640 symbols, so each shard carries
// at most about 20 subjects.
const fullSymbolSnapshotShards = 32

func (s *Scheduler) Tick(ctx context.Context, spaceID string) error {
	if s == nil || s.Rules == nil || s.Batches == nil || s.Invoker == nil {
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
	if s.ruleFingerprints == nil {
		s.ruleFingerprints = make(map[string]string)
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
	dnsRoutes := s.dnsSnapshot()
	allRules, err := s.Rules.ListEnabled(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list enabled collection rules: %w", err)
	}
	rules := allRules
	invokeRules := filterInvokeRules(allRules)
	rules = rotateRulesAfter(rules, s.lastRuleID)
	nodes, err := s.Invoker.ListMarketFetchers(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list market fetcher nodes: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Region != nodes[j].Region {
			return nodes[i].Region < nodes[j].Region
		}
		return nodes[i].FunctionName < nodes[j].FunctionName
	})
	if len(nodes) == 0 && len(invokeRules) > 0 {
		return fmt.Errorf("no active market fetcher nodes")
	}
	if err := s.recoverDue(ctx, spaceID, nodes, now); err != nil {
		log.WarnContextf(ctx, "recover market fetch batches failed: %v", err)
	}
	if err := s.dispatchDueRetries(ctx, spaceID, nodes, now); err != nil {
		log.WarnContextf(ctx, "dispatch market fetch retries failed: %v", err)
	}
	planned := 0
	for _, rule := range rules {
		if planned >= DefaultMaxPlan {
			break
		}
		items, frequencies, err := s.expandRule(ctx, rule)
		if err != nil {
			log.WarnContextf(ctx, "skip invalid collection rule=%s: %v", rule.RuleID, err)
			continue
		}
		activeTaskIDs := make([]string, 0, len(items)*len(frequencies))
		ruleFingerprintParts := make([]string, 0, len(items)*len(frequencies))
		instancesChanged := false
		for _, frequency := range frequencies {
			frequencyTaskIDs := make([]string, 0, len(items))
			for index := range items {
				spec := domain.TaskSpec{Provider: items[index].Provider, MarketType: items[index].MarketType, DataType: items[index].DataType, DatasetID: items[index].DatasetID, SubjectID: items[index].SubjectID, Frequency: frequency}
				items[index].TaskID = domain.StableTaskID(spaceID, rule.RuleID, spec)
			}
			for _, item := range items {
				frequencyTaskIDs = append(frequencyTaskIDs, item.TaskID)
				ruleFingerprintParts = append(ruleFingerprintParts, item.TaskID+"\x00"+rule.CollectParams)
			}
			if s.Instances != nil {
				instances := make([]domain.TaskInstance, 0, len(items))
				for _, item := range items {
					spec := domain.TaskSpec{Provider: item.Provider, MarketType: item.MarketType, DataType: item.DataType, DatasetID: item.DatasetID, SubjectID: item.SubjectID, Frequency: frequency}
					taskID := domain.StableTaskID(spaceID, rule.RuleID, spec)
					activeTaskIDs = append(activeTaskIDs, taskID)
					instances = append(instances, domain.TaskInstance{SpaceID: spaceID, TaskID: taskID, RuleID: rule.RuleID, Provider: item.Provider, MarketType: item.MarketType, DataType: item.DataType, DatasetID: item.DatasetID, SubjectID: item.SubjectID, Frequency: frequency, TaskParams: rule.CollectParams})
				}
				frequencyFingerprint := taskFingerprint(frequencyTaskIDs, rule.CollectParams)
				stateKey := rule.RuleID + "\x00" + frequency
				state := s.planStates[stateKey]
				frequencyChanged := state.fingerprint != frequencyFingerprint
				if frequencyChanged {
					instancesChanged = true
					if err := s.Instances.UpsertMany(ctx, instances); err != nil {
						return fmt.Errorf("persist stable collection instances: %w", err)
					}
				}
			}
			target, err := targetDataTime(now, frequency)
			if err != nil {
				log.WarnContextf(ctx, "skip rule=%s frequency=%s: %v", rule.RuleID, frequency, err)
				continue
			}
			stateKey := rule.RuleID + "\x00" + frequency
			state := s.planStates[stateKey]
			frequencyFingerprint := taskFingerprint(frequencyTaskIDs, rule.CollectParams)
			if s.InvokeNonRealtimeOnly && isKlineRule(rule) {
				// Keep the TaskInstance inventory for the bounded gap auditor, but
				// leave realtime K-line execution to Timer-triggered functions.
				s.planStates[stateKey] = scheduleState{fingerprint: frequencyFingerprint}
				continue
			}
			if !state.target.IsZero() && state.target.Equal(target) && state.fingerprint == frequencyFingerprint {
				continue
			}
			batchItems := append([]domain.CollectionItem(nil), items...)
			batchSize := s.realtimeBatchSize(len(batchItems), nodes)
			if strings.EqualFold(rule.DataType, "symbol") {
				batchSize = 1
			}
			for start, shard := 0, 0; start < len(batchItems); start, shard = start+batchSize, shard+1 {
				end := start + batchSize
				if end > len(batchItems) {
					end = len(batchItems)
				}
				// planned is the global batch cursor. Do not add shard again: the
				// loop already increments planned after every shard, and adding it
				// would skip every other node when the fleet size is even.
				node := nodes[planned%len(nodes)]
				for index := start; index < end; index++ {
					batchItems[index].TargetDataTime = target.Format(time.RFC3339Nano)
					batchItems[index].Frequency = frequency
					batchItems[index].BarLimit = 3
				}
				scheduleID := fmt.Sprintf("%s:%s:%s", rule.RuleID, frequency, target.Format(time.RFC3339Nano))
				batchKind := batchKindForRule(rule)
				batchID := stableID(spaceID, scheduleID, string(batchKind), fmt.Sprintf("%d", shard), "1")
				// The item is the normalized source of truth. Older rules may have an
				// empty top-level market_type while collect_params already contains
				// the canonical value; forwarding rule.MarketType would make SCF
				// reject the whole batch before it can inspect the item.
				batchProvider, batchMarketType := normalizedBatchIdentity(batchItems[start], rule)
				req := Request{BatchID: batchID, ScheduleID: scheduleID, BatchKind: batchKind, ShardIndex: shard, SpaceID: spaceID, DatasetID: batchItems[start].DatasetID, Frequency: frequency, Provider: batchProvider, MarketType: batchMarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: dnsRoutes, Items: batchItems[start:end]}
				created, err := s.planOne(ctx, rule, req, node, nodes)
				if err != nil {
					return err
				}
				if created {
					planned++
				}
				if planned >= DefaultMaxPlan {
					break
				}
			}
			s.planStates[stateKey] = scheduleState{target: target, fingerprint: frequencyFingerprint}
			if planned >= DefaultMaxPlan {
				break
			}
		}
		if s.Instances != nil && instancesChanged {
			ruleFingerprint := taskFingerprint(ruleFingerprintParts, "")
			if s.ruleFingerprints[rule.RuleID] == ruleFingerprint {
				instancesChanged = false
			} else {
				s.ruleFingerprints[rule.RuleID] = ruleFingerprint
			}
		}
		if s.Instances != nil && instancesChanged {
			if err := s.Instances.DeactivateMissingMarketFetchRuleInstances(ctx, spaceID, rule.RuleID, activeTaskIDs); err != nil {
				return fmt.Errorf("deactivate removed collection instances: %w", err)
			}
		}
		s.lastRuleID = rule.RuleID
	}
	if s.Instances != nil && (s.lastGapAudit.IsZero() || now.Sub(s.lastGapAudit) >= gapAuditInterval) {
		if err := s.auditGaps(ctx, spaceID, allRules, nodes, now); err != nil {
			log.WarnContextf(ctx, "market fetch gap audit failed: %v", err)
		} else {
			s.lastGapAudit = now
		}
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

func filterInvokeRules(rules []domain.TaskRule) []domain.TaskRule {
	filtered := make([]domain.TaskRule, 0, len(rules))
	for _, rule := range rules {
		dataType := strings.ToLower(strings.TrimSpace(rule.DataType))
		if dataType == "" {
			params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
			if err == nil {
				dataType = strings.ToLower(strings.TrimSpace(params.Collector.DataType))
			}
		}
		if dataType != "kline" {
			filtered = append(filtered, rule)
		}
	}
	return filtered
}

func isKlineRule(rule domain.TaskRule) bool {
	dataType := strings.ToLower(strings.TrimSpace(rule.DataType))
	if dataType != "" {
		return dataType == "kline"
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	return err == nil && strings.EqualFold(strings.TrimSpace(params.Collector.DataType), "kline")
}

func (s *Scheduler) dnsSnapshot() map[string]sources.DNSResolution {
	if s == nil || s.DNSCache == nil {
		return nil
	}
	return s.DNSCache.Snapshot()
}

func normalizedBatchIdentity(item domain.CollectionItem, rule domain.TaskRule) (string, string) {
	provider := strings.ToLower(firstNonEmpty(item.Provider, rule.Provider))
	marketType := firstNonEmpty(item.MarketType, rule.MarketType)
	return provider, marketType
}

func taskFingerprint(taskIDs []string, params string) string {
	ids := append([]string(nil), taskIDs...)
	sort.Strings(ids)
	h := sha256.New()
	for _, taskID := range ids {
		_, _ = h.Write([]byte(taskID))
		_, _ = h.Write([]byte{0})
	}
	_, _ = h.Write([]byte(params))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Scheduler) auditGaps(ctx context.Context, spaceID string, rules []domain.TaskRule, nodes []scfinvoker.Node, now time.Time) error {
	if len(nodes) == 0 {
		return nil
	}
	byRule := make(map[string]domain.TaskRule, len(rules))
	externalSymbols := make(map[string]string)
	for _, rule := range rules {
		byRule[rule.RuleID] = rule
		if !isKlineRule(rule) {
			continue
		}
		items, _, err := s.expandRule(ctx, rule)
		if err != nil {
			log.WarnContextf(ctx, "skip external symbol map rule=%s: %v", rule.RuleID, err)
			continue
		}
		for _, item := range items {
			if item.SubjectID == "" || item.Symbol == "" {
				continue
			}
			externalSymbols[rule.RuleID+"\x00"+strings.ToUpper(strings.TrimSpace(item.SubjectID))] = item.Symbol
		}
	}
	// Do not filter by last_exec_time here. A recent invocation can still leave
	// the Storage watermark stale. Scan a bounded cursor page and rotate through
	// the table; watermark reads run in parallel so the 10s timer is not spent
	// on a thousand sequential RPCs.
	instances, err := s.Instances.ListAfterID(ctx, spaceID, s.gapAuditCursorID, gapAuditPageSize)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		s.gapAuditCursorID = 0
		return nil
	}
	s.gapAuditCursorID = instances[len(instances)-1].ID
	type auditResult struct {
		instance domain.TaskInstance
		rule     domain.TaskRule
		symbol   string
		start    time.Time
		stale    bool
		err      error
	}
	jobs := make(chan domain.TaskInstance)
	results := make(chan auditResult, len(instances))
	workerCount := gapAuditWorkers
	if workerCount > len(instances) {
		workerCount = len(instances)
	}
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for instance := range jobs {
				rule, ok := byRule[instance.RuleID]
				if !ok || !strings.EqualFold(instance.DataType, "kline") || instance.SubjectID == "" {
					continue
				}
				result := auditResult{instance: instance, rule: rule, start: now.Add(-time.Hour)}
				result.symbol = externalSymbols[rule.RuleID+"\x00"+strings.ToUpper(strings.TrimSpace(instance.SubjectID))]
				if result.symbol == "" {
					result.err = fmt.Errorf("external symbol is missing for subject %s", instance.SubjectID)
					results <- result
					continue
				}
				threshold := gapAuditThreshold(instance.Frequency)
				if s.Storage == nil {
					if instance.LastExecTime != nil && !instance.LastExecTime.IsZero() {
						result.start = instance.LastExecTime.UTC()
					}
					result.stale = instance.LastExecTime == nil || instance.LastExecTime.IsZero() || now.Sub(result.start) >= threshold
					if result.stale {
						results <- result
					}
					continue
				}
				freq, freqErr := normalizeStorageFrequency(instance.Frequency)
				if freqErr != nil {
					continue
				}
				checkCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
				storage, storageErr := s.Storage(s.StorageTarget, instance.MarketType, "")
				if storageErr == nil {
					watermark, found, watermarkErr := storage.LatestTimeSeriesTime(checkCtx, &storagepb.TimeSeriesSelector{
						SpaceId: spaceID, DatasetId: instance.DatasetID, SubjectId: instance.SubjectID,
						Freq: freq, SeriesTag: stringPtr("venue:" + strings.ToLower(instance.Provider)),
					})
					storageErr = watermarkErr
					if storageErr == nil && found && !watermark.IsZero() {
						result.start = watermark.UTC()
						result.stale = now.Sub(result.start) >= threshold
					} else if storageErr == nil {
						if instance.LastExecTime != nil && !instance.LastExecTime.IsZero() {
							result.start = instance.LastExecTime.UTC()
						}
						result.stale = instance.LastExecTime == nil || instance.LastExecTime.IsZero() || now.Sub(result.start) >= threshold
					}
				}
				cancel()
				if storageErr != nil {
					result.err = storageErr
				} else if result.stale {
					results <- result
				}
			}
		}()
	}
	go func() {
		for _, instance := range instances {
			jobs <- instance
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	stale := make([]auditResult, 0, 5)
	for result := range results {
		if result.err != nil {
			log.WarnContextf(ctx, "skip market fetch gap audit task=%s: %v", result.instance.TaskID, result.err)
			continue
		}
		stale = append(stale, result)
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].instance.ID < stale[j].instance.ID })
	for index, candidate := range stale {
		if index >= 5 {
			break
		}
		node := nodes[index%len(nodes)]
		item := domain.CollectionItem{TaskID: candidate.instance.TaskID, SubjectID: candidate.instance.SubjectID, Symbol: candidate.symbol, Provider: candidate.instance.Provider, MarketType: candidate.instance.MarketType, DataType: "kline", DatasetID: candidate.instance.DatasetID, Frequency: candidate.instance.Frequency, StartTime: candidate.start.Format(time.RFC3339Nano), BarLimit: 1000}
		// A bounded 1,000-bar catchup must be allowed to advance on every audit
		// minute. A ten-minute identity keeps the first page deduplicated but
		// stalls large gaps for nine unnecessary minutes.
		scheduleID := fmt.Sprintf("catchup:%s:%s", candidate.instance.TaskID, now.Truncate(time.Minute).Format(time.RFC3339Nano))
		batchID := stableID(spaceID, scheduleID, string(domain.BatchKindCatchup), "0", "1")
		req := Request{BatchID: batchID, ScheduleID: scheduleID, BatchKind: domain.BatchKindCatchup, SpaceID: spaceID, DatasetID: item.DatasetID, Frequency: item.Frequency, Provider: item.Provider, MarketType: item.MarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: s.dnsSnapshot(), Items: []domain.CollectionItem{item}}
		if _, err := s.planOne(ctx, candidate.rule, req, node, nodes); err != nil {
			return err
		}
	}
	return nil
}

func gapAuditThreshold(frequency string) time.Duration {
	interval, err := report.ParseDatasetFrequency(strings.TrimSpace(frequency))
	if err != nil {
		return 10 * time.Minute
	}
	threshold := 3 * interval
	if threshold < 10*time.Minute {
		threshold = 10 * time.Minute
	}
	return threshold
}

func (s *Scheduler) planOne(ctx context.Context, rule domain.TaskRule, req Request, node scfinvoker.Node, nodes []scfinvoker.Node) (bool, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	if _, err := marketFetchEvent(req, s.StorageTarget); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	batch := &domain.BatchInvocation{SpaceID: req.SpaceID, BatchID: req.BatchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: req.ShardIndex, RuleID: rule.RuleID, DatasetID: req.DatasetID, Frequency: req.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: len(req.Items), PlannedAt: &now, DeadlineAt: timePtr(now.Add(70 * time.Second))}
	created, err := s.Batches.CreatePlanned(ctx, batch)
	if err != nil {
		return false, err
	}
	if !created {
		return false, nil
	}
	// Planning is the durable part of the timer tick. Invocation is bounded by
	// a small semaphore so a slow control plane cannot block the next rule.
	go s.dispatchPlanned(req, node, nodes)
	return true, nil
}

func rotateRulesAfter(rules []domain.TaskRule, lastRuleID string) []domain.TaskRule {
	if len(rules) < 2 || strings.TrimSpace(lastRuleID) == "" {
		return rules
	}
	for index, rule := range rules {
		if rule.RuleID == lastRuleID {
			return append(append([]domain.TaskRule(nil), rules[index+1:]...), rules[:index+1]...)
		}
	}
	return rules
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
		event, err := marketFetchEvent(requestForNode(req, candidate), s.StorageTarget)
		if err != nil {
			log.WarnContextf(ctx, "build SCF market fetch failover event failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
			return
		}
		invokeCtx, invokeCancel := context.WithTimeout(ctx, 5*time.Second)
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
		if _, err := s.Batches.MarkDispatchedToNode(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(70*time.Second), candidate.Region, candidate.NodeID, candidate.FunctionName); err != nil {
			log.WarnContextf(ctx, "mark market fetch batch dispatched failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
		}
		if attempt > 0 {
			log.InfoContextf(ctx, "SCF market fetch failover succeeded batch=%s node=%s", req.BatchID, candidate.NodeID)
		}
		return
	}
	log.WarnContextf(ctx, "SCF market fetch invoke exhausted failover batch=%s original_node=%s", req.BatchID, node.NodeID)
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
			effects.Retries = append(effects.Retries, &domain.RetryItem{SpaceID: spaceID, RetryKey: key, SourceBatchID: batch.BatchID, BatchKind: batch.BatchKind, RuleID: batch.RuleID, DatasetID: item.DatasetID, SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: target, TaskJSON: string(raw), Attempt: batch.Attempt, Status: "pending", NextRetryAt: &next, LastErrorType: "scf_completion_timeout", LastErrorSummary: "SCF completion event deadline exceeded", CreateTime: now, ModifyTime: now})
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
				BatchId: batch.BatchID, DatasetId: batch.DatasetID, Frequency: batch.Frequency,
				Status: "timed_out", DurationMs: duration, CompletedAt: timestamppb.New(now.UTC()),
			})
			if count, countErr := s.Retries.CountPending(ctx, spaceID, batch.DatasetID, batch.Frequency); countErr == nil {
				s.Metrics.SetRetryPending(spaceID, batch.DatasetID, batch.Frequency, int(count))
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
		node := nodes[index%len(nodes)]
		batchID := stableID(spaceID, "retry", retry.RetryKey, fmt.Sprintf("%d", retry.Attempt+1))
		batchKind := retry.BatchKind
		if batchKind == "" {
			batchKind = domain.BatchKindRealtime
		}
		req := Request{BatchID: batchID, ScheduleID: "retry:" + retry.RetryKey, BatchKind: batchKind, SpaceID: spaceID, DatasetID: item.DatasetID, Frequency: item.Frequency, Provider: item.Provider, MarketType: item.MarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: s.dnsSnapshot(), Items: []domain.CollectionItem{item}}
		raw, _ := json.Marshal(req)
		if _, err := marketFetchEvent(req, s.StorageTarget); err != nil {
			_ = s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed")
			continue
		}
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: batchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: index, RuleID: retry.RuleID, DatasetID: item.DatasetID, Frequency: item.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: retry.Attempt + 1, RequestJSON: string(raw), PlannedCount: 1, PlannedAt: &now, DeadlineAt: timePtr(now.Add(70 * time.Second))}
		created, err := s.Batches.CreatePlanned(ctx, batch)
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
		event, err := marketFetchEvent(requestForNode(req, candidate), s.StorageTarget)
		if err != nil {
			log.WarnContextf(ctx, "build SCF market fetch retry failover event failed batch=%s node=%s err=%v", req.BatchID, candidate.NodeID, err)
			return
		}
		invokeCtx, invokeCancel := context.WithTimeout(ctx, 5*time.Second)
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
		updated, err := s.Batches.MarkDispatchedToNode(ctx, req.SpaceID, req.BatchID, result.RequestID, time.Now().UTC().Add(70*time.Second), candidate.Region, candidate.NodeID, candidate.FunctionName)
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

func (s *Scheduler) expandRule(ctx context.Context, rule domain.TaskRule) ([]domain.CollectionItem, []string, error) {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return nil, nil, err
	}
	provider := strings.ToLower(firstNonEmpty(params.Provider, rule.Provider))
	marketType := strings.ToLower(firstNonEmpty(params.MarketType, rule.MarketType))
	dataType := strings.ToLower(firstNonEmpty(params.Collector.DataType, rule.DataType))
	targetDataset := firstNonEmpty(params.Target.DatasetID, params.Source.DatasetID)
	if targetDataset == "" {
		return nil, nil, fmt.Errorf("target dataset is required")
	}
	frequencies := append([]string(nil), params.Collector.Intervals...)
	if len(frequencies) == 0 && params.Schedule.Interval != "" {
		frequencies = []string{params.Schedule.Interval}
	}
	if dataType == "symbol" {
		if len(frequencies) == 0 {
			frequencies = []string{"1h"}
		}
		if params.SymbolSource != "exchange" {
			return nil, nil, fmt.Errorf("symbol task requires exchange snapshot source")
		}
		items := make([]domain.CollectionItem, fullSymbolSnapshotShards)
		for shard := range items {
			items[shard] = domain.CollectionItem{SubjectID: targetDataset, Provider: provider, MarketType: marketType, DataType: "symbol", DatasetID: targetDataset, SnapshotShardIndex: shard, SnapshotShardCount: fullSymbolSnapshotShards}
		}
		return items, frequencies[:1], nil
	}
	if dataType != "kline" {
		return nil, nil, fmt.Errorf("unsupported data_type %q", dataType)
	}
	if params.Source.DatasetID == "" {
		return nil, nil, fmt.Errorf("kline symbol dataset is required")
	}
	items := make([]domain.CollectionItem, 0)
	if s.Symbols != nil {
		spaceID := strings.TrimSpace(rule.SpaceID)
		if spaceID == "" {
			spaceID = strings.TrimSpace(s.SpaceID)
		}
		dataset, err := s.Symbols.GetDataset(ctx, spaceID, params.Source.DatasetID)
		if err != nil {
			return nil, nil, fmt.Errorf("get symbol dataset %s: %w", params.Source.DatasetID, err)
		}
		subjects, err := s.Symbols.ListSubjects(ctx, spaceID, params.Source.DatasetID, dataset.DataSourceID)
		if err != nil {
			return nil, nil, fmt.Errorf("list symbol dataset subjects: %w", err)
		}
		for _, subject := range subjects {
			if !strings.EqualFold(strings.TrimSpace(subject.Status), "active") || strings.TrimSpace(subject.SubjectID) == "" {
				continue
			}
			if strings.TrimSpace(subject.ExternalSymbol) == "" {
				return nil, nil, fmt.Errorf("external symbol is missing for subject %s", subject.SubjectID)
			}
			items = append(items, domain.CollectionItem{SubjectID: strings.TrimSpace(subject.SubjectID), Symbol: strings.TrimSpace(subject.ExternalSymbol), Provider: provider, MarketType: marketType, DataType: "kline", DatasetID: targetDataset})
		}
	} else {
		if s.Storage == nil {
			return nil, nil, fmt.Errorf("storage reader is not initialized")
		}
		storage, err := s.Storage(s.StorageTarget, marketType, "")
		if err != nil {
			return nil, nil, err
		}
		memberships, err := storage.ListDatasetSubjects(ctx, rule.SpaceID, params.Source.DatasetID)
		if err != nil {
			return nil, nil, fmt.Errorf("list symbol dataset subjects: %w", err)
		}
		for _, membership := range memberships {
			if membership == nil || !strings.EqualFold(strings.TrimSpace(membership.GetStatus()), "active") {
				continue
			}
			subjectID := strings.TrimSpace(membership.GetSubjectId())
			if subjectID == "" {
				continue
			}
			items = append(items, domain.CollectionItem{SubjectID: subjectID, Symbol: strings.ReplaceAll(subjectID, "-", ""), Provider: provider, MarketType: marketType, DataType: "kline", DatasetID: targetDataset})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SubjectID < items[j].SubjectID })
	return items, frequencies, nil
}

func batchKindForRule(rule domain.TaskRule) domain.BatchKind {
	if strings.EqualFold(strings.TrimSpace(rule.DataType), "symbol") {
		return domain.BatchKindSymbolSnapshot
	}
	return domain.BatchKindRealtime
}

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

func stringPtr(value string) *string { return &value }

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
