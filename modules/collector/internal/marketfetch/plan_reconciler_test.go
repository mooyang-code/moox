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
	jobs       map[string][]scfinvoker.BatchItemResult
	patches    map[string]*cloudnodepb.NodeRuntimeConfigPatch
	created    []string
	submits    int
	failNodes  map[string]bool
	leaseCalls int
	releases   int
}

func newFakePlanCloud() *fakePlanCloud {
	return &fakePlanCloud{nodes: map[string]scfinvoker.Node{}, jobs: map[string][]scfinvoker.BatchItemResult{}, patches: map[string]*cloudnodepb.NodeRuntimeConfigPatch{}, failNodes: map[string]bool{}}
}

func (c *fakePlanCloud) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	nodes := make([]scfinvoker.Node, 0, len(c.nodes))
	for _, node := range c.nodes {
		nodes = append(nodes, node)
	}
	return nodes, nil
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
	require.Equal(t, time.Date(2026, 10, 12, 8, 33, 0, 0, time.UTC).Unix(), change.P)
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

	// 没有变化：不会产生新计划。
	submits := h.cloud.submits
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
