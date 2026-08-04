package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	timerTriggerType      = "timer"
	timerTriggerQualifier = "$LATEST"
	timerTriggerMessage   = "market_fetch_timer_v1"
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
	Rules       ruleSource
	Symbols     datasetSource
	Nodes       runtimeConfigClient
	Instances   *store.TaskInstanceRepository
	DNS         dnsSnapshotter
	Metrics     *Metrics
	MaxSubjects int
	mu          sync.Mutex
	reconcileMu sync.Mutex
	pending     map[string]string
	pendingAt   map[string]time.Time
	pendingJob  string
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
	if pendingJob := r.pendingRuntimeJob(); pendingJob != "" {
		status, statusErr := r.Nodes.GetRuntimeConfigBatchStatus(ctx, spaceID, pendingJob)
		if statusErr != nil {
			return r.fail(spaceID, "cloudnode", fmt.Errorf("get timer runtime config job %s: %w", pendingJob, statusErr))
		}
		switch status.GetStatus() {
		case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING, cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING:
			log.InfoContextf(ctx, "collector_scf_timer_reconciliation_pending space=%s job=%s status=%s", spaceID, pendingJob, status.GetStatus().String())
			return nil
		case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED:
			r.clearPendingRuntimeJob(pendingJob)
			return r.fail(spaceID, "cloudnode", fmt.Errorf("timer runtime config job %s failed", pendingJob))
		case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL:
			r.clearPendingRuntimeJob(pendingJob)
			return r.fail(spaceID, "cloudnode", fmt.Errorf("timer runtime config job %s partially failed", pendingJob))
		case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS:
			r.clearPendingRuntimeJob(pendingJob)
		default:
			return r.fail(spaceID, "cloudnode", fmt.Errorf("timer runtime config job %s returned unknown status %s", pendingJob, status.GetStatus().String()))
		}
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
	dns := map[string]sources.DNSResolution(nil)
	if r.DNS != nil {
		dns = r.DNS.Snapshot()
	}
	// Publish the unsplit requirement before any local environment/capacity
	// validation. A malformed budget or an individual symbol that cannot fit
	// must still become a visible Monitor coordination failure.
	r.observeAssignmentRequirements(spaceID, groups)
	managedBudget, budgetErr := managedEnvironmentBudget(nodes)
	if budgetErr != nil {
		return r.fail(spaceID, "environment", budgetErr)
	}
	// The 30-subject limit is a business ceiling, not a promise that every
	// legal symbol fits in Tencent's 4KB Environment. Long symbols and DNS
	// routes can consume the remaining bytes, so split a group further before
	// checking node capacity instead of retrying the same oversized patch.
	groups, err = splitGroupsForEnvironment(groups, dns, r.maxSubjects(), managedBudget)
	if err != nil {
		return r.fail(spaceID, "environment", err)
	}
	r.observeAssignmentRequirements(spaceID, groups)
	assignments, err := BuildAssignments(groups, nodes, r.maxSubjects())
	if err != nil {
		return r.fail(spaceID, "capacity", err)
	}
	dnsAvailable := len(dns) > 0
	patches := make([]*cloudnodepb.NodeRuntimeConfigPatch, 0, len(assignments))
	pendingFingerprints := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		environment, envErr := BuildManagedEnvironment(assignment, dns)
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
			dnsHash = currentDNSHash(assignment.NodeID, nodes)
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
	jobID, err := r.Nodes.SubmitRuntimeConfigs(ctx, spaceID, patches)
	if err != nil {
		return r.fail(spaceID, "cloudnode", fmt.Errorf("submit timer runtime configs: %w", err))
	}
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[string]string)
	}
	if r.pendingAt == nil {
		r.pendingAt = make(map[string]time.Time)
	}
	for nodeID, fingerprint := range pendingFingerprints {
		r.pending[nodeID] = fingerprint
		r.pendingAt[nodeID] = time.Now().UTC()
	}
	r.pendingJob = jobID
	r.mu.Unlock()
	log.InfoContextf(ctx, "collector_scf_timer_reconciled space=%s nodes=%d patches=%d job=%s", spaceID, len(nodes), len(patches), jobID)
	r.observeAssignmentDesiredMetrics(spaceID, groups, assignments)
	return nil
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
		replacements = append(replacements, store.MarketFetchAssignment{Provider: assignment.Provider, MarketType: assignment.MarketType, DatasetID: assignment.DatasetID, Frequency: assignment.Frequency, FunctionName: assignment.FunctionName, Subjects: assignment.Subjects})
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
					Provider: group.Provider, MarketType: group.MarketType, DatasetID: group.DatasetID,
					Frequency: group.Frequency, Subjects: chunk, ExternalSymbols: externals, Enabled: true,
				}, snapshot, managedBudget)
				if err == nil {
					result = append(result, TaskGroup{Provider: group.Provider, MarketType: group.MarketType, DatasetID: group.DatasetID, Frequency: group.Frequency, Subjects: chunk, ExternalSymbols: externals})
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

func managedEnvironmentBudget(nodes []scfinvoker.Node) (int, error) {
	budget := maxManagedEnvironmentSize
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
	}
}

func (r *Reconciler) observeAssignmentRequirements(spaceID string, groups []TaskGroup) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ResetAssignmentScope(spaceID)
	for _, scope := range assignmentMetricScopes(groups, nil) {
		r.Metrics.ObserveAssignmentDesired(spaceID, scope.DatasetID, scope.Frequency, scope.Required, 0)
	}
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
		if hasStatus {
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

func (r *Reconciler) pendingRuntimeJob() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingJob
}

func (r *Reconciler) fail(spaceID, reason string, err error) error {
	if r != nil && r.Metrics != nil {
		r.Metrics.ObserveAssignmentFailure(spaceID, reason)
	}
	return err
}

func (r *Reconciler) clearPendingRuntimeJob(jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingJob != jobID {
		return
	}
	r.pendingJob = ""
	r.pending = make(map[string]string)
	r.pendingAt = make(map[string]time.Time)
}

func (r *Reconciler) observeAssignmentMetrics(spaceID string, groups []TaskGroup, assignments []NodeAssignment, reconciledAt int64) {
	if r == nil || r.Metrics == nil {
		return
	}
	r.Metrics.ResetAssignmentScope(spaceID)
	for _, scope := range assignmentMetricScopes(groups, assignments) {
		r.Metrics.ObserveAssignment(spaceID, scope.DatasetID, scope.Frequency, scope.Required, scope.Active, reconciledAt)
	}
	// A valid reconciliation with zero enabled groups is still a success. Set
	// the space-level health outside the scope loop so a previous failure can
	// recover after rules are disabled or removed.
	r.Metrics.ObserveAssignmentSuccess(spaceID, reconciledAt)
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
			if strings.EqualFold(strings.TrimSpace(subject.Status), "active") && strings.TrimSpace(subject.SubjectID) != "" {
				subjectID := strings.ToUpper(strings.TrimSpace(subject.SubjectID))
				symbolIDs = append(symbolIDs, subjectID)
				externalSymbols[subjectID] = strings.TrimSpace(subject.ExternalSymbol)
			}
		}
		for _, frequency := range params.Collector.Intervals {
			groups = append(groups, TaskGroup{Provider: params.Provider, MarketType: params.MarketType, DatasetID: params.Target.DatasetID, Frequency: frequency, Subjects: symbolIDs, ExternalSymbols: externalSymbols})
		}
	}
	return mergeGroups(groups), nil
}

func mergeGroups(groups []TaskGroup) []TaskGroup {
	byKey := make(map[string]TaskGroup)
	for _, group := range groups {
		key := groupKey(group)
		current := byKey[key]
		current.Provider, current.MarketType, current.DatasetID, current.Frequency = group.Provider, group.MarketType, group.DatasetID, group.Frequency
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
		stored := fmt.Sprintf("%v\x00%v\x00%v\x00%v", metadata["assignment_hash"], metadata["dns_hash"], metadata["timer_enabled"], metadata["timer_cron"])
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
	actualEnabled, hasActualEnabled := metadataBoolValue(metadata, "timer_actual_enabled")
	if !hasActualEnabled || actualEnabled != assignment.Enabled {
		return true
	}
	status := strings.TrimSpace(fmt.Sprint(metadata["timer_available_status"]))
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
