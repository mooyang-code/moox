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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type reconcilerRulesStub struct{ rules []domain.TaskRule }

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
				SCFRegionBlacklists: map[string][]string{"crypto": {"ap-guangzhou"}},
				Rules:               reconcilerRulesStub{rules: []domain.TaskRule{{SpaceID: "crypto", RuleID: "bars", DataType: "kline", Enabled: true, CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1h"}`}}},
				Symbols:             reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}}, Nodes: nodes,
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
	r := &Reconciler{SCFRegionBlacklists: map[string][]string{"crypto": {"ap-guangzhou"}}, Rules: reconcilerRulesStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: reconcilerInstrumentNodesStub{nodes}}
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Len(t, nodes.patches, 1)
	require.Equal(t, "blocked-instrument", nodes.patches[0].GetNodeId())
	require.False(t, nodes.patches[0].GetTimerEnabled())
	require.Equal(t, "0 0 8 * * * *", nodes.patches[0].GetTimerCron())
	require.Nil(t, nodes.patches[0].GetManagedEnvironment())
}

func (s reconcilerRulesStub) ListEnabled(context.Context, string) ([]domain.TaskRule, error) {
	return append([]domain.TaskRule(nil), s.rules...), nil
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

type reconcilerNodesStub struct {
	nodes         []scfinvoker.Node
	patches       []*cloudnodepb.NodeRuntimeConfigPatch
	submits       int
	listStarted   chan struct{}
	listRelease   chan struct{}
	listOnce      sync.Once
	submitErr     error
	batchStatuses map[string]cloudnodepb.NodeBatchStatus
	batchErr      error
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
	return "job-1", nil
}

func TestReconcilerResolvesMissingInstrumentIdentity(t *testing.T) {
	for _, product := range []string{"spot", "swap"} {
		t.Run(product, func(t *testing.T) {
			r := &Reconciler{
				ResolveSourceID: func(provider, instrument string) string { return instrument + "_test" },
				Rules:           reconcilerRulesStub{rules: []domain.TaskRule{{SpaceID: "crypto", Provider: "binance", MarketType: product, DataType: "kline", CollectParams: `{"symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1H"}`}}},
				Symbols:         reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbols"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
			}
			groups, err := r.groups(context.Background(), "crypto")
			require.NoError(t, err)
			require.Len(t, groups, 1)
			require.Equal(t, product, groups[0].InstrumentType)
			require.Equal(t, product+"_test", groups[0].SourceID)
			env, err := BuildManagedEnvironment(NodeAssignment{Provider: "binance", MarketType: product, MarketID: groups[0].MarketID, InstrumentType: groups[0].InstrumentType, SourceID: groups[0].SourceID, DatasetID: "bars", Frequency: "1H", Subjects: groups[0].Subjects, ExternalSymbols: groups[0].ExternalSymbols}, nil)
			require.NoError(t, err)
			require.Equal(t, product, env["MOOX_MARKET_FETCH_INSTRUMENT_TYPE"])
			require.Equal(t, product+"_test", env["MOOX_MARKET_FETCH_SOURCE_ID"])
		})
	}
}

func TestReconcilerTreatsRuntimeSubmitTimeoutAsRetryPending(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{
		nodes:     []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}},
		submitErr: context.DeadlineExceeded,
	}
	metrics := NewMetrics(prometheus.NewRegistry())
	reconciler := &Reconciler{
		Rules:   reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
		Nodes:   nodes,
		Metrics: metrics,
	}

	require.ErrorIs(t, reconciler.Reconcile(context.Background(), "crypto"), context.DeadlineExceeded)
	_, firstPendingSince := reconciler.pendingRuntimeJobState()
	require.False(t, firstPendingSince.IsZero())
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentActive.WithLabelValues("crypto", "bars", "1m")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "submit_timeout")))

	time.Sleep(time.Millisecond)
	require.ErrorIs(t, reconciler.Reconcile(context.Background(), "crypto"), context.DeadlineExceeded)
	_, secondPendingSince := reconciler.pendingRuntimeJobState()
	require.Equal(t, firstPendingSince, secondPendingSince, "retries must preserve the original pending time")
}

func (s *reconcilerNodesStub) GetRuntimeConfigBatchStatus(_ context.Context, _ string, jobID string) (*cloudnodepb.NodeBatchSummary, error) {
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	if status, ok := s.batchStatuses[jobID]; ok {
		return &cloudnodepb.NodeBatchSummary{Status: status}, nil
	}
	return &cloudnodepb.NodeBatchSummary{Status: cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS}, nil
}

func TestReconcilerRecordsCompletedBatchBeforeNextDNSChange(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	routes := map[string]sources.DNSResolution{"api.binance.com": {IPs: []string{"203.0.113.1"}}}
	r := &Reconciler{
		Rules: reconcilerRulesStub{rules: []domain.TaskRule{{SpaceID: "crypto", RuleID: "bars", DataType: "kline", Enabled: true,
			CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`}}},
		Symbols: reconcilerSymbolsStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
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
			r := &Reconciler{Rules: reconcilerRulesStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes, Metrics: metrics}
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
	r := &Reconciler{Rules: reconcilerRulesStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: nodes, Metrics: metrics}
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
	r := &Reconciler{Rules: reconcilerRulesStub{}, Symbols: reconcilerSymbolsStub{}, Nodes: &reconcilerNodesStub{}, Metrics: metrics}
	r.setPendingRuntimeJobs([]string{"accepted-chunk"}, nil, false)
	require.NoError(t, r.Reconcile(context.Background(), "crypto"))
	require.Equal(t, float64(123), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
}

type reconcilerDNSStub struct {
	routes map[string]sources.DNSResolution
}

func (s reconcilerDNSStub) Snapshot() map[string]sources.DNSResolution { return s.routes }

func TestReconcilerCopiesOneDNSSnapshotToPerNodeAssignmentsAndAvoidsNoop(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-2", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		Rules: reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{
			dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"},
			subjects: []domain.DatasetSubject{
				{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT", Status: "active"},
				{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
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
		require.NotEmpty(t, env["MOOX_MARKET_FETCH_SYMBOLS_JSON"])
		require.NotEmpty(t, env["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits, "unchanged assignment and DNS must not call CloudNode again")
}

func TestReconcilerPublishesStockCNRouteIdentityToEveryTimer(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: StockCNSpaceID, RuleID: "stock-bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-2", FunctionName: "moox-stockcn-ap-shanghai-000", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "moox-stockcn-ap-guangzhou-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		Rules: reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{
			{SubjectID: "600000.XSHG", ExternalSymbol: "sh600000", Status: "active"},
			{SubjectID: "000001.XSHE", ExternalSymbol: "sz000001", Status: "active"},
		}},
		Nodes: nodes,
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
		_, hasDNSRoutes := env["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"]
		_, hasDNSHash := env["MOOX_MARKET_FETCH_DNS_HASH"]
		require.False(t, hasDNSRoutes, "stock assignments must not inherit unrelated Binance DNS snapshots")
		require.False(t, hasDNSHash, "stock assignments must not inherit unrelated Binance DNS hashes")
		groups[env["MOOX_MARKET_FETCH_GROUP_ID"]] = struct{}{}
	}
	require.Len(t, groups, 2)
}

func TestReconcilerSkipsMalformedActiveStockSubjectWithoutBlockingValidSubjects(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: StockCNSpaceID, RuleID: "stock-bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		Rules: reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{
			{SubjectID: "600000.XSHG", ExternalSymbol: "sh600000", Status: "active"},
			{SubjectID: "BAD", ExternalSymbol: "", Status: "active"},
		}},
		Nodes: nodes, ExpectedStockCNTimerFunctions: 1, MeasuredSafeGroupSize: 30,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	require.Len(t, nodes.patches, 1)
	require.Equal(t, "600000.XSHG", nodes.patches[0].GetManagedEnvironment()["MOOX_MARKET_FETCH_SUBJECTS"])
}

func TestReconcilerFailsClosedWhenAllActiveStockSubjectsAreMalformed(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: StockCNSpaceID, RuleID: "stock-bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		Rules: reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{
			{SubjectID: "BAD-1", ExternalSymbol: "", Status: "active"},
			{SubjectID: "BAD-2", ExternalSymbol: "", Status: "active"},
		}},
		Nodes: nodes, ExpectedStockCNTimerFunctions: 1, MeasuredSafeGroupSize: 30,
	}

	require.ErrorContains(t, reconciler.Reconcile(context.Background(), StockCNSpaceID), "all active equity subjects are invalid")
	require.Zero(t, nodes.submits)
}

func TestReconcilerFailsClosedWhenSymbolCatalogIsEmpty(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-fetcher-crypto-0", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{Rules: reconcilerRulesStub{rules: []domain.TaskRule{rule}}, Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}}, Nodes: nodes}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Zero(t, nodes.submits, "an empty symbol catalog must not disable the existing Timer fleet")
}

func TestReconcilerSkipsUnavailableRuleWithoutBlockingHealthyGroups(t *testing.T) {
	badRule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "swap-bars", DataType: "kline", Provider: "binance", MarketType: "swap", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"swap","symbol_source":"dataset","symbol_dataset_id":"missing-swap-symbols","target_dataset_id":"dataset_binance_swap_kline","frequency":"1h"}`,
	}
	goodRule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "spot-bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"spot-symbols","target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-0", FunctionName: "moox-fetcher-crypto-0", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}}
	reconciler := &Reconciler{
		Rules: reconcilerRulesStub{rules: []domain.TaskRule{badRule, goodRule}},
		Symbols: reconcilerSymbolsStub{
			datasets: map[string]storagesource.DatasetInfo{"spot-symbols": {DataSourceID: "spot-source"}},
			getErrs:  map[string]error{"missing-swap-symbols": fmt.Errorf("metadata unavailable")},
			subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}},
		},
		Nodes: nodes,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), "crypto"))
	require.Equal(t, 1, nodes.submits)
	require.Len(t, nodes.patches, 1)
	require.Equal(t, "dataset_binance_spot_kline_1m", nodes.patches[0].GetManagedEnvironment()["MOOX_MARKET_FETCH_DATASET_ID"])
}

func TestReconcilerFailsClosedWhenStockRequiredGroupSizeExceedsMeasuredSafeSize(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: StockCNSpaceID, RuleID: "stock-bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	subjects := make([]domain.DatasetSubject, 0, 4)
	for index := 0; index < 4; index++ {
		subjects = append(subjects, domain.DatasetSubject{SubjectID: fmt.Sprintf("%06d.XSHG", 600000+index), ExternalSymbol: fmt.Sprintf("sh%06d", 600000+index), Status: "active"})
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "moox-stockcn-001", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-2", FunctionName: "moox-stockcn-002", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		Rules:   reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: subjects},
		Nodes:   nodes, ExpectedStockCNTimerFunctions: 3, MeasuredSafeGroupSize: 1,
	}

	require.ErrorContains(t, reconciler.Reconcile(context.Background(), StockCNSpaceID), "required_group_size 2 exceeds measured_safe_group_size 1")
	require.Zero(t, nodes.submits)
}

func TestReconcilerAllowsStockGroupAboveThirtyWhenMeasuredSafeSizeAllowsIt(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: StockCNSpaceID, RuleID: "stock-bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	subjects := make([]domain.DatasetSubject, 0, 40)
	for index := 0; index < 40; index++ {
		subjects = append(subjects, domain.DatasetSubject{SubjectID: fmt.Sprintf("%06d.XSHG", 600000+index), ExternalSymbol: fmt.Sprintf("sh%06d", 600000+index), Status: "active"})
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{
		{NodeID: "timer-0", FunctionName: "moox-stockcn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-1", FunctionName: "moox-stockcn-001", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "timer-2", FunctionName: "moox-stockcn-002", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
	}}
	reconciler := &Reconciler{
		Rules:   reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: subjects},
		Nodes:   nodes, ExpectedStockCNTimerFunctions: 3, MeasuredSafeGroupSize: 40,
	}

	require.NoError(t, reconciler.Reconcile(context.Background(), StockCNSpaceID))
	require.Len(t, nodes.patches, 3)
	seenSubjects := 0
	for _, patch := range nodes.patches {
		seenSubjects += strings.Count(patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_SUBJECTS"], "|") + 1
	}
	require.Equal(t, 40, seenSubjects)
}

func TestReconcilerFailsWithoutTimerCapacityBeforeSubmitting(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{}
	metrics := NewMetrics(prometheus.NewRegistry())
	reconciler := &Reconciler{
		Rules:   reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
		Nodes:   nodes,
		Metrics: metrics,
	}
	require.ErrorContains(t, reconciler.Reconcile(context.Background(), "crypto"), "capacity")
	require.Zero(t, nodes.submits)
	// Capacity failure must publish required work before returning.
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentRequired.WithLabelValues("crypto", "bars", "1m")))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.assignmentActive.WithLabelValues("crypto", "bars", "1m")))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.timerCapacityTotal.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.timerCapacityRequired.WithLabelValues("crypto")))
	require.Equal(t, float64(-1), testutil.ToFloat64(metrics.timerCapacityHeadroom.WithLabelValues("crypto")))
}

func TestReconcilerDoesNotEraseDNSWhenRefreshHasNoSnapshot(t *testing.T) {
	rule := domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"dns_hash": "old-dns"}}}}
	reconciler := &Reconciler{
		Rules:   reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
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
	rule := domain.TaskRule{SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`}
	nodes := &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}, listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	reconciler := &Reconciler{Rules: reconcilerRulesStub{rules: []domain.TaskRule{rule}}, Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}}, Nodes: nodes}
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
	rule := domain.TaskRule{SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot", Enabled: true,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`}
	metrics := NewMetrics(prometheus.NewRegistry())
	reconciler := &Reconciler{
		Rules:   reconcilerRulesStub{rules: []domain.TaskRule{rule}},
		Symbols: reconcilerSymbolsStub{dataset: storagesource.DatasetInfo{DataSourceID: "symbol-source"}, subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
		Nodes:   &reconcilerNodesStub{nodes: []scfinvoker.Node{{NodeID: "timer-1", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"managed_environment_budget_bytes": 0}}}},
		Metrics: metrics,
	}
	require.ErrorContains(t, reconciler.Reconcile(context.Background(), "crypto"), "no available timer environment budget")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentRequired.WithLabelValues("crypto", "bars", "1m")))
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

func TestReconcilerSplitsLongSubjectsBeforeEnvironmentFailure(t *testing.T) {
	group := TaskGroup{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", ExternalSymbols: map[string]string{}}
	for index := 0; index < 30; index++ {
		subject := fmt.Sprintf("%s%d-USDT", strings.Repeat("A", 25), index)
		group.Subjects = append(group.Subjects, subject)
		group.ExternalSymbols[subject] = strings.TrimSuffix(subject, "-USDT") + "USDT"
	}
	groups, err := splitGroupsForEnvironment([]TaskGroup{group}, nil, 30, nil)
	require.NoError(t, err)
	require.Greater(t, len(groups), 1)
	for _, split := range groups {
		_, err := BuildManagedEnvironment(NodeAssignment{
			Provider: split.Provider, MarketType: split.MarketType, DatasetID: split.DatasetID,
			Frequency: split.Frequency, Subjects: split.Subjects, ExternalSymbols: split.ExternalSymbols, GroupID: 999999, GroupCount: 999999, Enabled: true,
		}, nil)
		require.NoError(t, err)
	}
}
