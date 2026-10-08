package marketfetch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type reconcilerTasksStub struct{ tasks []domain.CollectionTask }

func TestReconcilerMissingClaimRouteCannotEnableTimer(t *testing.T) {
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer", FunctionName: "market-fetcher-timer", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"}}}
	r := &Reconciler{Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", Enabled: true, DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}}, Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}}, Nodes: nodes}
	require.ErrorContains(t, r.Reconcile(context.Background(), "crypto"), "collector runtime")
	require.Zero(t, nodes.submits)
}

func TestMarketRouteIDBoundsFrequencyLabels(t *testing.T) {
	require.Equal(t, "binance_spot_kline_1h", marketRouteID("crypto", "binance", "spot", "60m"))
	require.Equal(t, "unknown", marketRouteID("crypto", "binance", "spot", "task-specific-frequency"))
}

func TestDefaultMaxSubjectsUsesSmallerCryptoShards(t *testing.T) {
	require.Equal(t, 30, DefaultMaxSubjects("crypto"))
	require.Equal(t, 40, DefaultMaxSubjects("stockcn"))
}

func TestReconcilerUpgradesSameAssignmentClaimRouting(t *testing.T) {
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer", FunctionName: "timer", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"}}}
	r := &Reconciler{
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", Enabled: true, DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}}, Nodes: nodes,
		CollectorRuntimeGatewayTarget: "ip://collector.example:11002", CollectorRuntimeGatewayNodeID: "control-a",
	}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits)
	// Keep assignment, DNS and actual Timer metadata unchanged, but remove the
	// new runtime metadata to model a pre-period fleet.
	delete(nodes.nodes[0].Metadata, "binding_hash")
	delete(nodes.nodes[0].Metadata, "collector_rpc_gateway_target")
	delete(nodes.nodes[0].Metadata, "collector_gateway_target_node")
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 2, nodes.submits, "existing assignments must receive the Claim routing upgrade")
	require.Equal(t, "control-a", nodes.patches[0].GetManagedEnvironment()["MOOX_COLLECTOR_GATEWAY_TARGET_NODE"])
	r.CollectorRuntimeGatewayTarget = "ip://new-control.example:11002"
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 3, nodes.submits, "routing changes must repatch even when assignment is unchanged")
}

func TestDisableBlacklistedTimersHandlesMissingObservation(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata map[string]any
		patch    bool
	}{
		{"missing cron", map[string]any{"timer_enabled": true}, true},
		{"disabled with unknown actual", map[string]any{"timer_enabled": false}, false},
		{"disabled but actual enabled", map[string]any{"timer_enabled": false, "timer_actual_enabled": true}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			nodes := &reconcilerNodesStub{}
			r := &Reconciler{Nodes: nodes}
			submitted, err := r.disableBlacklistedTimers(context.Background(), "crypto", []scfinvoker.Node{{NodeID: "blocked", Metadata: test.metadata}})
			require.NoError(t, err)
			require.Equal(t, test.patch, submitted)
			if test.patch {
				require.Len(t, nodes.patches, 1)
				require.Equal(t, "0 * * * * * *", nodes.patches[0].GetTimerCron())
				require.False(t, nodes.patches[0].GetTimerEnabled())
			} else {
				require.Empty(t, nodes.patches)
			}
		})
	}
}

func TestReconcilerRegionBlacklistMigratesOrPreservesOnCapacityFailure(t *testing.T) {
	for _, capacity := range []bool{true, false} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "blocked", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"timer_enabled": true, "timer_cron": "0 * * * * * *"}}}}
			if capacity {
				nodes.nodes = append(nodes.nodes, scfinvoker.Node{NodeID: "allowed", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"})
			}
			r := &Reconciler{
				CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
				SCFRegionBlacklists: map[string][]string{"crypto": {"ap-guangzhou"}},
				Tasks:               reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true, CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}},
				Symbols:             reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}}, Nodes: nodes,
			}
			err := r.Reconcile(context.Background(), "crypto")
			require.NoError(t, err)
			require.Len(t, nodes.patches, 1)
			require.Equal(t, "blocked", nodes.patches[0].GetNodeId())
			require.False(t, nodes.patches[0].GetTimerEnabled())
			require.Empty(t, nodes.patches[0].GetManagedEnvironment())
			err = r.Reconcile(context.Background(), "crypto")
			if !capacity {
				require.Error(t, err)
				require.Equal(t, 1, nodes.submits, "capacity failure must not modify allowed timers")
				return
			}
			require.NoError(t, err)
			require.Len(t, nodes.patches, 1)
			require.Equal(t, "allowed", nodes.patches[0].GetNodeId())
			require.True(t, nodes.patches[0].GetTimerEnabled())
		})
	}
}

func TestFilterSCFRegionsIsSpaceScoped(t *testing.T) {
	nodes := []scfinvoker.Node{{NodeID: "invoke", Region: "ap-guangzhou", TriggerType: "invoke"}, {NodeID: "instrument", Region: "ap-guangzhou", TriggerType: "timer", Metadata: map[string]any{"function_mode": "instrument_snapshot"}}, {NodeID: "allowed", Region: "ap-singapore", TriggerType: "timer"}}
	blacklists := map[string][]string{"crypto": {"ap-guangzhou"}}
	allowed, blocked := filterSCFRegions(nodes, "crypto", blacklists)
	require.Len(t, allowed, 1)
	require.Equal(t, "allowed", allowed[0].NodeID)
	require.Len(t, blocked, 2)
	allowed, blocked = filterSCFRegions(nodes, "stockcn", blacklists)
	require.Equal(t, nodes, allowed)
	require.Empty(t, blocked)
}

type reconcilerInstrumentNodesStub struct{ *reconcilerNodesStub }

func (s reconcilerInstrumentNodesStub) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (s reconcilerInstrumentNodesStub) ListInstrumentSnapshotTimers(context.Context, string) ([]scfinvoker.Node, error) {
	return s.nodes, nil
}

func TestReconcilerDisablesBlacklistedInstrumentWithoutChangingIdentity(t *testing.T) {
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "blocked-instrument", Region: "ap-guangzhou", Metadata: map[string]any{"function_mode": "instrument_snapshot", "timer_enabled": true, "timer_cron": "0 0 8 * * * *"}},
		{NodeID: "allowed-instrument", Region: "ap-singapore", Metadata: map[string]any{"function_mode": "instrument_snapshot", "timer_enabled": true, "timer_cron": "0 0 8 * * * *"}},
	}}
	r := &Reconciler{SCFRegionBlacklists: map[string][]string{"crypto": {"ap-guangzhou"}}, Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: reconcilerInstrumentNodesStub{nodes}}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Empty(t, nodes.patches)
}

func (s reconcilerTasksStub) ListEnabled(context.Context, string) ([]domain.CollectionTask, error) {
	return append([]domain.CollectionTask(nil), s.tasks...), nil
}

type reconcilerSymbolsStub struct {
	dataset  storagesource.DatasetInfo
	subjects []domain.DatasetSubject
	datasets map[string]storagesource.DatasetInfo
	getErrs  map[string]error
	listErrs map[string]error
}

func (s reconcilerSymbolsStub) GetDataset(_ context.Context, _ string, datasetID string) (storagesource.DatasetInfo, error) {
	if err := s.getErrs[datasetID]; err != nil {
		return storagesource.DatasetInfo{}, err
	}
	if dataset, ok := s.datasets[datasetID]; ok {
		return dataset, nil
	}
	return s.dataset, nil
}

func (s reconcilerSymbolsStub) ListSubjects(_ context.Context, _ string, datasetID, _ string) ([]domain.DatasetSubject, error) {
	if err := s.listErrs[datasetID]; err != nil {
		return nil, err
	}
	return append([]domain.DatasetSubject(nil), s.subjects...), nil
}

func (s reconcilerSymbolsStub) ResolveSubjects(_ context.Context, _ string, _ []string) ([]domain.Subject, error) {
	items := make([]domain.Subject, 0, len(s.subjects))
	for _, subject := range s.subjects {
		items = append(items, domain.Subject{SubjectID: subject.SubjectID, Name: subject.SubjectName, Status: subject.Status})
	}
	return items, nil
}

type reconcilerTagSymbolsStub struct {
	tags     map[string]*storagepb.Tag
	subjects map[string][]domain.Subject
}

func (s reconcilerTagSymbolsStub) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{}, nil
}

func (s reconcilerTagSymbolsStub) GetTag(_ context.Context, _ string, tagID string) (*storagepb.Tag, error) {
	tag := s.tags[tagID]
	if tag == nil {
		return nil, fmt.Errorf("tag %s not found", tagID)
	}
	return tag, nil
}

func (s reconcilerTagSymbolsStub) ResolveSubjects(_ context.Context, _ string, tagIDs []string) ([]domain.Subject, error) {
	var result []domain.Subject
	for _, tagID := range tagIDs {
		result = append(result, s.subjects[tagID]...)
	}
	return result, nil
}

func TestReconcilerUsesTaskTagsAsAuthoritativeCrossProviderRoutes(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "cross-provider", DataType: "kline", Enabled: true,
		TagIDs:        []string{"binance_spot", "okx_spot"},
		CollectParams: `{"target_dataset_id":"task_dataset","frequency":"1m"}`,
	}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerTagSymbolsStub{
			tags: map[string]*storagepb.Tag{
				"binance_spot": {TagId: "binance_spot", Source: "binance", MarketType: "spot"},
				"okx_spot":     {TagId: "okx_spot", Source: "okx", MarketType: "spot"},
			},
			subjects: map[string][]domain.Subject{
				"binance_spot": {{SubjectID: "BTC-USDT", Status: "active"}},
				"okx_spot":     {{SubjectID: "BTC-USDT", Status: "active"}},
			},
		},
		ResolveSymbol: func(provider, _, _, subjectID string) (string, error) {
			return provider + ":" + subjectID, nil
		},
	}
	groups, err := r.groups(context.Background(), "crypto")
	require.NoError(t, err)
	require.Len(t, groups, 2)
	require.Equal(t, []string{"binance", "okx"}, []string{groups[0].Provider, groups[1].Provider})
	for _, group := range groups {
		require.Equal(t, "spot", group.MarketType)
		require.Equal(t, "task_dataset", group.DatasetID)
		require.Equal(t, []string{"BTC-USDT"}, group.Subjects)
		require.Equal(t, group.Provider+":BTC-USDT", group.ExternalSymbols["BTC-USDT"])
	}
}

type reconcilerNodesStub struct {
	nodes          []scfinvoker.Node
	patches        []*cloudnodepb.NodeRuntimeConfigPatch
	submits        int
	submitJobID    string
	submitJobIDSet bool
	listStarted    chan struct{}
	listRelease    chan struct{}
	listOnce       sync.Once
	submitErr      error
	batchStatuses  map[string]cloudnodepb.NodeBatchStatus
	batchErr       error
	batchErrors    map[string]error
	batchQueries   []string
	lease          *scfinvoker.CollectorPublishLease
	leaseError     error
	renewError     error
	leaseAcquires  int
	leaseRenews    int
	leaseReleases  int
}

func TestRuntimeConfigPatchBatchesRespectCloudNodeLimit(t *testing.T) {
	patches := make([]*cloudnodepb.NodeRuntimeConfigPatch, 201)
	batches := runtimeConfigPatchBatches(patches, runtimeConfigBatchSize)

	require.Len(t, batches, 3)
	require.Len(t, batches[0], 100)
	require.Len(t, batches[1], 100)
	require.Len(t, batches[2], 1)
}

func (s *reconcilerNodesStub) ListInstrumentSnapshotTimers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (s *reconcilerNodesStub) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	if s.listStarted != nil && s.listRelease != nil {
		s.listOnce.Do(func() { close(s.listStarted) })
		<-s.listRelease
	}
	result := make([]scfinvoker.Node, len(s.nodes))
	copy(result, s.nodes)
	return result, nil
}

func (s *reconcilerNodesStub) SubmitRuntimeConfigs(_ context.Context, _ string, patches []*cloudnodepb.NodeRuntimeConfigPatch) (string, error) {
	s.submits++
	s.patches = append([]*cloudnodepb.NodeRuntimeConfigPatch(nil), patches...)
	if s.submitErr != nil {
		return "", s.submitErr
	}
	for _, patch := range patches {
		for index := range s.nodes {
			if s.nodes[index].NodeID != patch.GetNodeId() {
				continue
			}
			if s.nodes[index].Metadata == nil {
				s.nodes[index].Metadata = map[string]any{}
			}
			s.nodes[index].Metadata["assignment_hash"] = patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
			s.nodes[index].Metadata["assignment_count"] = patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_SUBJECT_COUNT"]
			s.nodes[index].Metadata["binding_hash"] = patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_BINDING_HASH"]
			s.nodes[index].Metadata["collector_rpc_gateway_target"] = patch.GetManagedEnvironment()["MOOX_COLLECTOR_RPC_GATEWAY_TARGET"]
			s.nodes[index].Metadata["collector_gateway_target_node"] = patch.GetManagedEnvironment()["MOOX_COLLECTOR_GATEWAY_TARGET_NODE"]
			s.nodes[index].Metadata["fetch_timeout_seconds"] = patch.GetManagedEnvironment()["MOOX_FETCH_TIMEOUT_SECONDS"]
			s.nodes[index].Metadata["dns_hash"] = patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_DNS_HASH"]
			s.nodes[index].Metadata["timer_enabled"] = patch.GetTimerEnabled()
			s.nodes[index].Metadata["timer_cron"] = patch.GetTimerCron()
			s.nodes[index].Metadata["timer_available_status"] = "Available"
			s.nodes[index].Metadata["timer_actual_type"] = "timer"
			s.nodes[index].Metadata["timer_actual_enabled"] = patch.GetTimerEnabled()
			s.nodes[index].Metadata["timer_actual_cron"] = patch.GetTimerCron()
			s.nodes[index].Metadata["timer_actual_qualifier"] = "$LATEST"
			s.nodes[index].Metadata["timer_actual_message"] = "market_fetch_timer_v1"
		}
	}
	if s.submitJobIDSet {
		return s.submitJobID, nil
	}
	return "job-1", nil
}

func (s *reconcilerNodesStub) AcquireCollectorPublishLease(_ context.Context, spaceID, holderID string) (*scfinvoker.CollectorPublishLease, error) {
	s.leaseAcquires++
	if s.leaseError != nil {
		return nil, s.leaseError
	}
	s.lease = &scfinvoker.CollectorPublishLease{SpaceID: spaceID, LeaseID: "lease-" + holderID, HolderID: holderID, FencingToken: int64(s.leaseAcquires), ExpiresAt: time.Now().Add(time.Minute)}
	return s.lease, nil
}

func (s *reconcilerNodesStub) RenewCollectorPublishLease(_ context.Context, lease *scfinvoker.CollectorPublishLease) (*scfinvoker.CollectorPublishLease, error) {
	s.leaseRenews++
	if s.renewError != nil {
		return nil, s.renewError
	}
	if s.leaseError != nil {
		return nil, s.leaseError
	}
	if s.lease == nil || s.lease.LeaseID != lease.LeaseID {
		return nil, fmt.Errorf("lease is stale")
	}
	updated := *lease
	updated.ExpiresAt = time.Now().Add(time.Minute)
	s.lease = &updated
	return &updated, nil
}

func TestReconcilerRepairsLegacyAssignmentWithoutSubjectCount(t *testing.T) {
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer", FunctionName: "timer", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"}}}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", Enabled: true, DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
	}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits)
	require.Equal(t, "1", fmt.Sprint(nodes.nodes[0].Metadata["assignment_count"]))

	delete(nodes.nodes[0].Metadata, "assignment_count")
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 2, nodes.submits, "a legacy node with matching assignment hash but no count must be upgraded")
	require.Equal(t, "1", fmt.Sprint(nodes.nodes[0].Metadata["assignment_count"]))
}

func TestReconcilerEmptySubmitJobIDWaitsForExpiryThenFencesUnknownJob(t *testing.T) {
	nodes := &reconcilerNodesStub{
		nodes:          []scfinvoker.Node{{NodeID: "timer", FunctionName: "timer", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"}},
		submitJobIDSet: true,
		submitJobID:    "",
	}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", Enabled: true, DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
	}
	require.ErrorIs(t, r.Reconcile(context.Background(), "crypto"), scfinvoker.ErrRuntimeConfigSubmissionUnknown)
	jobs, _ := r.pendingRuntimeJobsState()
	require.Empty(t, jobs, "an absent job ID must not become an unqueryable pending job")
	require.True(t, r.pendingRuntimeSubmitUnknown())
	require.NotNil(t, r.publishLease, "retain the current fence while the response remains ambiguous")
	require.EqualValues(t, 1, r.publishLease.FencingToken)
	require.Zero(t, nodes.leaseReleases, "an unknown response must not release the lease")

	require.NoError(t, r.Reconcile(context.Background(), "crypto"), "the unresolved submission remains fenced until its lease expires")
	require.Equal(t, 1, nodes.leaseAcquires)
	require.Zero(t, nodes.leaseReleases)

	delete(nodes.nodes[0].Metadata, "assignment_count")
	r.publishLease.ExpiresAt = time.Now().Add(-time.Second)
	nodes.submitJobIDSet = false
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.False(t, r.pendingRuntimeSubmitUnknown())
	require.Equal(t, 2, nodes.leaseAcquires, "after expiry a new fencing token must be acquired before retrying")
	require.NotEmpty(t, nodes.patches[0].GetCollectorPublishLeaseId())
	require.EqualValues(t, 2, nodes.patches[0].GetCollectorPublishFencingToken())
	jobs, _ = r.pendingRuntimeJobsState()
	require.Equal(t, []string{"job-1"}, jobs)
}

func TestReconcilerStaleLeaseRenewalPollsTerminalJobThenAcquiresHigherFence(t *testing.T) {
	nodes := &reconcilerNodesStub{
		nodes:         []scfinvoker.Node{{NodeID: "timer", FunctionName: "timer", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"}},
		batchStatuses: map[string]cloudnodepb.NodeBatchStatus{"job-old": cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS},
		renewError:    scfinvoker.ErrCollectorPublishLeaseStale,
	}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", Enabled: true, DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
	}
	oldLease, err := r.ensurePublishLease(context.Background(), "crypto")
	require.NoError(t, err)
	r.setPendingRuntimeJobs([]string{"job-old"}, nil, true)

	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, []string{"job-old"}, nodes.batchQueries, "known work must be confirmed terminal before reacquiring")
	require.EqualValues(t, 2, nodes.leaseAcquires)
	require.Len(t, nodes.patches, 1)
	require.NotEqual(t, oldLease.LeaseID, nodes.patches[0].GetCollectorPublishLeaseId())
	require.Greater(t, nodes.patches[0].GetCollectorPublishFencingToken(), oldLease.FencingToken)
	require.NotNil(t, r.publishLease)
	require.Greater(t, r.publishLease.FencingToken, oldLease.FencingToken)
}

func TestReconcilerKeepsLeaseAndPendingJobsOnAmbiguousRenewFailure(t *testing.T) {
	nodes := &reconcilerNodesStub{
		batchStatuses: map[string]cloudnodepb.NodeBatchStatus{"job-1": cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING},
		renewError:    context.DeadlineExceeded,
	}
	r := &Reconciler{Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes}
	lease, err := r.ensurePublishLease(context.Background(), "crypto")
	require.NoError(t, err)
	r.setPendingRuntimeJobs([]string{"job-1"}, nil, true)

	require.ErrorIs(t, r.Reconcile(context.Background(), "crypto"), context.DeadlineExceeded)
	require.Same(t, lease, r.publishLease, "transport errors do not establish that the current fence is stale")
	jobs, _ := r.pendingRuntimeJobsState()
	require.Equal(t, []string{"job-1"}, jobs)
	require.Empty(t, nodes.batchQueries, "do not poll after an ambiguous failed renewal")
}

func (s *reconcilerNodesStub) ReleaseCollectorPublishLease(_ context.Context, lease *scfinvoker.CollectorPublishLease) error {
	s.leaseReleases++
	if s.leaseError != nil {
		return s.leaseError
	}
	if s.lease == nil || s.lease.LeaseID != lease.LeaseID {
		return fmt.Errorf("lease is stale")
	}
	s.lease = nil
	return nil
}

func TestTimerRuntimeConfigFencingLeaseCoversAsyncBatchLifetime(t *testing.T) {
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer", FunctionName: "market-fetcher-timer", Region: "ap-singapore", NodeType: "scf-event", TriggerType: "timer"}}}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", Enabled: true, DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
	}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.EqualValues(t, 1, nodes.leaseAcquires)
	require.Len(t, nodes.patches, 1)
	require.True(t, strings.HasPrefix(nodes.patches[0].GetCollectorPublishLeaseId(), "lease-timer-reconciler-"))
	require.EqualValues(t, 1, nodes.patches[0].GetCollectorPublishFencingToken())
	require.Zero(t, nodes.leaseReleases)

	nodes.batchStatuses = map[string]cloudnodepb.NodeBatchStatus{"job-1": cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.EqualValues(t, 1, nodes.leaseRenews)
	require.Zero(t, nodes.leaseReleases, "a higher fencing token must not be issued while CloudNode is applying this batch")

	nodes.batchStatuses["job-1"] = cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.EqualValues(t, 1, nodes.leaseReleases)
	require.Nil(t, r.publishLease)
}

func TestDisableBlacklistedTimersUsesPublishFence(t *testing.T) {
	nodes := &reconcilerNodesStub{}
	r := &Reconciler{Nodes: nodes}
	submitted, err := r.disableBlacklistedTimers(context.Background(), "crypto", []scfinvoker.Node{{
		NodeID: "blocked", Metadata: map[string]any{"timer_enabled": true},
	}})
	require.NoError(t, err)
	require.True(t, submitted)
	require.Len(t, nodes.patches, 1)
	require.NotEmpty(t, nodes.patches[0].GetCollectorPublishLeaseId())
	require.Positive(t, nodes.patches[0].GetCollectorPublishFencingToken())
	require.Zero(t, nodes.leaseReleases)
}

func TestReconcilerResolvesMissingInstrumentIdentity(t *testing.T) {
	for _, product := range []string{"spot", "swap"} {
		t.Run(product, func(t *testing.T) {
			r := &Reconciler{
				CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
				ResolveSourceID: func(provider, instrument string) string { return instrument + "_test" },
				Tasks:           reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", DataType: "kline", CollectParams: fmt.Sprintf(`{"provider":"binance","market_type":%q,"subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1h"}`, product)}}},
				Symbols:         reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbols"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
			}
			groups, err := r.groups(context.Background(), "crypto")
			require.NoError(t, err)
			require.Len(t, groups, 1)
			require.Equal(t, product, groups[0].InstrumentType)
			require.Equal(t, product+"_test", groups[0].SourceID)
			env, err := BuildManagedEnvironment(NodeAssignment{Provider: "binance", MarketType: product, MarketID: groups[0].MarketID, InstrumentType: groups[0].InstrumentType, SourceID: groups[0].SourceID, DatasetID: "bars", Frequency: "1h", Subjects: groups[0].Subjects, ExternalSymbols: groups[0].ExternalSymbols}, nil)
			require.NoError(t, err)
			require.Equal(t, product, env["MOOX_MARKET_FETCH_INSTRUMENT_TYPE"])
			require.Equal(t, product+"_test", env["MOOX_MARKET_FETCH_SOURCE_ID"])
		})
	}
}

func TestReconcilerTreatsRuntimeSubmitTimeoutAsRetryPending(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{
		nodes:     []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}},
		submitErr: context.DeadlineExceeded,
	}
	metrics := NewMetrics(prometheus.NewRegistry())
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
		Metrics: metrics,
	}

	require.ErrorIs(t, reconciler.Reconcile(context.Background(), "crypto"), context.DeadlineExceeded)
	_, firstPendingSince := reconciler.pendingRuntimeJobState()
	require.False(t, firstPendingSince.IsZero())
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentActive.WithLabelValues("crypto", "1m")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "submit_timeout")))

	time.Sleep(time.Millisecond)
	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"), "do not resubmit an outcome-unknown request under the same fence")
	_, secondPendingSince := reconciler.pendingRuntimeJobState()
	require.Equal(t, firstPendingSince, secondPendingSince, "retries must preserve the original pending time")
	require.Equal(t, 1, nodes.submits, "an unknown submission cannot be retried until a higher fencing token is acquired")
}

func (s *reconcilerNodesStub) GetRuntimeConfigBatchStatus(_ context.Context, _ string, jobID string) (*cloudnodepb.NodeBatchSummary, error) {
	s.batchQueries = append(s.batchQueries, jobID)
	if err := s.batchErrors[jobID]; err != nil {
		return nil, err
	}
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	if status, ok := s.batchStatuses[jobID]; ok {
		return &cloudnodepb.NodeBatchSummary{Status: status}, nil
	}
	return &cloudnodepb.NodeBatchSummary{Status: cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS}, nil
}

func TestReconcilerRetainsFailedBatchUntilOtherJobsAreTerminal(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentSuccess("crypto", 123)
	nodes := &reconcilerNodesStub{batchStatuses: map[string]cloudnodepb.NodeBatchStatus{
		"job-1": cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED,
		"job-2": cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING,
	}}
	r := &Reconciler{Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes, Metrics: metrics}
	_, leaseErr := r.ensurePublishLease(context.Background(), "crypto")
	require.NoError(t, leaseErr)
	r.setPendingRuntimeJobs([]string{"job-1", "job-2"}, nil, true)
	_, since := r.pendingRuntimeJobsState()

	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, []string{"job-1", "job-2"}, nodes.batchQueries)
	jobs, after := r.pendingRuntimeJobsState()
	require.Equal(t, []string{"job-1", "job-2"}, jobs)
	require.Equal(t, since, after)
	require.Zero(t, nodes.leaseReleases)
	require.NotNil(t, r.publishLease)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))

	nodes.batchStatuses["job-2"] = cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL
	err := r.Reconcile(context.Background(), "crypto")
	require.ErrorContains(t, err, "job-1 failed")
	require.ErrorContains(t, err, "job-2 partially failed")
	jobs, _ = r.pendingRuntimeJobsState()
	require.Empty(t, jobs)
	require.Equal(t, 1, nodes.leaseReleases)
	require.Nil(t, r.publishLease)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(123), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
}

func TestReconcilerChecksAllPendingJobsBeforeReturning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status cloudnodepb.NodeBatchStatus
		err    error
	}{
		{name: "pending", status: cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING},
		{name: "running", status: cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING},
		{name: "unknown", status: cloudnodepb.NodeBatchStatus(99)},
		{name: "unreadable", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := &reconcilerNodesStub{
				batchStatuses: map[string]cloudnodepb.NodeBatchStatus{"job-1": tc.status, "job-2": cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED},
				batchErrors:   map[string]error{"job-1": tc.err},
			}
			r := &Reconciler{Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes}
			_, leaseErr := r.ensurePublishLease(context.Background(), "crypto")
			require.NoError(t, leaseErr)
			r.setPendingRuntimeJobs([]string{"job-1", "job-2"}, nil, true)
			err := r.Reconcile(context.Background(), "crypto")
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			} else if tc.name == "unknown" {
				require.ErrorContains(t, err, "unknown status")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, []string{"job-1", "job-2"}, nodes.batchQueries)
			jobs, _ := r.pendingRuntimeJobsState()
			require.Equal(t, []string{"job-1", "job-2"}, jobs)
			require.Zero(t, nodes.leaseReleases)
			require.NotNil(t, r.publishLease)
		})
	}
}

func TestReconcilerRecordsCompletedBatchBeforeNextDNSChange(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	routes := map[string]sources.DNSResolution{"api.binance.com": {IPs: []string{"203.0.113.1"}}}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{{SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
			CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes, DNS: reconcilerDNSStub{routes: routes}, Metrics: metrics,
	}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), "submission is not completion")
	firstAssignment := nodes.patches[0].GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
	routes["api.binance.com"] = sources.DNSResolution{IPs: []string{"203.0.113.2"}}
	before := time.Now().UTC().Unix()
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 2, nodes.submits)
	require.Equal(t, firstAssignment, nodes.patches[0].GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	require.GreaterOrEqual(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), float64(before))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Positive(t, testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
}

func TestReconcilerDoesNotRecordIncompleteBatchAsSuccess(t *testing.T) {
	for _, status := range []cloudnodepb.NodeBatchStatus{
		cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING,
		cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING,
		cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED,
		cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL,
		cloudnodepb.NodeBatchStatus(99),
	} {
		t.Run(status.String(), func(t *testing.T) {
			metrics := NewMetrics(prometheus.NewRegistry())
			metrics.ObserveAssignmentSuccess("crypto", 123)
			nodes := &reconcilerNodesStub{batchStatuses: map[string]cloudnodepb.NodeBatchStatus{"job-2": status}}
			r := &Reconciler{Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes, Metrics: metrics}
			r.setPendingRuntimeJobs([]string{"job-1", "job-2"}, nil, true)
			_, since := r.pendingRuntimeJobsState()
			err := r.Reconcile(context.Background(), "crypto")
			if status == cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING || status == cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_RUNNING {
				require.NoError(t, err)
				_, after := r.pendingRuntimeJobsState()
				require.Equal(t, since, after)
				require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
			} else {
				require.Error(t, err)
			}
			require.Equal(t, float64(123), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
		})
	}
}

func TestReconcilerDoesNotRecordUnreadableBatchAsSuccess(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentSuccess("crypto", 123)
	nodes := &reconcilerNodesStub{batchErr: context.DeadlineExceeded}
	r := &Reconciler{Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes, Metrics: metrics}
	r.setPendingRuntimeJobs([]string{"job-1"}, nil, true)
	_, since := r.pendingRuntimeJobsState()
	require.ErrorIs(t, r.Reconcile(context.Background(), "crypto"), context.DeadlineExceeded)
	jobs, after := r.pendingRuntimeJobsState()
	require.Equal(t, []string{"job-1"}, jobs)
	require.Equal(t, since, after)
	require.Equal(t, float64(123), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
}

func TestReconcilerDoesNotRecordPartiallySubmittedPlanAsSuccess(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentSuccess("crypto", 123)
	r := &Reconciler{Tasks: reconcilerTasksStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: &reconcilerNodesStub{}, Metrics: metrics}
	r.setPendingRuntimeJobs([]string{"accepted-chunk"}, nil, false)
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, float64(123), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
}

type reconcilerDNSStub struct {
	routes map[string]sources.DNSResolution
}

func (s reconcilerDNSStub) Snapshot() map[string]sources.DNSResolution { return s.routes }

func TestReconcilerCopiesOneDNSSnapshotToPerNodeAssignmentsAndAvoidsNoop(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-2", FunctionName: "market-fetch-shanghai", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "market-fetch-guangzhou", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{
			dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"},
			subjects: []domain.DatasetSubject{
				{SubjectID: "ETH-USDT", Status: "active"},
				{SubjectID: "BTC-USDT", Status: "active"},
			},
		},
		Nodes: nodes,
		DNS: reconcilerDNSStub{routes: map[string]sources.DNSResolution{
			"api.binance.com": {IPs: []string{"203.0.113.2", "203.0.113.1"}, ResolvedAt: time.Date(2026, 8, 4, 1, 2, 0, 0, time.UTC)},
		}},
		MaxSubjects: 30,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits)
	require.Len(t, nodes.patches, 2)
	firstDNS := nodes.patches[0].GetManagedEnvironment()["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"]
	for _, patch := range nodes.patches {
		env := patch.GetManagedEnvironment()
		require.Equal(t, firstDNS, env["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"])
		require.NotContains(t, env, "MOOX_MARKET_FETCH_SYMBOLS_JSON")
		require.NotEmpty(t, env["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits, "unchanged assignment and DNS must not call CloudNode again")
}

func TestReconcilerExposesOnlyRuntimeObservedTimerAssignments(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "market-fetch", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"}}}
	r := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes, MaxSubjects: 30,
	}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Empty(t, r.TimerAssignments(), "submitted but not observed assignments are not claimable")
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	assignments := r.TimerAssignments()
	require.Len(t, assignments, 1)
	require.True(t, assignments[0].Enabled)
	require.Equal(t, "timer-1", assignments[0].NodeID)
	assignments[0].Subjects[0] = "MUTATED"
	require.Equal(t, []string{"BTC-USDT"}, r.TimerAssignments()[0].Subjects, "callers receive a defensive copy")
}

func TestReconcilerPublishesStockCNRouteIdentityToEveryTimer(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-2", FunctionName: "moox-stockcn-ap-shanghai-000", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "moox-stockcn-ap-guangzhou-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{
			{SubjectID: "600000.XSHG", Status: "active"},
			{SubjectID: "000001.XSHE", Status: "active"},
		}},
		Nodes:                         nodes,
		CollectorRuntimeGatewayTarget: "ip://collector-runtime:11003",
		CollectorRuntimeGatewayNodeID: "collector-runtime-node",
		DNS: reconcilerDNSStub{routes: map[string]sources.DNSResolution{
			"api.binance.com": {IPs: []string{"203.0.113.2", "203.0.113.1"}, ResolvedAt: time.Date(2026, 8, 29, 1, 2, 0, 0, time.UTC)},
		}},
		ExpectedStockCNTimerFunctions: 2,
		MeasuredSafeGroupSize:         30,
		Now:                           func() time.Time { return time.Date(2026, 8, 29, 4, 0, 0, 0, time.UTC) },
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	require.Len(t, nodes.patches, 2)
	groups := make(map[string]struct{}, 2)
	for _, patch := range nodes.patches {
		require.True(t, patch.GetTimerEnabled())
		env := patch.GetManagedEnvironment()
		require.Equal(t, StockCNRouteID, env["MOOX_MARKET_FETCH_ROUTE_VERSION"])
		require.NotEmpty(t, env["MOOX_MARKET_FETCH_PROVIDER_CHAIN"])
		require.NotEmpty(t, env["MOOX_MARKET_FETCH_GROUP_ID"])
		require.Equal(t, "ip://collector-runtime:11003", env["MOOX_COLLECTOR_RPC_GATEWAY_TARGET"])
		require.Equal(t, "collector-runtime-node", env["MOOX_COLLECTOR_GATEWAY_TARGET_NODE"])
		require.NotEmpty(t, env["MOOX_MARKET_FETCH_BINDING_HASH"])
		_, hasDNSRoutes := env["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"]
		_, hasDNSHash := env["MOOX_MARKET_FETCH_DNS_HASH"]
		require.False(t, hasDNSRoutes, "stock assignments must not inherit unrelated Binance DNS snapshots")
		require.False(t, hasDNSHash, "stock assignments must not inherit unrelated Binance DNS hashes")
		groups[env["MOOX_MARKET_FETCH_GROUP_ID"]] = struct{}{}
	}
	require.Len(t, groups, 2)
}

func TestReconcilerSkipsMalformedActiveStockSubjectWithoutBlockingValidSubjects(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{
			{SubjectID: "600000.XSHG", Status: "active"},
			{SubjectID: "BAD", Status: "active"},
		}},
		Nodes: nodes, ExpectedStockCNTimerFunctions: 1, MeasuredSafeGroupSize: 30,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	require.Len(t, nodes.patches, 1)
	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	require.Equal(t, []string{"600000.XSHG"}, reconciler.lastAssignments[0].Subjects)
	require.NotContains(t, nodes.patches[0].GetManagedEnvironment(), "MOOX_MARKET_FETCH_SUBJECTS")
}

func TestReconcilerFailsClosedWhenAllActiveStockSubjectsAreMalformed(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{
			{SubjectID: "BAD-1", Status: "active"},
			{SubjectID: "BAD-2", Status: "active"},
		}},
		Nodes: nodes, ExpectedStockCNTimerFunctions: 1, MeasuredSafeGroupSize: 30,
	}

	require.ErrorContains(t, reconciler.Reconcile(context.Background(), StockCNSpaceID), "all active equity subjects are invalid")
	require.Zero(t, nodes.submits)
}

func TestReconcilerFailsClosedWhenSymbolCatalogIsEmpty(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-fetcher-crypto-0", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}}, Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}}, Nodes: nodes}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Zero(t, nodes.submits, "an empty symbol catalog must not disable the existing Timer fleet")
}

func TestReconcilerSkipsUnavailableTaskWithoutBlockingHealthyGroups(t *testing.T) {
	badTask := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "swap-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"swap","subject_tags":["binance_swap"],"target_dataset_id":"dataset_binance_swap_kline","frequency":"1h"}`,
	}
	goodTask := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "spot-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"dataset_binance_kline_1m","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-fetcher-crypto-0", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{badTask, goodTask}},
		Symbols: reconcilerSymbolsStub{
			getErrs:  map[string]error{"dataset_binance_swap_kline": fmt.Errorf("metadata unavailable")},
			subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}},
		},
		Nodes: nodes,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits)
	require.Len(t, nodes.patches, 1)
	require.Equal(t, "dataset_binance_kline_1m", nodes.patches[0].GetManagedEnvironment()["MOOX_MARKET_FETCH_DATASET_ID"])
}

func TestReconcilerFailsClosedWhenStockRequiredGroupSizeExceedsMeasuredSafeSize(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	subjects := make([]domain.DatasetSubject, 0, 4)
	for index := 0; index < 4; index++ {
		subjects = append(subjects, domain.DatasetSubject{SubjectID: fmt.Sprintf("%06d.XSHG", 600000+index), Status: "active"})
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "moox-stockcn-001", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-2", FunctionName: "moox-stockcn-002", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: subjects},
		Nodes:   nodes, ExpectedStockCNTimerFunctions: 3, MeasuredSafeGroupSize: 1,
	}

	require.ErrorContains(t, reconciler.Reconcile(context.Background(), StockCNSpaceID), "required_group_size 2 exceeds measured_safe_group_size 1")
	require.Zero(t, nodes.submits)
}

func TestReconcilerAllowsStockGroupAboveThirtyWhenMeasuredSafeSizeAllowsIt(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	subjects := make([]domain.DatasetSubject, 0, 40)
	for index := 0; index < 40; index++ {
		subjects = append(subjects, domain.DatasetSubject{SubjectID: fmt.Sprintf("%06d.XSHG", 600000+index), Status: "active"})
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "moox-stockcn-001", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-2", FunctionName: "moox-stockcn-002", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: subjects},
		Nodes:   nodes, ExpectedStockCNTimerFunctions: 3, MeasuredSafeGroupSize: 40,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	require.Len(t, nodes.patches, 3)
	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	seenSubjects := 0
	for _, assignment := range reconciler.lastAssignments {
		seenSubjects += len(assignment.Subjects)
	}
	require.Equal(t, 40, seenSubjects)
}

func TestReconcilerRejectsStockGroupSizeAboveRealtimeLimit(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "600000.XSHG", Status: "active"}}},
		Nodes:   nodes, ExpectedStockCNTimerFunctions: 1, MeasuredSafeGroupSize: MaxRealtimeItems + 1,
	}

	require.ErrorContains(t, reconciler.Reconcile(context.Background(), StockCNSpaceID), "measured_safe_group_size must be between 1 and 40")
	require.Zero(t, nodes.submits)
}

func TestReconcilerFailsWithoutTimerCapacityBeforeSubmitting(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{}
	metrics := NewMetrics(prometheus.NewRegistry())
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
		Metrics: metrics,
	}
	require.ErrorContains(t, reconciler.Reconcile(context.Background(), "crypto"), "capacity")
	require.Zero(t, nodes.submits)
	// Capacity failure must publish required work before returning.
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentRequired.WithLabelValues("crypto", "1m")))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.assignmentActive.WithLabelValues("crypto", "1m")))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.timerCapacityTotal.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.timerCapacityRequired.WithLabelValues("crypto")))
	require.Equal(t, float64(-1), testutil.ToFloat64(metrics.timerCapacityHeadroom.WithLabelValues("crypto")))
}

func TestReconcilerDoesNotEraseDNSWhenRefreshHasNoSnapshot(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"dns_hash": "old-dns"}}}}
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   nodes,
		DNS:     reconcilerDNSStub{routes: nil},
	}
	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Len(t, nodes.patches, 1)
	env := nodes.patches[0].GetManagedEnvironment()
	_, hasRoutes := env["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"]
	_, hasHash := env["MOOX_MARKET_FETCH_DNS_HASH"]
	require.False(t, hasRoutes)
	require.False(t, hasHash)
}

func TestReconcilerSkipsOverlappingTicks(t *testing.T) {
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}, listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	reconciler := &Reconciler{CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node", Tasks: reconcilerTasksStub{tasks: []domain.CollectionTask{task}}, Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}}, Nodes: nodes}
	first := make(chan error, 1)
	go func() { first <- reconciler.Reconcile(context.Background(), "crypto") }()
	<-nodes.listStarted
	second := make(chan error, 1)
	go func() { second <- reconciler.Reconcile(context.Background(), "crypto") }()
	require.ErrorContains(t, <-second, "already running")
	close(nodes.listRelease)
	require.NoError(t, <-first)
	require.Equal(t, 1, nodes.submits, "overlapping schedule ticks must not submit two snapshots")
}

func TestReconcilerDetectsUnexpectedOpenDisabledTimer(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	(&Reconciler{Metrics: metrics}).observeTimerStates("crypto", []scfinvoker.Node{{
		NodeID: "timer-id", Metadata: map[string]any{
			"timer_enabled": false, "timer_available_status": "Available", "timer_actual_type": "timer",
			"timer_actual_enabled": true, "timer_actual_cron": "0 * * * * * *", "timer_cron": "0 * * * * * *",
			"timer_actual_qualifier": "$LATEST", "timer_actual_message": "market_fetch_timer_v1",
		},
	}})
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.timerAvailable.WithLabelValues("crypto", "timer-id", "false")))
}

func TestReconcilerTreatsUnknownTimerReadbackAsUnknown(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	(&Reconciler{Metrics: metrics}).observeTimerStates("crypto", []scfinvoker.Node{{
		NodeID: "timer-id", Metadata: map[string]any{
			"timer_enabled": true, "timer_available_status": "Unknown", "timer_status_error": "RequestLimitExceeded",
		},
	}})
	require.Equal(t, float64(-1), testutil.ToFloat64(metrics.timerAvailable.WithLabelValues("crypto", "timer-id", "true")))
}

func TestReconcilerRejectsExhaustedRemoteEnvironmentBudget(t *testing.T) {
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`}
	metrics := NewMetrics(prometheus.NewRegistry())
	reconciler := &Reconciler{
		CollectorRuntimeGatewayTarget: "ip://collector.local:11002", CollectorRuntimeGatewayNodeID: "collector-node",
		Tasks:   reconcilerTasksStub{tasks: []domain.CollectionTask{task}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
		Nodes:   &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"managed_environment_budget_bytes": 0}}}},
		Metrics: metrics,
	}
	require.ErrorContains(t, reconciler.Reconcile(context.Background(), "crypto"), "no available timer environment budget")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentRequired.WithLabelValues("crypto", "1m")))
}

func TestTimerTriggerNeedsRepairWhenSchedulerOwnsAcquisition(t *testing.T) {
	assignment := NodeAssignment{NodeID: "timer-1", Enabled: true, Cron: "0 * * * * * *"}
	metadata := map[string]any{
		"timer_actual_enabled": true, "timer_available_status": "Available",
		"timer_actual_type": timerTriggerType, "timer_actual_qualifier": timerTriggerQualifier,
		"timer_actual_message": timerTriggerMessage, "timer_actual_cron": assignment.Cron,
	}
	require.True(t, timerTriggerNeedsRepair(assignment, metadata, false), "scheduler-owned spaces must disable the Tencent timer trigger")
	metadata["timer_actual_enabled"] = false
	require.False(t, timerTriggerNeedsRepair(assignment, metadata, false))
}

func TestReconcilerRepairsTriggerProtocolDrift(t *testing.T) {
	assignment := NodeAssignment{Enabled: true, Cron: "0 * * * * * *"}
	metadata := map[string]any{"timer_actual_type": "timer", "timer_actual_enabled": true, "timer_actual_cron": assignment.Cron, "timer_actual_qualifier": "$LATEST", "timer_actual_message": "wrong", "timer_available_status": "Available"}
	require.True(t, timerTriggerNeedsRepair(assignment, metadata))
	metadata["timer_actual_message"] = "market_fetch_timer_v1"
	require.False(t, timerTriggerNeedsRepair(assignment, metadata))
	metadata["timer_actual_enabled"] = true
	assignment.Enabled = false
	require.True(t, timerTriggerNeedsRepair(assignment, metadata))
}

func TestReconcilerDoesNotStormOnTransientTimerReadbackError(t *testing.T) {
	assignment := NodeAssignment{Enabled: true, Cron: "0 * * * * * *"}
	metadata := map[string]any{"timer_available_status": "Unknown", "timer_status_error": "RequestLimitExceeded"}
	require.False(t, timerTriggerNeedsRepair(assignment, metadata))
	metadata["timer_status_error"] = nil
	require.True(t, timerTriggerNeedsRepair(assignment, metadata))
}

func TestReconcilerIgnoresDNSRotationForDisabledAssignments(t *testing.T) {
	assignment := NodeAssignment{NodeID: "timer-1", Enabled: false, AssignmentHash: AssignmentHash()}
	fingerprint := assignment.AssignmentHash + "\x00\x00false\x000 * * * * * *"
	nodes := []scfinvoker.Node{{NodeID: assignment.NodeID, Metadata: map[string]any{
		"assignment_hash":        assignment.AssignmentHash,
		"dns_hash":               "rotated-dns-hash",
		"timer_enabled":          false,
		"timer_cron":             "0 * * * * * *",
		"timer_available_status": "Available",
		"timer_actual_type":      "timer",
		"timer_actual_enabled":   false,
		"timer_actual_cron":      "0 * * * * * *",
		"timer_actual_qualifier": "$LATEST",
		"timer_actual_message":   timerTriggerMessage,
	}}}
	require.False(t, (&Reconciler{}).shouldPatch(assignment, nodes, fingerprint), "disabled nodes must not be repatched when only DNS rotates")
}

func TestReconcilerLongSubjectsDoNotConsumeRuntimeEnvironment(t *testing.T) {
	group := TaskGroup{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", ExternalSymbols: map[string]string{}}
	for index := 0; index < 30; index++ {
		subject := fmt.Sprintf("%s%d-USDT", strings.Repeat("A", 25), index)
		group.Subjects = append(group.Subjects, subject)
		group.ExternalSymbols[subject] = strings.TrimSuffix(subject, "-USDT") + "USDT"
	}
	groups, err := splitGroupsForEnvironment([]TaskGroup{group}, nil, 30, nil)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	for _, split := range groups {
		_, err := BuildManagedEnvironment(NodeAssignment{
			Provider: split.Provider, MarketType: split.MarketType, DatasetID: split.DatasetID,
			Frequency: split.Frequency, Subjects: split.Subjects, ExternalSymbols: split.ExternalSymbols, GroupID: 999999, GroupCount: 999999, Enabled: true,
		}, nil)
		require.NoError(t, err)
	}
}

func (r *Reconciler) pendingRuntimeJobState() (string, time.Time) {
	jobs, since := r.pendingRuntimeJobsState()
	if len(jobs) == 0 {
		return "", since
	}
	return jobs[0], since
}
