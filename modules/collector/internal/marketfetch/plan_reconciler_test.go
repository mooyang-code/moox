package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

type memPlanStore struct {
	mu      sync.Mutex
	records []*store.FetchPlanRecord
}

func (s *memPlanStore) Open(_ context.Context, spaceID string) (*store.FetchPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.SpaceID == spaceID && record.Status != store.FetchPlanApplied {
			clone := *record
			return &clone, nil
		}
	}
	return nil, nil
}

func (s *memPlanStore) LatestApplied(_ context.Context, spaceID string) (*store.FetchPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.records) - 1; i >= 0; i-- {
		if s.records[i].SpaceID == spaceID && s.records[i].Status == store.FetchPlanApplied {
			clone := *s.records[i]
			return &clone, nil
		}
	}
	return nil, nil
}

func (s *memPlanStore) Create(_ context.Context, spaceID, plan, progress string) (*store.FetchPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.SpaceID == spaceID && record.Status != store.FetchPlanApplied {
			return nil, store.ErrFetchPlanOpen
		}
	}
	record := &store.FetchPlanRecord{ID: int64(len(s.records) + 1), SpaceID: spaceID, Status: store.FetchPlanPending, Plan: plan, Progress: progress}
	s.records = append(s.records, record)
	clone := *record
	return &clone, nil
}

func (s *memPlanStore) Update(_ context.Context, id int64, status, progress, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id-1]
	record.Status, record.Progress, record.LastError = status, progress, lastError
	return nil
}

func (s *memPlanStore) SavePlan(_ context.Context, id int64, plan string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[id-1].Plan = plan
	return nil
}

type fakeRegistrar struct {
	stale      int
	failures   int
	registered map[string][]int64
	calls      int
}

func (f *fakeRegistrar) RegisterSeriesSnapshot(_ context.Context, _, datasetID, frequency string, series []seriesset.Series, effectiveFrom int64) (marketstorage.SnapshotRegistration, error) {
	f.calls++
	if f.failures > 0 {
		f.failures--
		return marketstorage.SnapshotRegistration{}, errors.New("connection reset")
	}
	if f.stale > 0 {
		f.stale--
		return marketstorage.SnapshotRegistration{}, marketstorage.ErrStaleEffectiveFrom
	}
	set, err := seriesset.Canonicalize(series)
	if err != nil {
		return marketstorage.SnapshotRegistration{}, err
	}
	if f.registered == nil {
		f.registered = map[string][]int64{}
	}
	key := datasetID + "|" + frequency
	f.registered[key] = append(f.registered[key], effectiveFrom)
	return marketstorage.SnapshotRegistration{Hash: set.Hash, Count: uint32(len(set.Series))}, nil
}

type fakePlanCloud struct {
	nodes      map[string]scfinvoker.Node
	clock      func() time.Time
	unready    map[string]scfinvoker.Node
	deleted    []string
	upgraded   []string
	jobs       map[string][]scfinvoker.BatchItemResult
	patches    map[string]*cloudnodepb.NodeRuntimeConfigPatch
	created    []string
	submits    int
	failNodes  map[string]bool
	leaseCalls int
	releases   int
}

func newFakePlanCloud() *fakePlanCloud {
	return &fakePlanCloud{nodes: map[string]scfinvoker.Node{}, unready: map[string]scfinvoker.Node{}, jobs: map[string][]scfinvoker.BatchItemResult{}, patches: map[string]*cloudnodepb.NodeRuntimeConfigPatch{}, failNodes: map[string]bool{}}
}

func (c *fakePlanCloud) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	nodes := make([]scfinvoker.Node, 0, len(c.nodes))
	for _, node := range c.nodes {
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (c *fakePlanCloud) ListTimerMarketFetchersFresh(ctx context.Context, spaceID string) ([]scfinvoker.Node, error) {
	nodes, err := c.ListTimerMarketFetchers(ctx, spaceID)
	for index := range nodes {
		metadata := map[string]any{}
		for key, value := range nodes[index].Metadata {
			metadata[key] = value
		}
		if _, set := metadata["timer_last_readback_at"]; !set && c.clock != nil {
			metadata["timer_last_readback_at"] = c.clock().UTC().Format(time.RFC3339Nano)
		}
		nodes[index].Metadata = metadata
	}
	return nodes, err
}

func (c *fakePlanCloud) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (c *fakePlanCloud) ListUnreadyMarketFetchers(context.Context, string, string) ([]scfinvoker.Node, error) {
	nodes := make([]scfinvoker.Node, 0, len(c.unready))
	for _, node := range c.unready {
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (c *fakePlanCloud) SubmitDeployNodes(_ context.Context, _ string, nodeIDs []string, packageID string, _ *scfinvoker.CollectorPublishLease) (string, error) {
	for _, id := range nodeIDs {
		node := c.nodes[id]
		node.PackageID = packageID
		c.nodes[id] = node
		c.upgraded = append(c.upgraded, id)
	}
	return c.newJob(nodeIDs), nil
}

func (c *fakePlanCloud) SubmitDeleteNodes(_ context.Context, _ string, nodeIDs []string, _ *scfinvoker.CollectorPublishLease) (string, error) {
	for _, id := range nodeIDs {
		delete(c.unready, id)
		delete(c.nodes, id)
	}
	c.deleted = append(c.deleted, nodeIDs...)
	return c.newJob(nodeIDs), nil
}

func (c *fakePlanCloud) newJob(names []string) string {
	c.submits++
	id := fmt.Sprintf("job-%d", c.submits)
	items := make([]scfinvoker.BatchItemResult, 0, len(names))
	for _, name := range names {
		if c.failNodes[name] {
			items = append(items, scfinvoker.BatchItemResult{NodeID: name, Failed: true, Message: "boom"})
			continue
		}
		items = append(items, scfinvoker.BatchItemResult{NodeID: name, Success: true})
	}
	c.jobs[id] = items
	return id
}

func (c *fakePlanCloud) SubmitCreateNodes(_ context.Context, _ string, items []*cloudnodepb.NodeCreateItem) (string, error) {
	names := make([]string, 0, len(items))
	for _, item := range items {
		name := item.GetMetadata().AsMap()["function_name"].(string)
		names = append(names, name)
		if !c.failNodes[name] {
			c.nodes[name] = scfinvoker.Node{NodeID: name, FunctionName: name, Region: item.GetRegion(), TriggerType: "timer", Metadata: map[string]any{}}
			c.created = append(c.created, name)
		}
	}
	return c.newJob(names), nil
}

func (c *fakePlanCloud) SubmitRuntimeConfigs(_ context.Context, _ string, patches []*cloudnodepb.NodeRuntimeConfigPatch) (string, error) {
	// 与 CloudNode 的预检一致：cron 不能为空，任何一项不合法整批拒绝。
	for _, patch := range patches {
		if strings.TrimSpace(patch.GetTimerCron()) == "" {
			return "", errors.New("INVALID_PARAM: timer_cron is required")
		}
	}
	names := make([]string, 0, len(patches))
	for _, patch := range patches {
		names = append(names, patch.GetNodeId())
		if !c.failNodes[patch.GetNodeId()] {
			c.patches[patch.GetNodeId()] = patch
		}
	}
	return c.newJob(names), nil
}

func (c *fakePlanCloud) GetBatchResults(_ context.Context, _, jobID string) (*cloudnodepb.NodeBatchSummary, []scfinvoker.BatchItemResult, error) {
	items := c.jobs[jobID]
	status := cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS
	for _, item := range items {
		if item.Failed {
			status = cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL
		}
	}
	return &cloudnodepb.NodeBatchSummary{JobId: jobID, Status: status}, items, nil
}

func (c *fakePlanCloud) AcquireCollectorPublishLease(_ context.Context, spaceID, holder string) (*scfinvoker.CollectorPublishLease, error) {
	c.leaseCalls++
	return &scfinvoker.CollectorPublishLease{SpaceID: spaceID, LeaseID: "lease", HolderID: holder, FencingToken: int64(c.leaseCalls), ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (c *fakePlanCloud) RenewCollectorPublishLease(_ context.Context, lease *scfinvoker.CollectorPublishLease) (*scfinvoker.CollectorPublishLease, error) {
	return lease, nil
}

func (c *fakePlanCloud) ReleaseCollectorPublishLease(context.Context, *scfinvoker.CollectorPublishLease) error {
	c.releases++
	return nil
}

type fakeActivity struct {
	failures int
	entries  map[string]ActivityEntry
}

func (a *fakeActivity) AppendActivityLogs(_ context.Context, _ string, entries []ActivityEntry) error {
	if a.failures > 0 {
		a.failures--
		return errors.New("admin unavailable")
	}
	if a.entries == nil {
		a.entries = map[string]ActivityEntry{}
	}
	for _, entry := range entries {
		if _, exists := a.entries[entry.DedupKey]; !exists {
			a.entries[entry.DedupKey] = entry
		}
	}
	return nil
}

type fakeLateness struct{ values map[string]int }

func (l *fakeLateness) EnsureAllowedLateness(_ context.Context, _, datasetID string, seconds int) error {
	if l.values == nil {
		l.values = map[string]int{}
	}
	l.values[datasetID] = seconds
	return nil
}

type planTasksStub struct{ tasks []domain.CollectionTask }

func (s *planTasksStub) ListEnabled(context.Context, string) ([]domain.CollectionTask, error) {
	return append([]domain.CollectionTask(nil), s.tasks...), nil
}

type planSymbolsStub struct{ subjects []string }

func (s *planSymbolsStub) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{SubjectTags: []string{"binance_spot"}}, nil
}

func (s *planSymbolsStub) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	items := make([]domain.Subject, 0, len(s.subjects))
	for _, subject := range s.subjects {
		items = append(items, domain.Subject{SubjectID: subject, Status: "active"})
	}
	return items, nil
}

type planHarness struct {
	t         *testing.T
	store     *memPlanStore
	registrar *fakeRegistrar
	cloud     *fakePlanCloud
	activity  *fakeActivity
	lateness  *fakeLateness
	symbols   *planSymbolsStub
	tasks     *planTasksStub
	now       time.Time
}

func newPlanHarness(t *testing.T, subjectCount int) *planHarness {
	h := &planHarness{
		t: t, store: &memPlanStore{}, registrar: &fakeRegistrar{}, cloud: newFakePlanCloud(), activity: &fakeActivity{}, lateness: &fakeLateness{},
		symbols: &planSymbolsStub{}, now: time.Date(2026, 10, 12, 8, 30, 20, 0, time.UTC),
	}
	h.cloud.clock = func() time.Time { return h.now }
	h.setSubjects(subjectCount)
	h.tasks = &planTasksStub{tasks: []domain.CollectionTask{{
		SpaceID: "crypto", TaskID: "spot-1m", Enabled: true, DataType: "kline",
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot","frequency":"1m","fetch_mode":"timer"}`,
	}}}
	return h
}

func (h *planHarness) setSubjects(count int) {
	h.symbols.subjects = h.symbols.subjects[:0]
	for i := 0; i < count; i++ {
		h.symbols.subjects = append(h.symbols.subjects, fmt.Sprintf("S%03d-USDT", i))
	}
}

func (h *planHarness) reconciler() *PlanReconciler {
	return &PlanReconciler{
		SpaceID: "crypto", Options: testPlanOptions(),
		Deploy: PlanDeploy{CloudAccountID: "acct", PackageID: "pkg", Namespace: "default", MemoryMB: 64, Networks: map[string]PlanNetwork{"ap-nanjing": {VPCID: "vpc-1", SubnetID: "subnet-1"}},
			BaseEnvironment: map[string]string{"MOOX_SPACE_ID": "crypto"}},
		Groups: &Reconciler{Tasks: h.tasks, Symbols: h.symbols, TaskFilter: func(p *domain.CollectParams) bool { return p.TimerManaged() }},
		Store:  h.store, Storage: h.registrar, Cloud: h.cloud, Access: testSCFAccessEnvironment, Lateness: h.lateness, Activity: h.activity,
		Now: func() time.Time { return h.now }, JobWait: time.Millisecond, PollInterval: time.Millisecond,
	}
}

func (h *planHarness) reconcile(r *PlanReconciler) error {
	return r.Reconcile(context.Background())
}

func (h *planHarness) open() *store.FetchPlanRecord {
	record, err := h.store.Open(context.Background(), "crypto")
	require.NoError(h.t, err)
	return record
}

func (h *planHarness) content(record *store.FetchPlanRecord) *FetchPlanContent {
	content, err := decodeFetchPlanContent(record.Plan)
	require.NoError(h.t, err)
	return content
}

func TestPlanReconcilerFullLifecycle(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))

	record := h.open()
	require.NotNil(t, record, "the plan stays open until the window ends")
	require.Equal(t, store.FetchPlanApplying, record.Status)
	content := h.content(record)
	change := content.Datasets["ds_spot|1m"]
	require.Equal(t, time.Date(2026, 10, 12, 8, 37, 0, 0, time.UTC).Unix(), change.P)
	require.Equal(t, 15, h.lateness.values["ds_spot"])
	require.Equal(t, []int64{change.P}, h.registrar.registered["ds_spot|1m"])
	require.Len(t, h.cloud.created, 3)
	require.Len(t, h.cloud.patches, 3)
	for name, patch := range h.cloud.patches {
		require.True(t, patch.GetTimerEnabled(), name)
		require.Equal(t, "0 * * * * * *", patch.GetTimerCron())
		plan, err := DecodePlan(patch.GetManagedEnvironment()[PlanEnvName])
		require.NoError(t, err)
		require.Len(t, plan.Datasets[0].Copies, 1)
		require.Equal(t, "access@storage", patch.GetManagedEnvironment()["MOOX_ACCESS_ID"])
		require.NotEmpty(t, patch.GetCollectorPublishLeaseId())
	}
	require.Equal(t, 2, h.cloud.releases, "the lease is released after the create batch and the config batch")

	// 窗口结束前什么都不做；之后收尾并标记已生效。
	h.now = time.Unix(change.P, 0).Add(10 * time.Second)
	require.NoError(t, h.reconcile(r))
	require.NotNil(t, h.open())
	h.now = time.Unix(change.P, 0).Add(30 * time.Second)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.Len(t, h.activity.entries, 2)

	// 没有变化：不会产生新计划（云端校验另有测试，这里让它保持已完成）。
	submits := h.cloud.submits
	r.lastVerified = h.now
	h.now = h.now.Add(5 * time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.Equal(t, submits, h.cloud.submits)
	require.Len(t, h.store.records, 1)
}

func TestPlanReconcilerSubjectChangeUsesTwoCopies(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	first := h.content(h.open())
	h.now = time.Unix(first.Datasets["ds_spot|1m"].P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	h.setSubjects(119)
	h.cloud.patches = map[string]*cloudnodepb.NodeRuntimeConfigPatch{}
	require.NoError(t, h.reconcile(r))
	record := h.open()
	require.NotNil(t, record)
	content := h.content(record)
	change := content.Datasets["ds_spot|1m"]
	require.Equal(t, []string{"S119-USDT"}, change.Removed)
	require.NotEmpty(t, change.OldHash)
	require.Len(t, h.registrar.registered["ds_spot|1m"], 2)
	for name, patch := range h.cloud.patches {
		plan, err := DecodePlan(patch.GetManagedEnvironment()[PlanEnvName])
		require.NoError(t, err, name)
		copies := plan.Datasets[0].Copies
		require.Len(t, copies, 2, name)
		require.Equal(t, change.P, copies[1].From)
		require.Equal(t, change.Hash, copies[1].Hash)
		require.Equal(t, change.OldHash, copies[0].Hash)
	}

	// 收尾：每个函数只留最新一份。
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	for name, patch := range h.cloud.patches {
		plan, err := DecodePlan(patch.GetManagedEnvironment()[PlanEnvName])
		require.NoError(t, err, name)
		require.Len(t, plan.Datasets[0].Copies, 1, name)
	}
	logged := 0
	for key := range h.activity.entries {
		if strings.Contains(key, ".2.") {
			logged++
		}
	}
	require.Equal(t, 2, logged, "second plan writes a created and an applied entry")
}

func TestPlanReconcilerRetriesOnlyFailedFunctions(t *testing.T) {
	h := newPlanHarness(t, 120)
	names := []string{}
	state, _ := BuildDesiredState(testPlanOptions(), testGroups(120, "1m"), nil)
	for name := range state.Functions {
		names = append(names, name)
	}
	sort.Strings(names)
	h.cloud.failNodes[names[1]] = true
	r := h.reconciler()
	require.Error(t, h.reconcile(r))
	record := h.open()
	progress, err := decodeFetchPlanProgress(record.Progress)
	require.NoError(t, err)
	require.True(t, progress.Created[names[0]])
	require.False(t, progress.Created[names[1]])
	require.Contains(t, record.LastError, names[1])

	delete(h.cloud.failNodes, names[1])
	before := len(h.cloud.created)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, before+1, len(h.cloud.created), "only the failed function is created again")
	require.Len(t, h.cloud.patches, 3)
}

func TestPlanReconcilerRestartContinuesWithoutNewPeriod(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.registrar.failures = 1
	r := h.reconciler()
	require.Error(t, h.reconcile(r))
	record := h.open()
	require.Equal(t, store.FetchPlanPending, record.Status)
	period := h.content(record).Datasets["ds_spot|1m"].P

	// 重启：新的对账器实例，晚一些恢复，但没有登记成功过，P 仍然在未来则不重选。
	h.now = h.now.Add(20 * time.Second)
	restarted := h.reconciler()
	require.NoError(t, h.reconcile(restarted))
	record = h.open()
	require.Equal(t, period, h.content(record).Datasets["ds_spot|1m"].P)
	require.Equal(t, []int64{period}, h.registrar.registered["ds_spot|1m"])
	require.Len(t, h.cloud.patches, 2)
}

func TestPlanReconcilerReselectsPeriodOnlyOnExplicitStaleRejection(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.registrar.failures = 1
	r := h.reconciler()
	require.Error(t, h.reconcile(r), "an unknown outcome is replayed, not reselected")
	original := h.content(h.open()).Datasets["ds_spot|1m"].P
	require.Empty(t, h.registrar.registered)

	// 保存计划后 Collector 宕机，恢复时 P 已过：Storage 明确拒绝，重选更晚的 P。
	h.now = h.now.Add(10 * time.Minute)
	h.registrar.stale = 1
	require.NoError(t, h.reconcile(r))
	reselected := h.content(h.open()).Datasets["ds_spot|1m"].P
	require.Greater(t, reselected, original)
	require.Empty(t, h.registrar.registered)

	require.NoError(t, h.reconcile(r))
	require.Equal(t, []int64{reselected}, h.registrar.registered["ds_spot|1m"])
	for _, patch := range h.cloud.patches {
		plan, err := DecodePlan(patch.GetManagedEnvironment()[PlanEnvName])
		require.NoError(t, err)
		require.Equal(t, reselected, plan.Datasets[0].Copies[0].From)
	}
}

func TestPlanReconcilerActivityLogFailureKeepsPlanOpenAndDoesNotDuplicate(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.activity.failures = 3
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.Error(t, h.reconcile(r))
	require.NotNil(t, h.open(), "the plan is not applied before both entries are written")
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.Len(t, h.activity.entries, 2)
}

func TestPlanReconcilerRetiresFunctionsWhenSubjectsShrink(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	h.setSubjects(40)
	require.NoError(t, h.reconcile(r))
	record := h.open()
	content := h.content(record)
	var retired []string
	for name, function := range content.Functions {
		if function.New == nil {
			retired = append(retired, name)
		}
	}
	require.Len(t, retired, 2)
	h.now = time.Unix(content.Datasets["ds_spot|1m"].P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	for _, name := range retired {
		require.False(t, h.cloud.patches[name].GetTimerEnabled(), "%s must be disabled after the window", name)
	}
}

func TestPlanReconcilerKeepsStateWhenEnabledTasksHaveNoSubjects(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	h.setSubjects(0)
	for i := 0; i < 2*planGoneDebounce; i++ {
		require.NoError(t, h.reconcile(r))
		require.Nil(t, h.open(), "an enabled task with no resolvable subjects must not retire the fleet")
	}
}

func TestPlanReconcilerRepairsDriftHourly(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	// 云端读回与计划一致：不下发。
	for name, patch := range h.cloud.patches {
		h.cloud.nodes[name] = scfinvoker.Node{NodeID: name, Metadata: map[string]any{
			"assignment_hash": patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "timer_actual_enabled": true,
			"access_address": patch.GetManagedEnvironment()["MOOX_ACCESS_ADDRESS"], "access_id": patch.GetManagedEnvironment()["MOOX_ACCESS_ID"], "access_key_id": patch.GetManagedEnvironment()["MOOX_CALLER_KEY_ID"],
			"timer_available_status": "Available", "timer_actual_cron": patch.GetTimerCron(),
		}}
	}
	h.now = h.now.Add(2 * time.Hour)
	before := h.cloud.submits
	require.NoError(t, h.reconcile(r))
	require.Equal(t, before, h.cloud.submits)

	// 被手工改动：下一次读回时补推。
	for name := range h.cloud.nodes {
		node := h.cloud.nodes[name]
		node.Metadata["assignment_hash"] = "tampered"
		h.cloud.nodes[name] = node
		break
	}
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, before+1, h.cloud.submits)
}

func TestPlanReconcilerRejectsOversizedEnvironment(t *testing.T) {
	h := newPlanHarness(t, 400)
	r := h.reconciler()
	r.Options.SymbolsPerFunction = 200
	h.tasks.tasks[0].CollectParams = strings.Replace(h.tasks.tasks[0].CollectParams, `"fetch_mode":"timer"`, `"fetch_mode":"timer","symbols_per_function":200`, 1)
	r.Deploy.BaseEnvironment["PADDING"] = strings.Repeat("x", 3000)
	require.Error(t, h.reconcile(r))
	require.Empty(t, h.store.records, "no plan is saved when the environment cannot fit")
}

func TestPlanManagerRunsOneWorkerPerSpaceAndDisablesRemovedSpace(t *testing.T) {
	h := newPlanHarness(t, 60)
	var mu sync.Mutex
	spaces := []string{"crypto"}
	created := 0
	manager := &PlanManager{
		Spaces: func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), spaces...), nil
		},
		Factory: func(space string) (*PlanReconciler, error) {
			mu.Lock()
			defer mu.Unlock()
			created++
			r := h.reconciler()
			r.Now = time.Now
			return r, nil
		},
		Interval: 40 * time.Millisecond, Offset: 5 * time.Millisecond, Timeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { manager.Run(ctx); close(finished) }()

	require.Eventually(t, func() bool {
		record, _ := h.store.Open(context.Background(), "crypto")
		return record != nil
	}, 5*time.Second, 10*time.Millisecond, "the space worker creates a fetch plan")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	require.Equal(t, 1, created, "one reconciler per space")
	spaces = nil
	mu.Unlock()

	require.Eventually(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.workers) == 0
	}, 5*time.Second, 10*time.Millisecond, "the worker exits only after timers are disabled")
	cancel()
	<-finished
	disabled := 0
	for _, patch := range h.cloud.patches {
		if !patch.GetTimerEnabled() {
			disabled++
		}
	}
	require.Equal(t, 2, disabled, "every function of the removed space is disabled")
}

func TestPlanReconcilerCreateFailureDoesNotBlockExistingFunctions(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	// 120 → 140 个标的需要新增第 3 个函数以外的容量变化：50*3=150 足够，无需新函数；
	// 改成 160 个标的 → 4 个函数，其中新增 1 个，创建失败。
	h.setSubjects(160)
	state, _ := BuildDesiredState(testPlanOptions(), testGroups(160, "1m"), nil)
	var failing string
	for name := range state.Functions {
		if strings.HasSuffix(name, "-003") {
			failing = name
		}
	}
	h.cloud.failNodes[failing] = true
	h.cloud.patches = map[string]*cloudnodepb.NodeRuntimeConfigPatch{}
	require.Error(t, h.reconcile(r))
	require.Len(t, h.cloud.patches, 3, "the three existing functions still receive their transitional environment")
	require.Empty(t, h.cloud.patches[failing])
	delete(h.cloud.failNodes, failing)
	require.NoError(t, h.reconcile(r))
	require.NotNil(t, h.cloud.patches[failing])
}

func TestPlanReconcilerRetiredFunctionsGetAValidCronWhenDisabled(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	h.setSubjects(40)
	require.NoError(t, h.reconcile(r))
	p := h.content(h.open()).Datasets["ds_spot|1m"].P
	h.now = time.Unix(p, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r), "the final push of retired functions must pass CloudNode's cron preflight")
	require.Nil(t, h.open())
}

func TestPlanReconcilerRecreatesMissingFunctionsAndDetectsAccessDrift(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	created := len(h.cloud.created)
	for name := range h.cloud.nodes {
		delete(h.cloud.nodes, name)
		break
	}
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, created+1, len(h.cloud.created), "a function deleted in the cloud is rebuilt from the applied plan")
}

func TestPlanReconcilerRollbackStopsDatasetAtBoundary(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))

	// 回退：任务仍是 timer 模式，但设置了旧链路下界 T'。
	boundary := h.now.Truncate(time.Hour).Add(3 * time.Hour)
	h.tasks.tasks[0].CollectParams = strings.Replace(h.tasks.tasks[0].CollectParams, `"fetch_mode":"timer"`, `"fetch_mode":"timer","invoke_from_period":"`+boundary.Format(time.RFC3339)+`"`, 1)
	require.NoError(t, h.reconcile(r))
	record := h.open()
	require.NotNil(t, record)
	stop := h.content(record).Datasets["ds_spot|1m"]
	require.True(t, stop.Gone)
	require.Equal(t, boundary.Unix(), stop.P, "the dataset stops exactly where the old chain takes over")
	for name, patch := range h.cloud.patches {
		plan, err := DecodePlan(patch.GetManagedEnvironment()[PlanEnvName])
		require.NoError(t, err, name)
		copies := plan.Datasets[0].Copies
		require.Len(t, copies, 2)
		require.Empty(t, copies[1].Subjects)
		require.Equal(t, boundary.Unix(), copies[1].From)
	}
}

func TestTransitionalPlanDropsRemovedSymbolExceptions(t *testing.T) {
	old := &Plan{V: 1, Market: PlanMarket{MarketID: "crypto", Provider: "binance", SourceID: "s", MarketType: "spot"}, Fetch: PlanFetch{Concurrency: 5},
		SymbolExceptions: map[string]string{"1000SHIB-USDT": "1000SHIBUSDT"},
		Datasets:         []PlanDataset{{DatasetID: "d", Frequency: "1m", LatenessS: 15, Copies: []PlanCopy{{From: 100, Hash: "h", Count: 2, Subjects: []PlanSubject{{ID: "1000SHIB-USDT", Index: 0}, {ID: "BTC-USDT", Index: 1}}}}}}}
	next := &Plan{V: 1, Market: old.Market, Fetch: old.Fetch,
		Datasets: []PlanDataset{{DatasetID: "d", Frequency: "1m", LatenessS: 15, Copies: []PlanCopy{{Hash: "h2", Count: 2, Subjects: []PlanSubject{{ID: "1000SHIB-USDT", Index: 0}, {ID: "BTC-USDT", Index: 1}}}}}}}
	content := &FetchPlanContent{Datasets: map[string]*PlanDatasetChange{"d|1m": {Changed: true, HashChanged: true, P: 200, PlanDatasetState: PlanDatasetState{DatasetID: "d", Frequency: "1m", Hash: "h2", Count: 2}}}}
	function := &PlanFunctionChange{Name: "f", Old: old, New: next}
	transitional := content.TransitionalPlan(function)
	require.Empty(t, transitional.SymbolExceptions, "an exception that was removed from the new plan must not survive")

	next.SymbolExceptions = map[string]string{"1000SHIB-USDT": "1000SHIBUSDT"}
	require.Equal(t, "1000SHIBUSDT", content.TransitionalPlan(function).SymbolExceptions["1000SHIB-USDT"])
}

func TestBuildDesiredStateRejectsMarketTypeClash(t *testing.T) {
	groups := testGroups(5, "1m")
	other := testGroups(5, "1h")[0]
	other.Provider, other.SourceID = "okx", "okx"
	other.DatasetID = "ds_okx"
	_, err := BuildDesiredState(testPlanOptions(), append(groups, other), nil)
	require.ErrorContains(t, err, "more than one market")
}

func TestPlanLeadIsLongerWhenFunctionsMustBeCreated(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	require.GreaterOrEqual(t, change.P-h.now.Unix(), int64(planLeadTimeWithCreate/time.Second))
}

func TestNamespaceShardsEveryFiftyFunctions(t *testing.T) {
	deploy := PlanDeploy{Namespace: "moox-stockcn-plan", NamespaceCapacity: 50}
	require.Equal(t, "moox-stockcn-plan", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-000"))
	require.Equal(t, "moox-stockcn-plan", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-049"))
	require.Equal(t, "moox-stockcn-plan-2", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-050"))
	require.Equal(t, "moox-stockcn-plan-4", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-169"))
	require.Equal(t, "plain", PlanDeploy{Namespace: "plain"}.NamespaceFor("f-999"))
}

func TestStaggerDelayStartsAtTheConfiguredBase(t *testing.T) {
	plan := &Plan{Fetch: PlanFetch{StaggerBaseMS: 5000, StaggerMS: 35000}}
	for _, name := range []string{"a", "b", "moox-fetch-plan-stockcn-equity-nanjing-017"} {
		delay := plan.StaggerDelay(name)
		require.GreaterOrEqual(t, delay, 5*time.Second)
		require.LessOrEqual(t, delay, 40*time.Second)
	}
	require.Equal(t, 5*time.Second, (&Plan{Fetch: PlanFetch{StaggerBaseMS: 5000}}).StaggerDelay("x"))
}

func TestStockCNPlanSkipsNonTradingPeriods(t *testing.T) {
	// 没有交易日历配置时（如本测试环境），过滤保守地拒绝执行 stockcn 周期；crypto 不受影响。
	handler := &Handler{PlanPeriodValid: func(market PlanMarket, frequency string, period time.Time) bool {
		return market.MarketID != StockCNSpaceID
	}}
	require.True(t, handler.PlanPeriodValid(PlanMarket{MarketID: "crypto"}, "1m", time.Now()))
	require.False(t, handler.PlanPeriodValid(PlanMarket{MarketID: StockCNSpaceID}, "1m", time.Now()))
}

func TestPlanReconcilerClearsStaleCreateReservationsBeforeRecreating(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	state, _ := BuildDesiredState(testPlanOptions(), testGroups(60, "1m"), nil)
	var name string
	for fn := range state.Functions {
		name = fn
		break
	}
	// 创建失败后遗留的预留：目录里有记录但没有部署完成，且已超过等待时长。
	h.cloud.unready[name] = scfinvoker.Node{NodeID: name, FunctionName: name, TriggerType: "timer", ModifiedAt: h.now.Add(-time.Hour)}
	require.NoError(t, h.reconcile(r))
	require.Contains(t, h.cloud.deleted, name, "the stale reservation is deleted before creating")
	require.Contains(t, h.cloud.created, name)
}

func TestPlanReconcilerWaitsForFreshCreateReservations(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	state, _ := BuildDesiredState(testPlanOptions(), testGroups(60, "1m"), nil)
	var name string
	for fn := range state.Functions {
		name = fn
		break
	}
	h.cloud.unready[name] = scfinvoker.Node{NodeID: name, FunctionName: name, TriggerType: "timer", ModifiedAt: h.now.Add(-time.Minute)}
	require.NoError(t, h.reconcile(r))
	require.Empty(t, h.cloud.deleted, "a reservation that may still be in progress is left alone")
	require.NotContains(t, h.cloud.created, name)
}

func TestPlanManagerDisablesConfiguredSpacesThatAreInactiveAtStartup(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r)) // 先让该空间有函数与计划，模拟停机前的状态
	created := 0
	var mu sync.Mutex
	manager := &PlanManager{
		Configured: []string{"crypto"},
		Spaces:     func(context.Context) ([]string, error) { return nil, nil },
		Factory: func(string) (*PlanReconciler, error) {
			mu.Lock()
			defer mu.Unlock()
			created++
			reconciler := h.reconciler()
			reconciler.Now = time.Now
			return reconciler, nil
		},
		Interval: 40 * time.Millisecond, Offset: 5 * time.Millisecond, Timeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { manager.Run(ctx); close(finished) }()
	require.Eventually(t, func() bool {
		for _, patch := range h.cloud.patches {
			if patch.GetTimerEnabled() {
				return false
			}
		}
		return len(h.cloud.patches) > 0
	}, 5*time.Second, 10*time.Millisecond, "timers of the inactive space are disabled after a restart")
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-finished
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, created, "a space that is already disabled is not wound down again")
}

func TestPlanReconcilerRecreatesFunctionsDeletedInTheCloudAndIgnoresFailedReadbacks(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	var names []string
	for name, patch := range h.cloud.patches {
		names = append(names, name)
		h.cloud.nodes[name] = scfinvoker.Node{NodeID: name, Metadata: map[string]any{
			"assignment_hash": patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "timer_actual_enabled": true,
			"access_address": patch.GetManagedEnvironment()["MOOX_ACCESS_ADDRESS"], "access_id": patch.GetManagedEnvironment()["MOOX_ACCESS_ID"], "access_key_id": patch.GetManagedEnvironment()["MOOX_CALLER_KEY_ID"],
			"timer_available_status": "Available", "timer_actual_cron": patch.GetTimerCron(),
		}}
	}
	sort.Strings(names)
	require.GreaterOrEqual(t, len(names), 2)
	// 一个函数在云端已被删除（目录里还有记录），另一个这次读回失败（云端暂时读不到）。
	deleted, unreadable := names[0], names[1]
	node := h.cloud.nodes[deleted]
	node.Metadata["timer_status_error"] = "ResourceNotFound.Function: function not found"
	h.cloud.nodes[deleted] = node
	node = h.cloud.nodes[unreadable]
	node.Metadata["timer_status_error"] = "context deadline exceeded"
	h.cloud.nodes[unreadable] = node

	h.now = h.now.Add(2 * time.Hour)
	h.cloud.created = nil
	require.NoError(t, h.reconcile(r))
	require.Equal(t, []string{deleted}, h.cloud.deleted, "only the function that is gone in the cloud is removed from the catalog")
	require.Equal(t, []string{deleted}, h.cloud.created, "and is recreated afterwards")
	require.True(t, r.lastVerified.Before(h.now), "an unreadable function keeps the verification open for a retry")
}

func TestPlanReconcilerUpgradesFunctionCodeWhenThePackageChanges(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	for name, patch := range h.cloud.patches {
		h.cloud.nodes[name] = scfinvoker.Node{NodeID: name, PackageID: "pkg-old", Metadata: map[string]any{
			"assignment_hash": patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "timer_actual_enabled": true,
			"access_address": patch.GetManagedEnvironment()["MOOX_ACCESS_ADDRESS"], "access_id": patch.GetManagedEnvironment()["MOOX_ACCESS_ID"], "access_key_id": patch.GetManagedEnvironment()["MOOX_CALLER_KEY_ID"],
			"timer_available_status": "Available", "timer_actual_cron": patch.GetTimerCron(),
		}}
	}
	r.Deploy.PackageID = "pkg-new"
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Len(t, h.cloud.upgraded, len(h.cloud.nodes), "every function still on the old package is upgraded")
	for _, node := range h.cloud.nodes {
		require.Equal(t, "pkg-new", node.PackageID)
	}
	before := len(h.cloud.upgraded)
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Len(t, h.cloud.upgraded, before, "functions already on the configured package are left alone")
}
