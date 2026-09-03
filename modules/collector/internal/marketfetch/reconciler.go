package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	timerTriggerType       = "timer"
	timerTriggerQualifier  = "$LATEST"
	timerTriggerMessage    = "market_fetch_timer_v1"
	runtimeConfigBatchSize = 100
)

type ruleSource interface {
	ListEnabled(context.Context, string) ([]domain.TaskRule, error)
}

type datasetSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error)
}

type runtimeConfigClient interface {
	ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error)
	SubmitRuntimeConfigs(context.Context, string, []*cloudnodepb.NodeRuntimeConfigPatch) (string, error)
	GetRuntimeConfigBatchStatus(context.Context, string, string) (*cloudnodepb.NodeBatchSummary, error)
}

type dnsSnapshotter interface {
	Snapshot() map[string]sources.DNSResolution
}

// Reconciler is the Collector control-plane loop for static Timer-triggered
// functions. It never invokes a function; it only submits desired config.
type Reconciler struct {
	Rules                         ruleSource
	Symbols                       datasetSource
	Nodes                         runtimeConfigClient
	Instances                     *store.TaskInstanceRepository
	DNS                           dnsSnapshotter
	Metrics                       *Metrics
	MaxSubjects                   int
	MeasuredSafeGroupSize         int
	ExpectedStockCNTimerFunctions int
	StockCNStagger                StockCNStaggerConfig
	Now                           func() time.Time
	mu                            sync.Mutex
	reconcileMu                   sync.Mutex
	pending                       map[string]string
	pendingAt                     map[string]time.Time
	pendingJob                    string
	pendingJobs                   []string
	pendingSince                  time.Time
}

func (r *Reconciler) Reconcile(ctx context.Context, spaceID string) error {
	if r == nil || r.Rules == nil || r.Symbols == nil || r.Nodes == nil {
		return fmt.Errorf("SCF timer reconciler is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	// The timer callback starts a goroutine on every tick. Serialize the full
	// check-to-submit sequence so two slow ticks cannot publish overlapping
	// snapshots and overwrite pendingJob.
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	if pendingJobs, pendingSince := r.pendingRuntimeJobsState(); len(pendingJobs) > 0 {
		for _, pendingJob := range pendingJobs {
			status, statusErr := r.Nodes.GetRuntimeConfigBatchStatus(ctx, spaceID, pendingJob)
			if statusErr != nil {
				return r.fail(spaceID, "cloudnode", fmt.Errorf("get timer runtime config job %s: %w", pendingJob, statusErr))
			}
			switch status.GetStatus() {
			case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING, cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING:
				r.observeAssignmentPending(spaceID, true, pendingSince)
				log.InfoContextf(ctx, "collector_scf_timer_reconciliation_pending space=%s job=%s jobs=%d status=%s", spaceID, pendingJob, len(pendingJobs), status.GetStatus().String())
				return nil
			case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED:
				r.clearPendingRuntimeJobs()
				r.observeAssignmentPending(spaceID, false, time.Time{})
				return r.fail(spaceID, "cloudnode", fmt.Errorf("timer runtime config job %s failed", pendingJob))
			case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL:
				r.clearPendingRuntimeJobs()
				r.observeAssignmentPending(spaceID, false, time.Time{})
				return r.fail(spaceID, "cloudnode", fmt.Errorf("timer runtime config job %s partially failed", pendingJob))
			case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS:
				continue
			default:
				r.clearPendingRuntimeJobs()
				return r.fail(spaceID, "cloudnode", fmt.Errorf("timer runtime config job %s returned unknown status %s", pendingJob, status.GetStatus().String()))
			}
		}
		r.clearPendingRuntimeJobs()
		r.observeAssignmentPending(spaceID, false, time.Time{})
	}
	nodes, err := r.Nodes.ListTimerMarketFetchers(ctx, spaceID)
	if err != nil {
		return r.fail(spaceID, "cloudnode", fmt.Errorf("list timer market fetchers: %w", err))
	}
	r.observeTimerStates(spaceID, nodes)
	groups, err := r.groups(ctx, spaceID)
	if err != nil {
		return r.fail(spaceID, "rules", err)
	}
	stockCN := strings.EqualFold(spaceID, StockCNSpaceID)
	if stockCN && len(groups) == 0 {
		// Keep the published Timer fleet explicitly disabled when no active
		// rule/subject remains. This also clears stale FunctionName bindings
		// without inventing a fake market subject.
		groups = []TaskGroup{{
			Provider: "stock_cn_multi", MarketType: "equity", MarketID: StockCNSpaceID,
			InstrumentType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		}}
	}
	dns := map[string]sources.DNSResolution(nil)
	if r.DNS != nil && !stockCN {
		dns = r.DNS.Snapshot()
	}
	// The configured DNS snapshot currently contains Binance hosts only. Stock
	// providers use their own hostname-based HTTP clients, so copying that
	// unrelated snapshot into every stock assignment would make a Binance IP
	// rotation rewrite all 170 Timer functions without changing stock runtime
	// state.
	// Publish the unsplit requirement before any local environment/capacity
	// validation. A malformed budget or an individual symbol that cannot fit
	// must still become a visible Monitor coordination failure.
	r.observeAssignmentRequirements(spaceID, groups)
	managedBudget, budgetErr := managedEnvironmentBudget(nodes, managedEnvironmentLimit(stockCN))
	if budgetErr != nil {
		return r.fail(spaceID, "environment", budgetErr)
	}
	// The 30-subject limit is a business ceiling, not a promise that every
	// legal symbol fits in Tencent's 4KB Environment. Long symbols and DNS
	// routes can consume the remaining bytes, so split a group further before
	// checking node capacity instead of retrying the same oversized patch.
	if !stockCN {
		groups, err = splitGroupsForEnvironment(groups, dns, r.maxSubjects(), managedBudget)
		if err != nil {
			return r.fail(spaceID, "environment", err)
		}
	}
	r.observeAssignmentRequirements(spaceID, groups)
	// Publish the fleet-level demand before building assignments. If the
	// required shard count exceeds the visible Timer fleet, BuildAssignments
	// will fail below, but Monitor can still report the exact shortfall instead
	// of waiting for a generic coordination error.
	r.observeTimerCapacity(spaceID, nodes, groups, nil)
	var assignments []NodeAssignment
	if stockCN {
		if len(groups) != 1 {
			return r.fail(spaceID, "rules", fmt.Errorf("stock_cn requires exactly one merged kline assignment group, got %d", len(groups)))
		}
		if r.ExpectedStockCNTimerFunctions <= 0 {
			return r.fail(spaceID, "capacity", fmt.Errorf("stock_cn timer function count must be an explicit positive value"))
		}
		if r.MeasuredSafeGroupSize <= 0 {
			return r.fail(spaceID, "capacity", fmt.Errorf("stock_cn measured_safe_group_size must be a positive value"))
		}
		requiredGroupSize, sizeErr := requiredStockCNGroupSize(len(groups[0].Subjects), r.ExpectedStockCNTimerFunctions)
		if sizeErr != nil {
			return r.fail(spaceID, "capacity", sizeErr)
		}
		if requiredGroupSize > r.MeasuredSafeGroupSize {
			return r.fail(spaceID, "capacity", fmt.Errorf("stock_cn required_group_size %d exceeds measured_safe_group_size %d", requiredGroupSize, r.MeasuredSafeGroupSize))
		}
		assignments, err = BuildStockCNAssignmentsWithStagger(groups[0], nodes, r.MeasuredSafeGroupSize, r.stockCNTradingDate(), r.StockCNStagger, r.ExpectedStockCNTimerFunctions)
	} else {
		assignments, err = BuildAssignments(groups, nodes, r.maxSubjects())
	}
	if err != nil {
		return r.fail(spaceID, "capacity", err)
	}
	r.observeTimerCapacity(spaceID, nodes, groups, assignments)
	// Publish the complete desired plan before the remote submission. A client
	// timeout is ambiguous: CloudNode may still have accepted the request, so
	// Monitor must not reinterpret a temporary HTTP failure as zero capacity.
	r.observeAssignmentDesiredMetrics(spaceID, groups, assignments)
	dnsAvailable := len(dns) > 0
	patches := make([]*cloudnodepb.NodeRuntimeConfigPatch, 0, len(assignments))
	pendingFingerprints := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		environment, envErr := buildManagedEnvironment(assignment, dns, managedBudget)
		if envErr != nil {
			return r.fail(spaceID, "environment", envErr)
		}
		if !dnsAvailable {
			// A Collector restart can reach this tick before its first DNS
			// refresh succeeds. Omit DNS-owned keys rather than sending an empty
			// snapshot that would erase the last known-good SCF routes; the
			// CloudNode merge keeps the remote values intact.
			delete(environment, "MOOX_MARKET_FETCH_DNS_ROUTES_JSON")
			delete(environment, "MOOX_MARKET_FETCH_DNS_HASH")
			delete(environment, "MOOX_MARKET_FETCH_DNS_UPDATED_AT")
		}
		cron := assignment.Cron
		if cron == "" {
			cron = "0 * * * * * *"
		}
		dnsHash := environment["MOOX_MARKET_FETCH_DNS_HASH"]
		if dnsHash == "" && !dnsAvailable {
			// A failed refresh must not erase the last-known-good SCF route. Keep
			// the stored identity in the fingerprint so the same stale snapshot
			// is not submitted on every market-fetch tick. The SCF runtime still
			// falls back to the hostname whenever its stored route is unusable.
			dnsHash = currentDNSHash(assignment.NodeID, nodes)
		}
		// Disabled nodes do not execute Timer work, so rotating DNS routes do
		// not require an environment update for them. Keep their existing DNS
		// values until the node is enabled again; the next enabled assignment
		// always carries the current snapshot.
		if !assignment.Enabled {
			dnsHash = ""
		}
		fingerprint := assignment.AssignmentHash + "\x00" + dnsHash + "\x00" + fmt.Sprint(assignment.Enabled) + "\x00" + cron
		if !r.shouldPatch(assignment, nodes, fingerprint) {
			continue
		}
		patches = append(patches, &cloudnodepb.NodeRuntimeConfigPatch{NodeId: assignment.NodeID, ManagedEnvironment: environment, TimerEnabled: assignment.Enabled, TimerCron: cron})
		pendingFingerprints[assignment.NodeID] = fingerprint
	}
	if len(patches) == 0 {
		if err := r.persistAssignments(ctx, spaceID, nodes, assignments); err != nil {
			return r.fail(spaceID, "task_instances", err)
		}
		r.observeAssignmentMetrics(spaceID, groups, assignments, time.Now().UTC().Unix())
		return nil
	}
	jobIDs := make([]string, 0, (len(patches)+runtimeConfigBatchSize-1)/runtimeConfigBatchSize)
	acceptedFingerprints := make(map[string]string, len(pendingFingerprints))
	for _, batch := range runtimeConfigPatchBatches(patches, runtimeConfigBatchSize) {
		jobID, submitErr := r.Nodes.SubmitRuntimeConfigs(ctx, spaceID, batch)
		if submitErr != nil {
			// A timeout is ambiguous: CloudNode may have accepted earlier
			// chunks. Track those jobs so the next tick observes them before
			// retrying the remaining desired state.
			if len(jobIDs) > 0 {
				r.setPendingRuntimeJobs(jobIDs, acceptedFingerprints)
			}
			if isTimeoutError(submitErr) {
				pendingSince := r.markSubmitRetryPending()
				r.observeAssignmentPending(spaceID, true, pendingSince)
				return r.fail(spaceID, "submit_timeout", fmt.Errorf("submit timer runtime configs: %w", submitErr))
			}
			if len(jobIDs) == 0 {
				r.clearSubmitRetryPending()
			}
			r.observeAssignmentPending(spaceID, false, time.Time{})
			return r.fail(spaceID, "cloudnode", fmt.Errorf("submit timer runtime configs: %w", submitErr))
		}
		jobIDs = append(jobIDs, jobID)
		for _, patch := range batch {
			if fingerprint, ok := pendingFingerprints[patch.GetNodeId()]; ok {
				acceptedFingerprints[patch.GetNodeId()] = fingerprint
			}
		}
	}
	r.setPendingRuntimeJobs(jobIDs, acceptedFingerprints)
	pendingSince := r.pendingRuntimeSince()
	if r.Metrics != nil {
		r.Metrics.ClearAssignmentFailure(spaceID)
	}
	log.InfoContextf(ctx, "collector_scf_timer_reconciled space=%s nodes=%d patches=%d jobs=%d", spaceID, len(nodes), len(patches), len(jobIDs))
	r.observeAssignmentPending(spaceID, true, pendingSince)
	return nil
}

func runtimeConfigPatchBatches(patches []*cloudnodepb.NodeRuntimeConfigPatch, batchSize int) [][]*cloudnodepb.NodeRuntimeConfigPatch {
	if batchSize <= 0 {
		return nil
	}
	batches := make([][]*cloudnodepb.NodeRuntimeConfigPatch, 0, (len(patches)+batchSize-1)/batchSize)
	for start := 0; start < len(patches); start += batchSize {
		end := minInt(start+batchSize, len(patches))
		batches = append(batches, patches[start:end])
	}
	return batches
}

func (r *Reconciler) persistAssignments(ctx context.Context, spaceID string, nodes []scfinvoker.Node, assignments []NodeAssignment) error {
	if r == nil || r.Instances == nil {
		return nil
	}
	functionNames := make([]string, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		name := strings.TrimSpace(node.FunctionName)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		functionNames = append(functionNames, name)
	}
	replacements := make([]store.MarketFetchAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		if !assignment.Enabled {
			continue
		}
		if strings.TrimSpace(assignment.FunctionName) == "" {
			return fmt.Errorf("enabled assignment %s has no function_name", assignment.NodeID)
		}
		provider := firstNonEmpty(assignment.RouteProvider, assignment.Provider)
		replacements = append(replacements, store.MarketFetchAssignment{Provider: provider, SourceID: assignment.SourceID, MarketType: assignment.MarketType, DatasetID: assignment.DatasetID, Frequency: assignment.Frequency, FunctionName: assignment.FunctionName, Subjects: assignment.Subjects})
	}
	if err := r.Instances.ReplaceMarketFetchAssignments(ctx, spaceID, functionNames, replacements); err != nil {
		return fmt.Errorf("replace SCF task assignments: %w", err)
	}
	return nil
}

func splitGroupsForEnvironment(groups []TaskGroup, snapshot map[string]sources.DNSResolution, maxSubjects int, budgets ...int) ([]TaskGroup, error) {
	if maxSubjects <= 0 {
		return nil, fmt.Errorf("max subjects must be positive")
	}
	managedBudget := maxManagedEnvironmentSize
	if len(budgets) > 0 && budgets[0] > 0 && budgets[0] < managedBudget {
		managedBudget = budgets[0]
	}
	result := make([]TaskGroup, 0, len(groups))
	for _, group := range groups {
		subjects := normalizeSubjects(group.Subjects)
		if len(subjects) == 0 {
			result = append(result, group)
			continue
		}
		for start := 0; start < len(subjects); {
			size := minInt(maxSubjects, len(subjects)-start)
			for size > 0 {
				chunk := append([]string(nil), subjects[start:start+size]...)
				externals := make(map[string]string, len(chunk))
				for _, subject := range chunk {
					externals[subject] = group.ExternalSymbols[subject]
				}
				_, err := buildManagedEnvironment(NodeAssignment{
					Provider: group.Provider, MarketType: group.MarketType, MarketID: group.MarketID, InstrumentType: group.InstrumentType, SourceID: group.SourceID, SeriesTag: group.SeriesTag, DatasetID: group.DatasetID,
					Frequency: group.Frequency, Subjects: chunk, ExternalSymbols: externals, Enabled: true,
				}, snapshot, managedBudget)
				if err == nil {
					result = append(result, TaskGroup{Provider: group.Provider, MarketType: group.MarketType, MarketID: group.MarketID, InstrumentType: group.InstrumentType, SourceID: group.SourceID, SeriesTag: group.SeriesTag, DatasetID: group.DatasetID, Frequency: group.Frequency, Subjects: chunk, ExternalSymbols: externals})
					start += size
					break
				}
				if size == 1 {
					return nil, fmt.Errorf("subject %s cannot fit in timer environment: %w", chunk[0], err)
				}
				size--
			}
		}
	}
	return result, nil
}

func managedEnvironmentBudget(nodes []scfinvoker.Node, limits ...int) (int, error) {
	budget := maxManagedEnvironmentSize
	if len(limits) > 0 && limits[0] > 0 {
		budget = limits[0]
	}
	for _, node := range nodes {
		_, exists := node.Metadata["managed_environment_budget_bytes"]
		if !exists {
			continue
		}
		value := metadataInt(node.Metadata, "managed_environment_budget_bytes")
		if value <= 0 {
			return 0, fmt.Errorf("node %s reports no available timer environment budget", node.NodeID)
		}
		if value < budget {
			budget = value
		}
	}
	return budget, nil
}

func managedEnvironmentLimit(stockCN bool) int {
	if stockCN {
		return stockCNMaxManagedEnvironmentSize
	}
	return maxManagedEnvironmentSize
}

func (r *Reconciler) stockCNTradingDate() string {
	now := time.Now()
	if r != nil && r.Now != nil {
		now = r.Now()
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	return now.In(location).Format("2006-01-02")
}

func metadataInt(metadata map[string]any, key string) int {
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		parsed, _ := strconv.Atoi(strings.TrimSpace(fmt.Sprint(typed)))
		return parsed
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (r *Reconciler) observeAssignmentDesiredMetrics(spaceID string, groups []TaskGroup, assignments []NodeAssignment) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ResetAssignmentScope(spaceID)
	for _, scope := range assignmentMetricScopes(groups, assignments) {
		r.Metrics.ObserveAssignmentDesired(spaceID, scope.DatasetID, scope.Frequency, scope.Required, scope.Active)
		expected, actual := scope.Required, scope.Active
		if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
			expected, actual = r.ExpectedStockCNTimerFunctions, len(assignments)
			routeID := marketRouteID(spaceID, scope.Provider, scope.MarketType, scope.Frequency)
			r.Metrics.ResetConfiguredGroupIDs(marketMetricID(spaceID), routeID)
			for _, assignment := range assignments {
				r.Metrics.ObserveConfiguredGroupID(marketMetricID(spaceID), routeID, assignment.GroupID)
			}
		}
		r.Metrics.ObserveConfiguredGroups(marketMetricID(spaceID), marketRouteID(spaceID, scope.Provider, scope.MarketType, scope.Frequency), expected, actual)
	}
}

func (r *Reconciler) observeAssignmentRequirements(spaceID string, groups []TaskGroup) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ResetAssignmentRequirements(spaceID)
	for _, scope := range assignmentMetricScopes(groups, nil) {
		r.Metrics.ObserveAssignmentRequired(spaceID, scope.DatasetID, scope.Frequency, scope.Required)
	}
}

func (r *Reconciler) observeTimerCapacity(spaceID string, nodes []scfinvoker.Node, groups []TaskGroup, assignments []NodeAssignment) {
	if r == nil || r.Metrics == nil {
		return
	}
	required := 0
	for _, scope := range assignmentMetricScopes(groups, nil) {
		required += scope.Required
	}
	active := 0
	for _, assignment := range assignments {
		if assignment.Enabled && len(assignment.Subjects) > 0 {
			active++
		}
	}
	r.Metrics.ObserveTimerCapacity(spaceID, len(nodes), required, active)
}

func (r *Reconciler) observeTimerStates(spaceID string, nodes []scfinvoker.Node) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ResetTimerScope(spaceID)
	for _, node := range nodes {
		enabled := metadataBool(node.Metadata, "timer_enabled")
		status, hasStatus := node.Metadata["timer_available_status"]
		available := strings.TrimSpace(fmt.Sprint(status))
		value := -1.0
		// CloudNode reports Unknown when the provider readback is unavailable
		// (for example, a transient Tencent API limit). Unknown is not proof
		// that the trigger is down; keep the documented -1 value so Monitor
		// ignores this observation until the next bounded readback.
		if hasStatus && available != "" && !strings.EqualFold(available, "unknown") {
			actualEnabled, hasActualEnabled := metadataBoolValue(node.Metadata, "timer_actual_enabled")
			actualType := strings.TrimSpace(fmt.Sprint(node.Metadata["timer_actual_type"]))
			actualCron := strings.TrimSpace(fmt.Sprint(node.Metadata["timer_actual_cron"]))
			actualQualifier := strings.TrimSpace(fmt.Sprint(node.Metadata["timer_actual_qualifier"]))
			actualMessage := strings.TrimSpace(fmt.Sprint(node.Metadata["timer_actual_message"]))
			desiredCron := strings.TrimSpace(fmt.Sprint(node.Metadata["timer_cron"]))
			healthy := strings.EqualFold(available, "available")
			if hasActualEnabled && actualEnabled != enabled {
				healthy = false
			}
			if actualType != "" && !strings.EqualFold(actualType, timerTriggerType) || actualQualifier != "" && actualQualifier != timerTriggerQualifier || actualMessage != "" && actualMessage != timerTriggerMessage {
				healthy = false
			}
			if actualType == "" || actualQualifier == "" || actualMessage == "" {
				// A fresh CloudNode readback always includes the protocol fields;
				// missing fields are an unknown/unsafe trigger, not healthy.
				healthy = false
			}
			if enabled && actualCron != "" && desiredCron != "" && actualCron != desiredCron {
				healthy = false
			}
			if healthy {
				value = 1
			} else {
				value = 0
			}
		}
		r.Metrics.ObserveTimerState(spaceID, node.NodeID, strconv.FormatBool(enabled), value)
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	value, ok := metadataBoolValue(metadata, key)
	return ok && value
}

func metadataBoolValue(metadata map[string]any, key string) (bool, bool) {
	value, ok := metadata[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true"), true
	default:
		return fmt.Sprint(typed) == "1", true
	}
}

func currentDNSHash(nodeID string, nodes []scfinvoker.Node) string {
	for _, node := range nodes {
		if node.NodeID == nodeID {
			return fmt.Sprint(node.Metadata["dns_hash"])
		}
	}
	return ""
}

func (r *Reconciler) pendingRuntimeJobState() (string, time.Time) {
	jobs, since := r.pendingRuntimeJobsState()
	if len(jobs) == 0 {
		return "", since
	}
	return jobs[0], since
}

func (r *Reconciler) pendingRuntimeJobsState() ([]string, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs := append([]string(nil), r.pendingJobs...)
	if len(jobs) == 0 && r.pendingJob != "" {
		jobs = []string{r.pendingJob}
	}
	return jobs, r.pendingSince
}

func (r *Reconciler) pendingRuntimeSince() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingSince
}

func (r *Reconciler) markSubmitRetryPending() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingSince.IsZero() {
		r.pendingSince = time.Now().UTC()
	}
	return r.pendingSince
}

func (r *Reconciler) clearSubmitRetryPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingJob == "" && len(r.pendingJobs) == 0 {
		r.pendingSince = time.Time{}
	}
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func (r *Reconciler) fail(spaceID, reason string, err error) error {
	if r != nil && r.Metrics != nil {
		r.Metrics.ObserveAssignmentFailure(spaceID, reason)
	}
	return err
}

func (r *Reconciler) setPendingRuntimeJobs(jobIDs []string, fingerprints map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingJobs = append([]string(nil), jobIDs...)
	r.pendingJob = ""
	if len(r.pendingJobs) > 0 {
		r.pendingJob = r.pendingJobs[0]
	}
	r.pending = make(map[string]string, len(fingerprints))
	r.pendingAt = make(map[string]time.Time, len(fingerprints))
	now := time.Now().UTC()
	for nodeID, fingerprint := range fingerprints {
		r.pending[nodeID] = fingerprint
		r.pendingAt[nodeID] = now
	}
	if len(r.pendingJobs) > 0 && r.pendingSince.IsZero() {
		r.pendingSince = now
	}
}

func (r *Reconciler) clearPendingRuntimeJobs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingJobs = nil
	r.pendingJob = ""
	r.pendingSince = time.Time{}
	r.pending = make(map[string]string)
	r.pendingAt = make(map[string]time.Time)
}

func (r *Reconciler) clearPendingRuntimeJob(jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingJob != jobID && !containsString(r.pendingJobs, jobID) {
		return
	}
	remaining := r.pendingJobs[:0]
	for _, pendingJob := range r.pendingJobs {
		if pendingJob != jobID {
			remaining = append(remaining, pendingJob)
		}
	}
	r.pendingJobs = remaining
	if len(r.pendingJobs) > 0 {
		r.pendingJob = r.pendingJobs[0]
		return
	}
	r.pendingJob = ""
	r.pendingSince = time.Time{}
	r.pending = make(map[string]string)
	r.pendingAt = make(map[string]time.Time)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (r *Reconciler) observeAssignmentPending(spaceID string, pending bool, since time.Time) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ObserveAssignmentPending(spaceID, pending, since)
}

func (r *Reconciler) observeAssignmentMetrics(spaceID string, groups []TaskGroup, assignments []NodeAssignment, reconciledAt int64) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ResetAssignmentScope(spaceID)
	for _, scope := range assignmentMetricScopes(groups, assignments) {
		r.Metrics.ObserveAssignment(spaceID, scope.DatasetID, scope.Frequency, scope.Required, scope.Active, reconciledAt)
		expected, actual := scope.Required, scope.Active
		if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
			expected, actual = r.ExpectedStockCNTimerFunctions, len(assignments)
			routeID := marketRouteID(spaceID, scope.Provider, scope.MarketType, scope.Frequency)
			r.Metrics.ResetConfiguredGroupIDs(marketMetricID(spaceID), routeID)
			for _, assignment := range assignments {
				r.Metrics.ObserveConfiguredGroupID(marketMetricID(spaceID), routeID, assignment.GroupID)
			}
		}
		r.Metrics.ObserveConfiguredGroups(marketMetricID(spaceID), marketRouteID(spaceID, scope.Provider, scope.MarketType, scope.Frequency), expected, actual)
	}
	// A valid reconciliation with zero enabled groups is still a success. Set
	// the space-level health outside the scope loop so a previous failure can
	// recover after rules are disabled or removed.
	r.Metrics.ObserveAssignmentSuccess(spaceID, reconciledAt)
}

func marketMetricID(spaceID string) string {
	if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
		return StockCNSpaceID
	}
	return "crypto"
}

func marketRouteID(spaceID, provider, marketType, frequency string) string {
	if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
		return StockCNRouteID
	}
	if strings.EqualFold(strings.TrimSpace(provider), "binance") {
		product := "spot"
		if strings.EqualFold(strings.TrimSpace(marketType), "swap") {
			product = "swap"
		}
		if parsed, err := marketdata.ParseFrequency(frequency); err == nil {
			frequency = string(parsed)
		} else {
			frequency = strings.ToLower(strings.TrimSpace(frequency))
		}
		return "binance_" + product + "_kline_" + frequency
	}
	return "unknown"
}

type assignmentMetricScope struct {
	Provider, MarketType, DatasetID, Frequency string
	Required, Active                           int
}

func assignmentMetricScopes(groups []TaskGroup, assignments []NodeAssignment) []assignmentMetricScope {
	byKey := make(map[string]assignmentMetricScope)
	for _, group := range groups {
		key := assignmentMetricKey(group.DatasetID, group.Frequency)
		scope := byKey[key]
		scope.Provider, scope.MarketType, scope.DatasetID, scope.Frequency = group.Provider, group.MarketType, group.DatasetID, group.Frequency
		scope.Required++
		byKey[key] = scope
	}
	for _, assignment := range assignments {
		if len(assignment.Subjects) == 0 {
			continue
		}
		key := assignmentMetricKey(assignment.DatasetID, assignment.Frequency)
		scope, ok := byKey[key]
		if !ok {
			continue
		}
		scope.Active++
		byKey[key] = scope
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]assignmentMetricScope, 0, len(keys))
	for _, key := range keys {
		result = append(result, byKey[key])
	}
	return result
}

func assignmentMetricKey(datasetID, frequency string) string {
	return strings.TrimSpace(datasetID) + "\x00" + strings.TrimSpace(frequency)
}

func (r *Reconciler) groups(ctx context.Context, spaceID string) ([]TaskGroup, error) {
	rules, err := r.Rules.ListEnabled(ctx, spaceID)
	if err != nil {
		return nil, fmt.Errorf("list enabled collection rules: %w", err)
	}
	groups := make([]TaskGroup, 0)
	for _, rule := range rules {
		params, parseErr := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
		if parseErr != nil {
			return nil, fmt.Errorf("parse rule %s: %w", rule.RuleID, parseErr)
		}
		if params.Collector.DataType != "kline" {
			continue
		}
		dataset, datasetErr := r.Symbols.GetDataset(ctx, spaceID, params.Source.DatasetID)
		if datasetErr != nil {
			return nil, fmt.Errorf("get symbol dataset %s: %w", params.Source.DatasetID, datasetErr)
		}
		subjects, subjectErr := r.Symbols.ListSubjects(ctx, spaceID, params.Source.DatasetID, dataset.DataSourceID)
		if subjectErr != nil {
			return nil, fmt.Errorf("list symbol dataset %s: %w", params.Source.DatasetID, subjectErr)
		}
		symbolIDs := make([]string, 0, len(subjects))
		externalSymbols := make(map[string]string, len(subjects))
		for _, subject := range subjects {
			if !strings.EqualFold(strings.TrimSpace(subject.Status), "active") {
				continue
			}
			subjectID := strings.ToUpper(strings.TrimSpace(subject.SubjectID))
			external, symbolErr := marketProviderSymbol(params.MarketType, subjectID, subject.ExternalSymbol)
			if symbolErr != nil {
				return nil, fmt.Errorf("invalid stock symbol subject %s: %w", subjectID, symbolErr)
			}
			symbolIDs = append(symbolIDs, subjectID)
			externalSymbols[subjectID] = external
		}
		marketID, instrumentType := marketIdentity(params.MarketID, params.InstrumentType, params.Target.DatasetID)
		for _, frequency := range params.Collector.Intervals {
			groups = append(groups, TaskGroup{Provider: params.Provider, MarketType: params.MarketType, MarketID: marketID, InstrumentType: instrumentType, SourceID: params.SourceID, SeriesTag: params.SeriesTag, DatasetID: params.Target.DatasetID, Frequency: frequency, Subjects: symbolIDs, ExternalSymbols: externalSymbols})
		}
	}
	return mergeGroups(groups), nil
}

func marketIdentity(marketID, instrumentType, datasetID string) (string, string) {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	instrumentType = strings.ToLower(strings.TrimSpace(instrumentType))
	if marketID != "" && instrumentType != "" {
		return marketID, instrumentType
	}
	datasetID = strings.ToLower(strings.TrimSpace(datasetID))
	switch {
	case strings.HasPrefix(datasetID, "stock_cn_index"):
		return "stock_cn", "index"
	case strings.HasPrefix(datasetID, "stock_cn_convertible_bond"):
		return "stock_cn", "convertible_bond"
	case strings.HasPrefix(datasetID, "stock_hk"):
		return "stock_hk", "equity"
	case strings.HasPrefix(datasetID, "stock_us"):
		return "stock_us", "equity"
	case strings.HasPrefix(datasetID, "stock_cn"):
		return "stock_cn", "equity"
	default:
		return marketID, instrumentType
	}
}

func mergeGroups(groups []TaskGroup) []TaskGroup {
	byKey := make(map[string]TaskGroup)
	for _, group := range groups {
		key := groupKey(group)
		current := byKey[key]
		current.Provider, current.MarketType = group.Provider, group.MarketType
		current.MarketID, current.InstrumentType = group.MarketID, group.InstrumentType
		current.SourceID, current.SeriesTag = group.SourceID, group.SeriesTag
		current.DatasetID, current.Frequency = group.DatasetID, group.Frequency
		current.Subjects = append(current.Subjects, group.Subjects...)
		if group.ExternalSymbols != nil {
			if current.ExternalSymbols == nil {
				current.ExternalSymbols = make(map[string]string)
			}
			for subject, external := range group.ExternalSymbols {
				if previous, exists := current.ExternalSymbols[subject]; exists {
					if previous != external {
						current.ExternalSymbols[subject] = ""
					}
					continue
				}
				current.ExternalSymbols[subject] = external
			}
		}
		byKey[key] = current
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]TaskGroup, 0, len(keys))
	for _, key := range keys {
		group := byKey[key]
		group.Subjects = normalizeSubjects(group.Subjects)
		if len(group.Subjects) > 0 {
			result = append(result, group)
		}
	}
	return result
}

func (r *Reconciler) maxSubjects() int {
	if r.MaxSubjects > 0 && r.MaxSubjects <= 30 {
		return r.MaxSubjects
	}
	return 30
}

func (r *Reconciler) shouldPatch(assignment NodeAssignment, nodes []scfinvoker.Node, fingerprint string) bool {
	for _, node := range nodes {
		if node.NodeID != assignment.NodeID {
			continue
		}
		metadata := node.Metadata
		storedDNSHash := fmt.Sprint(metadata["dns_hash"])
		if !assignment.Enabled {
			// DNS is intentionally ignored for disabled assignments; otherwise
			// every five-minute resolver refresh would rewrite all spare nodes.
			storedDNSHash = ""
		}
		stored := fmt.Sprintf("%v\x00%v\x00%v\x00%v", metadata["assignment_hash"], storedDNSHash, metadata["timer_enabled"], metadata["timer_cron"])
		if stored == fingerprint {
			if timerTriggerNeedsRepair(assignment, metadata) {
				return true
			}
			return false
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending[assignment.NodeID] == fingerprint && time.Since(r.pendingAt[assignment.NodeID]) < 2*time.Minute {
		return false
	}
	return true
}

func timerTriggerNeedsRepair(assignment NodeAssignment, metadata map[string]any) bool {
	// A Tencent readback can be temporarily Unknown when the account-level API
	// rate limit is hit. Do not immediately enqueue another full environment
	// update for that node; the next bounded readback will recover the state.
	status, _ := metadata["timer_available_status"].(string)
	statusError, _ := metadata["timer_status_error"].(string)
	if strings.EqualFold(strings.TrimSpace(status), "unknown") && strings.TrimSpace(statusError) != "" {
		return false
	}
	actualEnabled, hasActualEnabled := metadataBoolValue(metadata, "timer_actual_enabled")
	if !hasActualEnabled || actualEnabled != assignment.Enabled {
		return true
	}
	status = strings.TrimSpace(status)
	if assignment.Enabled && !strings.EqualFold(status, "available") {
		return true
	}
	if actualType := strings.TrimSpace(fmt.Sprint(metadata["timer_actual_type"])); !strings.EqualFold(actualType, timerTriggerType) {
		return true
	}
	if actualQualifier := strings.TrimSpace(fmt.Sprint(metadata["timer_actual_qualifier"])); actualQualifier != timerTriggerQualifier {
		return true
	}
	if actualMessage := strings.TrimSpace(fmt.Sprint(metadata["timer_actual_message"])); actualMessage != timerTriggerMessage {
		return true
	}
	desiredCron := assignment.Cron
	if desiredCron == "" {
		desiredCron = "0 * * * * * *"
	}
	return strings.TrimSpace(fmt.Sprint(metadata["timer_actual_cron"])) != desiredCron
}
