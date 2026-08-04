package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"trpc.group/trpc-go/trpc-go/log"
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
	DNS         dnsSnapshotter
	Metrics     *Metrics
	MaxSubjects int
	mu          sync.Mutex
	pending     map[string]string
	pendingAt   map[string]time.Time
}

func (r *Reconciler) Reconcile(ctx context.Context, spaceID string) error {
	if r == nil || r.Rules == nil || r.Symbols == nil || r.Nodes == nil {
		return fmt.Errorf("SCF timer reconciler is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	nodes, err := r.Nodes.ListTimerMarketFetchers(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("list timer market fetchers: %w", err)
	}
	groups, err := r.groups(ctx, spaceID)
	if err != nil {
		return err
	}
	assignments, err := BuildAssignments(groups, nodes, r.maxSubjects())
	if err != nil {
		if r.Metrics != nil {
			r.Metrics.ObserveAssignmentError(spaceID, "capacity")
		}
		return err
	}
	dns := map[string]sources.DNSResolution(nil)
	if r.DNS != nil {
		dns = r.DNS.Snapshot()
	}
	patches := make([]*cloudnodepb.NodeRuntimeConfigPatch, 0, len(assignments))
	pendingFingerprints := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		environment, envErr := BuildManagedEnvironment(assignment, dns)
		if envErr != nil {
			return envErr
		}
		cron := assignment.Cron
		if cron == "" {
			cron = "0 * * * * * *"
		}
		fingerprint := assignment.AssignmentHash + "\x00" + environment["MOOX_MARKET_FETCH_DNS_HASH"] + "\x00" + fmt.Sprint(assignment.Enabled) + "\x00" + cron
		if !r.shouldPatch(assignment, nodes, fingerprint) {
			continue
		}
		patches = append(patches, &cloudnodepb.NodeRuntimeConfigPatch{NodeId: assignment.NodeID, ManagedEnvironment: environment, TimerEnabled: assignment.Enabled, TimerCron: cron})
		pendingFingerprints[assignment.NodeID] = fingerprint
	}
	if len(patches) == 0 {
		r.observeAssignmentMetrics(spaceID, groups, assignments, time.Now().UTC().Unix())
		return nil
	}
	jobID, err := r.Nodes.SubmitRuntimeConfigs(ctx, spaceID, patches)
	if err != nil {
		return fmt.Errorf("submit timer runtime configs: %w", err)
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
	r.mu.Unlock()
	log.InfoContextf(ctx, "collector_scf_timer_reconciled space=%s nodes=%d patches=%d job=%s", spaceID, len(nodes), len(patches), jobID)
	r.observeAssignmentMetrics(spaceID, groups, assignments, time.Now().UTC().Unix())
	return nil
}

func (r *Reconciler) observeAssignmentMetrics(spaceID string, groups []TaskGroup, assignments []NodeAssignment, reconciledAt int64) {
	if r == nil || r.Metrics == nil {
		return
	}
	for _, group := range groups {
		required := (len(normalizeSubjects(group.Subjects)) + r.maxSubjects() - 1) / r.maxSubjects()
		r.Metrics.ObserveAssignment(spaceID, group.DatasetID, group.Frequency, required, countAssignments(assignments, group), reconciledAt)
	}
}

func countAssignments(assignments []NodeAssignment, group TaskGroup) int {
	count := 0
	for _, assignment := range assignments {
		if assignment.Provider == group.Provider && assignment.MarketType == group.MarketType && assignment.DatasetID == group.DatasetID && assignment.Frequency == group.Frequency && len(assignment.Subjects) > 0 {
			count++
		}
	}
	return count
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
