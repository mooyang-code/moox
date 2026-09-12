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
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// Scheduler scans enabled rules and creates stable SCF batches. Realtime work
// fans out across the available SCF fleet before the per-function item limit
// applies. It is intentionally a single process timer handler; SQLite unique
// indexes provide the only idempotency needed by this single-user system.
type Scheduler struct {
	SCFRegionBlacklists map[string][]string
	ResolveSymbol       SymbolResolver
	Rules               *store.TaskRuleRepository
	Instances           *store.TaskInstanceRepository
	Batches             *store.FetchBatchRepository
	Retries             *store.FetchRetryRepository
	Invoker             *scfinvoker.Client
	Storage             func(string, string, string) (StorageReader, error)
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
	// InvokeNonRealtimeOnly keeps the Invoke path for instrument snapshots and
	// bounded catch-up while realtime K-lines run from Timer-triggered nodes.
	InvokeNonRealtimeOnly bool
	DNSCache              interface {
		Snapshot() map[string]sources.DNSResolution
	}
	Now              func() time.Time
	mu               sync.Mutex
	lastRuleID       string
	lastCleanup      time.Time
	planStates       map[string]scheduleState
	ruleFingerprints map[string]string
	invokeSem        chan struct{}
}

// fullInstrumentSnapshotShards keeps stockcn's large metadata registration
// small. Crypto symbol snapshots use one invocation because Binance returns a
// complete exchange snapshot and there is no benefit in issuing the same
// exchangeInfo request from every shard.
const fullInstrumentSnapshotShards = 32

func instrumentSnapshotShardCount(marketID string) int {
	if strings.EqualFold(strings.TrimSpace(marketID), "crypto") {
		return 1
	}
	return fullInstrumentSnapshotShards
}

const (
	defaultBatchCompletionDeadline     = 70 * time.Second
	instrumentSnapshotCompletionWindow = 6 * time.Minute
)

func batchCompletionDeadline(kind domain.BatchKind) time.Duration {
	if kind == domain.BatchKindInstrumentSnapshot {
		return instrumentSnapshotCompletionWindow
	}
	return defaultBatchCompletionDeadline
}

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
	dnsRoutes := s.dnsSnapshot(ctx)
	allRules, err := s.Rules.ListEnabled(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list enabled collection rules: %w", err)
	}
	// Local collector jobs (for example kline_resample) are driven by their
	// own timer workers. The market-fetch scheduler only owns cloud-invoked
	// collection rules.
	rules := filterMarketFetchRules(allRules)
	invokeRules := filterInvokeRules(rules)
	rules = rotateRulesAfter(rules, s.lastRuleID)
	// The scheduler still owns the durable TaskInstance inventory for Timer
	// K-lines even when InvokeNonRealtimeOnly keeps realtime execution out of
	// this path. ListMarketFetchers intentionally returns Invoke nodes only, so
	// fetch the Timer fleet separately before expanding the rule.
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
	if len(invokeNodes) == 0 && len(invokeRules) > 0 {
		return fmt.Errorf("no active Invoke market fetcher nodes")
	}
	if err := s.recoverDue(ctx, spaceID, invokeNodes, now); err != nil {
		log.WarnContextf(ctx, "recover market fetch batches failed: %v", err)
	}
	if err := s.dispatchDueRetries(ctx, spaceID, invokeNodes, now); err != nil {
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
		ruleNodes := timerNodes
		if !isKlineRule(rule) {
			ruleNodes = invokeNodes
		}
		if len(ruleNodes) == 0 {
			log.WarnContextf(ctx, "skip collection rule=%s: no nodes for trigger type", rule.RuleID)
			continue
		}
		activeTaskIDs := make([]string, 0, len(items)*len(frequencies))
		ruleFingerprintParts := make([]string, 0, len(items)*len(frequencies))
		instancesChanged := false
		for _, frequency := range frequencies {
			frequencyTaskIDs := make([]string, 0, len(items))
			for index := range items {
				items[index].TaskID = collectionItemTaskID(spaceID, rule.RuleID, items[index], frequency)
			}
			for _, item := range items {
				frequencyTaskIDs = append(frequencyTaskIDs, item.TaskID)
				ruleFingerprintParts = append(ruleFingerprintParts, item.TaskID+"\x00"+rule.CollectParams)
			}
			if s.Instances != nil {
				instances := make([]domain.TaskInstance, 0, len(items))
				for _, item := range items {
					taskID := collectionItemTaskID(spaceID, rule.RuleID, item, frequency)
					activeTaskIDs = append(activeTaskIDs, taskID)
					instances = append(instances, domain.TaskInstance{SpaceID: spaceID, TaskID: taskID, RuleID: rule.RuleID, Provider: item.Provider, SourceID: item.SourceID, MarketType: item.MarketType, DataType: item.DataType, DatasetID: item.DatasetID, SubjectID: item.SubjectID, Frequency: frequency, TaskParams: rule.CollectParams})
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
				// Keep the TaskInstance inventory while realtime K-line execution is
				// owned by Timer-triggered functions.
				s.planStates[stateKey] = scheduleState{fingerprint: frequencyFingerprint}
				continue
			}
			if !state.target.IsZero() && state.target.Equal(target) && state.fingerprint == frequencyFingerprint {
				continue
			}
			batchItems := append([]domain.CollectionItem(nil), items...)
			batchSize := s.realtimeBatchSize(len(batchItems), ruleNodes)
			if strings.EqualFold(rule.DataType, domain.InstrumentDataType) {
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
				node := ruleNodes[planned%len(ruleNodes)]
				for index := start; index < end; index++ {
					batchItems[index].TargetDataTime = target.Format(time.RFC3339Nano)
					batchItems[index].Frequency = frequency
					batchItems[index].BarLimit = MaxRealtimeRows
					if strings.EqualFold(batchItems[index].DataType, domain.InstrumentDataType) {
						// Every snapshot shard must use one generation timestamp. The
						// provider response is fetched independently, so this field is
						// the generation fence used by staged shard activation.
						batchItems[index].SnapshotAt = now.Format(time.RFC3339Nano)
					}
				}
				scheduleID := fmt.Sprintf("%s:%s:%s", rule.RuleID, frequency, target.Format(time.RFC3339Nano))
				batchKind := batchKindForRule(rule)
				batchID := stableID(spaceID, scheduleID, string(batchKind), fmt.Sprintf("%d", shard), "1")
				syncPointID := stableID(spaceID, scheduleID, string(batchKind), fmt.Sprintf("%d", shard), "write")
				// The item is the normalized source of truth. Older rules may have an
				// empty top-level market_type while collect_params already contains
				// the canonical value; forwarding rule.MarketType would make SCF
				// reject the whole batch before it can inspect the item.
				batchProvider, batchMarketType := normalizedBatchIdentity(batchItems[start], rule)
				req := Request{BatchID: batchID, SyncPointID: syncPointID, ScheduleID: scheduleID, BatchKind: batchKind, ShardIndex: shard, SpaceID: spaceID, MarketID: batchItems[start].MarketID, InstrumentType: batchItems[start].InstrumentType, DatasetID: batchItems[start].DatasetID, Frequency: frequency, Provider: batchProvider, SourceID: batchItems[start].SourceID, MarketType: batchMarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: dnsRoutes, Items: batchItems[start:end]}
				created, err := s.planOne(ctx, rule, req, node, ruleNodes)
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

func filterMarketFetchRules(rules []domain.TaskRule) []domain.TaskRule {
	filtered := make([]domain.TaskRule, 0, len(rules))
	for _, rule := range rules {
		dataType := strings.ToLower(strings.TrimSpace(rule.DataType))
		if dataType == "" {
			params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
			if err == nil {
				dataType = strings.ToLower(strings.TrimSpace(params.Collector.DataType))
			}
		}
		if dataType == "kline_resample" {
			continue
		}
		filtered = append(filtered, rule)
	}
	return filtered
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

func isKlineRule(rule domain.TaskRule) bool {
	dataType := strings.ToLower(strings.TrimSpace(rule.DataType))
	if dataType != "" {
		return dataType == "kline"
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
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

// The public stock endpoints currently expose only a bounded latest page and
// have no safe cursor shared by all active providers. Reject older history at
// planning time instead of repeatedly requesting a page that cannot cover the
// requested start. A future cursor-paginated feed can raise this deliberately.
const stockCNHistoryMaxLookback = 24 * time.Hour

func collectionItemTaskID(spaceID, ruleID string, item domain.CollectionItem, frequency string) string {
	if strings.EqualFold(strings.TrimSpace(item.DataType), domain.InstrumentDataType) && item.SnapshotShardCount > 0 {
		return stableID(spaceID, ruleID, string(domain.BatchKindInstrumentSnapshot), item.DatasetID, strconv.Itoa(item.SnapshotShardIndex))
	}
	spec := domain.TaskSpec{RouteID: stableRouteID(item.MarketType, item.DatasetID, frequency), Provider: item.Provider, MarketType: item.MarketType, DataType: item.DataType, DatasetID: item.DatasetID, SubjectID: item.SubjectID, Frequency: frequency}
	return domain.StableTaskID(spaceID, ruleID, spec)
}

func (s *Scheduler) planOne(ctx context.Context, rule domain.TaskRule, req Request, node scfinvoker.Node, nodes []scfinvoker.Node) (bool, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	if _, err := marketFetchEvent(req, s.eventStorageTarget()); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	batch := &domain.BatchInvocation{SpaceID: req.SpaceID, BatchID: req.BatchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: req.ShardIndex, RuleID: rule.RuleID, DatasetID: req.DatasetID, Frequency: req.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: len(req.Items), PlannedAt: &now, DeadlineAt: timePtr(now.Add(batchCompletionDeadline(req.BatchKind)))}
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
		event, err := marketFetchEvent(requestForNode(req, candidate), s.eventStorageTarget())
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
			effects.Retries = append(effects.Retries, &domain.RetryItem{SpaceID: spaceID, RetryKey: key, SourceBatchID: logicalSyncPointID, BatchKind: batch.BatchKind, RuleID: batch.RuleID, DatasetID: item.DatasetID, SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: target, TaskJSON: string(raw), Attempt: batch.Attempt, Status: "pending", NextRetryAt: &next, LastErrorType: "scf_completion_timeout", LastErrorSummary: "SCF completion event deadline exceeded", CreateTime: now, ModifyTime: now})
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
		req := Request{BatchID: batchID, SyncPointID: retry.SourceBatchID, ScheduleID: "retry:" + retry.RetryKey, BatchKind: batchKind, SpaceID: spaceID, DatasetID: item.DatasetID, Frequency: item.Frequency, Provider: item.Provider, SourceID: item.SourceID, MarketType: item.MarketType, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, DNSRoutes: s.dnsSnapshot(ctx), Items: []domain.CollectionItem{item}}
		raw, _ := json.Marshal(req)
		if _, err := marketFetchEvent(req, s.eventStorageTarget()); err != nil {
			_ = s.Retries.MarkStatus(ctx, spaceID, retry.RetryKey, "permanent_failed")
			continue
		}
		batch := &domain.BatchInvocation{SpaceID: spaceID, BatchID: batchID, ScheduleID: req.ScheduleID, BatchKind: req.BatchKind, ShardIndex: index, RuleID: retry.RuleID, DatasetID: item.DatasetID, Frequency: item.Frequency, Region: node.Region, NodeID: node.NodeID, FunctionName: node.FunctionName, Status: domain.BatchStatusPlanned, Attempt: retry.Attempt + 1, RequestJSON: string(raw), PlannedCount: 1, PlannedAt: &now, DeadlineAt: timePtr(now.Add(batchCompletionDeadline(req.BatchKind)))}
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
		event, err := marketFetchEvent(requestForNode(req, candidate), s.eventStorageTarget())
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

func (s *Scheduler) expandRule(ctx context.Context, rule domain.TaskRule) ([]domain.CollectionItem, []string, error) {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return nil, nil, err
	}
	provider := strings.ToLower(firstNonEmpty(params.Provider, rule.Provider))
	marketType := strings.ToLower(firstNonEmpty(params.MarketType, rule.MarketType))
	marketID := strings.ToLower(firstNonEmpty(params.MarketID, rule.SpaceID, s.SpaceID))
	instrumentType := strings.ToLower(firstNonEmpty(params.InstrumentType, defaultInstrumentTypeForMarket(marketID, marketType)))
	sourceID := strings.ToLower(strings.TrimSpace(params.SourceID))
	dataType := strings.ToLower(firstNonEmpty(params.Collector.DataType, rule.DataType))
	targetDataset := firstNonEmpty(params.Target.DatasetID, params.Source.DatasetID)
	if targetDataset == "" {
		return nil, nil, fmt.Errorf("target dataset is required")
	}
	frequencies := append([]string(nil), params.Collector.Intervals...)
	if len(frequencies) == 0 && params.Schedule.Interval != "" {
		frequencies = []string{params.Schedule.Interval}
	}
	if dataType == domain.InstrumentDataType {
		if len(frequencies) == 0 {
			frequencies = []string{"1h"}
		}
		if params.SymbolSource != "exchange" {
			return nil, nil, fmt.Errorf("symbol task requires exchange snapshot source")
		}
		shardCount := instrumentSnapshotShardCount(marketID)
		items := make([]domain.CollectionItem, shardCount)
		for shard := range items {
			items[shard] = domain.CollectionItem{SubjectID: targetDataset, Provider: provider, SourceID: sourceID, MarketID: marketID, InstrumentType: instrumentType, MarketType: marketType, DataType: domain.InstrumentDataType, DatasetID: targetDataset, SnapshotShardIndex: shard, SnapshotShardCount: shardCount}
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
			subjectID := strings.ToUpper(strings.TrimSpace(subject.SubjectID))
			symbol, symbolErr := resolveProviderSymbol(s.ResolveSymbol, provider, marketID, marketType, subjectID, subject.ExternalSymbol)
			if symbolErr != nil {
				log.WarnContextf(ctx, "skip market symbol without valid external symbol subject=%q error=%v", subject.SubjectID, symbolErr)
				continue
			}
			items = append(items, domain.CollectionItem{SubjectID: subjectID, Symbol: symbol, Provider: provider, SourceID: sourceID, MarketID: marketID, InstrumentType: instrumentType, MarketType: marketType, DataType: "kline", DatasetID: targetDataset})
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
			symbol, symbolErr := resolveProviderSymbol(s.ResolveSymbol, provider, marketID, marketType, subjectID, "")
			if symbolErr != nil {
				continue
			}
			items = append(items, domain.CollectionItem{SubjectID: subjectID, Symbol: symbol, Provider: provider, SourceID: sourceID, MarketID: marketID, InstrumentType: instrumentType, MarketType: marketType, DataType: "kline", DatasetID: targetDataset})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SubjectID < items[j].SubjectID })
	return items, frequencies, nil
}

func batchKindForRule(rule domain.TaskRule) domain.BatchKind {
	if strings.EqualFold(strings.TrimSpace(rule.DataType), domain.InstrumentDataType) {
		return domain.BatchKindInstrumentSnapshot
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
