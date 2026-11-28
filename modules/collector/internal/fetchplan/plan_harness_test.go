package fetchplan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 对账器测试的夹具：内存计划库、假 Storage、假活动日志，以及真正的 cloud.TencentFunctions 接在 T0 的假腾讯云
// （cloud/cloudtest，带状态、记录写请求）上。断言看的是“云上状态变成了什么”。

func init() {
	// 假腾讯云在被读 PendingReads 次后回到 Active：等待间隔与重试退避调小，测试不必真的等。
	cloud.UseFastPollingForTest()
}

type memPlanStore struct {
	mu      sync.Mutex
	records []*store.FetchPlanRecord
	// shadow 非空时，计划记录的每次写入都同样写进这个真实的 SQLite 计划库（时间线等价测试用）：
	// 时间线行走的是生产的写入路径，与计划记录同一个事务。记录本身仍以内存里的为准。
	shadow *store.FetchPlanRepository
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

func (s *memPlanStore) Create(ctx context.Context, spaceID, plan, progress string, timeline ...store.SeriesTimelineEntry) (*store.FetchPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.SpaceID == spaceID && record.Status != store.FetchPlanApplied {
			return nil, store.ErrFetchPlanOpen
		}
	}
	record := &store.FetchPlanRecord{ID: int64(len(s.records) + 1), SpaceID: spaceID, Status: store.FetchPlanPending, Plan: plan, Progress: progress}
	if s.shadow != nil {
		shadowed, err := s.shadow.Create(ctx, spaceID, plan, progress, timeline...)
		if err != nil {
			return nil, err
		}
		if shadowed.ID != record.ID {
			return nil, fmt.Errorf("shadow fetch plan id %d does not match %d", shadowed.ID, record.ID)
		}
	}
	s.records = append(s.records, record)
	clone := *record
	return &clone, nil
}

func (s *memPlanStore) Update(ctx context.Context, id int64, status, progress, lastError string, timeline ...store.SeriesTimelineEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[id-1]
	if s.shadow != nil {
		if err := s.shadow.Update(ctx, id, status, progress, lastError, timeline...); err != nil {
			return err
		}
	}
	record.Status, record.Progress, record.LastError = status, progress, lastError
	return nil
}

// DeleteOpen 删掉一份 pending 的未生效计划（记录留在切片里、空间清空，编号仍按下标）。
func (s *memPlanStore) DeleteOpen(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shadow != nil {
		if err := s.shadow.DeleteOpen(ctx, id); err != nil {
			return err
		}
	}
	record := s.records[id-1]
	if record.Status != store.FetchPlanPending {
		return fmt.Errorf("fetch plan %d is %s", id, record.Status)
	}
	record.SpaceID = ""
	return nil
}

func (s *memPlanStore) SavePlan(ctx context.Context, id int64, plan string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shadow != nil {
		if err := s.shadow.SavePlan(ctx, id, plan); err != nil {
			return err
		}
	}
	s.records[id-1].Plan = plan
	return nil
}

// fakeRegistrar 按 Storage 的真实规则（pebble.RegisterSeriesSnapshot）维护每个“数据集|频率”的生效表：
// 新生效点必须严格晚于最后一条并且在未来；同 P 同 hash 是重放；同 P 不同 hash 只在它是最后一条、仍在未来时覆盖；其余返回 effective_from_stale。
// clock 为空时不检查“在未来”。stale、failures 是注入的失败次数；lostReplies 是登记成功但回复丢失（返回错误）的次数。
type fakeRegistrar struct {
	stale       int
	failures    int
	lostReplies int
	registered  map[string][]int64
	calls       int
	clock       func() time.Time
	effective   map[string]map[int64]string
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
	key := datasetID + "|" + frequency
	if f.effective == nil {
		f.effective = map[string]map[int64]string{}
	}
	entries := f.effective[key]
	if entries == nil {
		entries = map[int64]string{}
		f.effective[key] = entries
	}
	last, hasLast := int64(0), false
	for from := range entries {
		if !hasLast || from > last {
			last, hasLast = from, true
		}
	}
	future := f.clock == nil || effectiveFrom > f.clock().Unix()
	if existing, found := entries[effectiveFrom]; found {
		if existing != set.Hash && (last != effectiveFrom || !future) {
			return marketstorage.SnapshotRegistration{}, marketstorage.ErrStaleEffectiveFrom
		}
	} else if (hasLast && effectiveFrom <= last) || !future {
		return marketstorage.SnapshotRegistration{}, marketstorage.ErrStaleEffectiveFrom
	}
	entries[effectiveFrom] = set.Hash
	if f.registered == nil {
		f.registered = map[string][]int64{}
	}
	f.registered[key] = append(f.registered[key], effectiveFrom)
	if f.lostReplies > 0 {
		f.lostReplies--
		return marketstorage.SnapshotRegistration{}, errors.New("connection reset after commit")
	}
	return marketstorage.SnapshotRegistration{Hash: set.Hash, Count: uint32(len(set.Series))}, nil
}

// effectiveHash 返回生效表在 at 上的有效 hash（起点不晚于 at 的最后一条）。
func (f *fakeRegistrar) effectiveHash(key string, at int64) (string, bool) {
	best, hash, found := int64(0), "", false
	for from, value := range f.effective[key] {
		if from <= at && (!found || from > best) {
			best, hash, found = from, value, true
		}
	}
	return hash, found
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

// fakeLateness 记下每个数据集最后设的截止；failFor 里的数据集一律失败。
type fakeLateness struct {
	values  map[string]int
	failFor map[string]bool
}

func (l *fakeLateness) EnsureAllowedLateness(_ context.Context, _, datasetID string, seconds int) error {
	if l.failFor[datasetID] {
		return errors.New("storage unavailable")
	}
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

// ListKline 按结果数据集筛任务（补采路由用）。
func (s *planTasksStub) ListKline(_ context.Context, _, datasetID string) ([]domain.CollectionTask, error) {
	var out []domain.CollectionTask
	for _, task := range s.tasks {
		if task.ResultDatasetID == datasetID {
			out = append(out, task)
		}
	}
	return out, nil
}

type planSymbolsStub struct{ subjects []string }

func (s *planSymbolsStub) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{SubjectTags: []string{"binance_spot"}}, nil
}

func (s *planSymbolsStub) GetTag(_ context.Context, _, tagID string) (*storagepb.Tag, error) {
	return &storagepb.Tag{TagId: tagID, Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}}, nil
}

func (s *planSymbolsStub) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	items := make([]domain.Subject, 0, len(s.subjects))
	for _, subject := range s.subjects {
		items = append(items, domain.Subject{SubjectID: subject, Status: "active"})
	}
	return items, nil
}

// ----- 假腾讯云 -----

const (
	testAccount     = "acct"
	testPackageName = "collector"
)

// testCloud 是对账器用的云端：真正的 cloud.TencentFunctions 接在假腾讯云上，外加内存里的代码包列表；
// 同时按先后记下对账器发出的每个云端操作（Ensure、UpgradeCode、SetTimer、Delete），给下发金样与顺序断言用。
type testCloud struct {
	*cloud.TencentFunctions
	scf    *cloudtest.SCF
	client *cloudtest.Scripted
	clock  func() time.Time

	mu         sync.Mutex
	packages   []cloud.Package // 按上传先后（旧到新）的可用代码包
	packageErr error
	calls      []cloudCall
}

// cloudCall 是对账器发出的一个云端操作。
type cloudCall struct {
	At        time.Time
	Op        string // ensure、upgrade、timer、delete
	Name      string
	Spec      cloud.FunctionSpec
	Timer     cloud.Timer
	PackageID string
}

func testPackage(id string) cloud.Package {
	return cloud.Package{
		PackageID: id, PackageName: testPackageName, Version: id, Runtime: "Go1", CloudAccountID: testAccount,
		COSRegion: "ap-guangzhou", COSBucket: "fake-bucket", COSPath: "/packages/" + id + ".zip", Status: store.FunctionPackageAvailable,
	}
}

type testCredentials struct{}

func (testCredentials) Resolve(_ context.Context, account store.CloudAccount) (cloud.Credential, error) {
	return cloud.Credential{AccountID: account.AccountID, SecretID: "fake-secret-id", SecretKey: "fake-secret-key"}, nil
}

// testCLS 补齐日志服务（CLS）的上报环境，值是固定的假值。
type testCLS struct{}

func (testCLS) Apply(_ context.Context, _ string, _ cloud.Credential, environment map[string]string) bool {
	changed := false
	for key, value := range map[string]string{
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "fake-topic",
		"MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "fake-cls-secret-id", "MOOX_CLS_SECRET_KEY": "fake-cls-secret-key",
	} {
		if environment[key] != value {
			environment[key] = value
			changed = true
		}
	}
	return changed
}

// newTestCloud 用临时库（登记云账号 acct）、假密钥、假 CLS 构造接在 scf 上的云端；clock 给操作记录打时间。
func newTestCloud(t *testing.T, scf *cloudtest.SCF, clock func() time.Time) *testCloud {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.CloudAccounts().Upsert(context.Background(), store.CloudAccount{AccountID: testAccount, Provider: "tencent", CredentialSecretID: "fake-secret", COSRegion: "ap-guangzhou", COSBucket: "fake-bucket"}))
	client := cloudtest.NewScripted(scf)
	functions := cloud.NewFunctionsWithClient(db, testCredentials{}, testCLS{}, func(cloud.Credential) cloud.SCFClient { return client })
	return &testCloud{TencentFunctions: functions, scf: scf, client: client, clock: clock, packages: []cloud.Package{testPackage("pkg")}}
}

func (c *testCloud) LatestPackage(_ context.Context, name string) (*cloud.Package, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.packageErr != nil {
		return nil, c.packageErr
	}
	if len(c.packages) == 0 || name != testPackageName {
		return nil, nil
	}
	pkg := c.packages[len(c.packages)-1]
	return &pkg, nil
}

// setPackages 换掉可用代码包列表（旧到新）。
func (c *testCloud) setPackages(ids ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.packages = nil
	for _, id := range ids {
		c.packages = append(c.packages, testPackage(id))
	}
}

// setPackagesTo 换掉可用代码包列表（旧到新），包的内容由调用方给。
func (c *testCloud) setPackagesTo(packages ...cloud.Package) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.packages = append([]cloud.Package(nil), packages...)
}

func (c *testCloud) setPackageErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.packageErr = err
}

func (c *testCloud) record(call cloudCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clock != nil {
		call.At = c.clock().UTC()
	}
	c.calls = append(c.calls, call)
}

// operations 返回到目前为止记下的云端操作。
func (c *testCloud) operations() []cloudCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]cloudCall(nil), c.calls...)
}

func (c *testCloud) resetOperations() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = nil
}

// countOps 返回某种操作（可限定函数名）的次数。
func (c *testCloud) countOps(op, name string) int {
	count := 0
	for _, call := range c.operations() {
		if call.Op == op && (name == "" || call.Name == name) {
			count++
		}
	}
	return count
}

func (c *testCloud) Ensure(ctx context.Context, spec cloud.FunctionSpec) error {
	c.record(cloudCall{Op: "ensure", Name: spec.Ref.Name, Spec: spec, PackageID: spec.Package.PackageID})
	return c.TencentFunctions.Ensure(ctx, spec)
}

func (c *testCloud) UpgradeCode(ctx context.Context, ref cloud.FunctionRef, pkg cloud.Package) error {
	c.record(cloudCall{Op: "upgrade", Name: ref.Name, PackageID: pkg.PackageID})
	return c.TencentFunctions.UpgradeCode(ctx, ref, pkg)
}

func (c *testCloud) SetTimer(ctx context.Context, ref cloud.FunctionRef, timer cloud.Timer, wantPackageID string) error {
	c.record(cloudCall{Op: "timer", Name: ref.Name, Timer: timer, PackageID: wantPackageID})
	return c.TencentFunctions.SetTimer(ctx, ref, timer, wantPackageID)
}

func (c *testCloud) Delete(ctx context.Context, ref cloud.FunctionRef) error {
	c.record(cloudCall{Op: "delete", Name: ref.Name})
	return c.TencentFunctions.Delete(ctx, ref)
}

// ----- 对账器夹具 -----

type planHarness struct {
	t         *testing.T
	store     *memPlanStore
	registrar *fakeRegistrar
	cloud     *testCloud
	scf       *cloudtest.SCF
	activity  *fakeActivity
	lateness  *fakeLateness
	symbols   *planSymbolsStub
	tasks     *planTasksStub
	now       time.Time
}

func newPlanHarness(t *testing.T, subjectCount int) *planHarness {
	h := &planHarness{
		t: t, store: &memPlanStore{}, registrar: &fakeRegistrar{}, activity: &fakeActivity{}, lateness: &fakeLateness{},
		symbols: &planSymbolsStub{}, now: time.Date(2026, 10, 12, 8, 30, 20, 0, time.UTC), scf: cloudtest.NewSCF(),
	}
	h.cloud = newTestCloud(t, h.scf, func() time.Time { return h.now })
	h.registrar.clock = func() time.Time { return h.now }
	h.setSubjects(subjectCount)
	h.tasks = &planTasksStub{tasks: []domain.CollectionTask{{
		SpaceID: "crypto", TaskID: "spot-1m", Enabled: true, DataType: "kline", TagIDs: []string{"binance_spot"},
		ResultDatasetID: "ds_spot", CollectParams: `{"frequency":"1m"}`,
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
	r := h.buildReconciler()
	// 测试结束时取消它还在跑的云端操作（不等）：cloud 包的函数锁是进程内全局的，
	// 留着的操作会占住同名函数的锁，拖慢后面用同样函数名的测试。
	h.t.Cleanup(func() { r.ops().cancelAll() })
	return r
}

func (h *planHarness) buildReconciler() *PlanReconciler {
	return &PlanReconciler{
		SpaceID: "crypto", Options: testPlanOptions(),
		Deploy: PlanDeploy{CloudAccountID: testAccount, PackageName: testPackageName, Namespace: "default", MemoryMB: 64, Networks: map[string]PlanNetwork{"ap-nanjing": {VPCID: "vpc-1", SubnetID: "subnet-1"}},
			BaseEnvironment: map[string]string{"MOOX_SPACE_ID": "crypto"}},
		Groups: &GroupBuilder{Tasks: h.tasks, Symbols: h.symbols},
		Store:  h.store, Storage: h.registrar, Cloud: h.cloud, Entry: testSCFEntryEnvironment, Lateness: h.lateness, Activity: h.activity,
		Now:       func() time.Time { return h.now },
		opWaitMax: 5 * time.Second,
		sleep:     func(_ context.Context, d time.Duration) error { h.now = h.now.Add(d); return nil },
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

// applyFirstPlan 跑完第一份计划（建函数、登记、下发确认即生效），把时间推到生效点之后一分钟，断言没有未生效的计划。
func (h *planHarness) applyFirstPlan(r *PlanReconciler) {
	h.t.Helper()
	require.NoError(h.t, h.reconcile(r))
	require.Nil(h.t, h.open(), "下发确认就生效")
	change := h.appliedContent().Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(h.t, h.reconcile(r))
	require.Nil(h.t, h.open())
}

// converged 判断已生效状态里每个数据集只有一份、不是停止份，并且已到生效点。
func (h *planHarness) converged() bool {
	record, err := h.store.LatestApplied(context.Background(), "crypto")
	require.NoError(h.t, err)
	if record == nil {
		return false
	}
	for _, dataset := range h.content(record).Datasets {
		if len(dataset.Copies) != 1 || dataset.Copies[0].Stop || dataset.Copies[0].From > h.now.Unix() {
			return false
		}
	}
	return true
}

// lastRecord 返回最近建的一份计划（不论是否已生效）。
func (h *planHarness) lastRecord() *store.FetchPlanRecord {
	h.t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for i := len(h.store.records) - 1; i >= 0; i-- {
		if h.store.records[i].SpaceID == "crypto" {
			clone := *h.store.records[i]
			return &clone
		}
	}
	h.t.Fatal("还没有计划")
	return nil
}

// appliedContent 返回最近一份已生效计划的内容。
func (h *planHarness) appliedContent() *FetchPlanContent {
	h.t.Helper()
	record, err := h.store.LatestApplied(context.Background(), "crypto")
	require.NoError(h.t, err)
	require.NotNil(h.t, record)
	return h.content(record)
}

// scfRef 返回函数在假腾讯云里的位置：地域取函数名的倒数第二段，命名空间按 NamespaceFor（测试里不分片）。
func scfRef(name string) tencentscf.FunctionRef {
	parts := strings.Split(name, "-")
	region := "ap-nanjing"
	if len(parts) >= 2 {
		region = "ap-" + parts[len(parts)-2]
	}
	if strings.Contains(name, "backfill") {
		region = "ap-" + parts[len(parts)-1]
	}
	return tencentscf.FunctionRef{Region: region, Namespace: "default", FunctionName: name}
}

// opKeyOf 返回函数在操作表与读回缓存里的键（地域/命名空间/函数名，测试里命名空间是 default）。
func opKeyOf(name string) string {
	ref := scfRef(name)
	return ref.Region + "/" + ref.Namespace + "/" + name
}

// cloudFunction 返回云上一个函数的状态；没有时第二个返回值为 false。
func (h *planHarness) cloudFunction(name string) (cloudtest.Function, bool) {
	return h.scf.Lookup(scfRef(name))
}

// mustFunction 返回云上一个函数的状态，没有时测试失败。
func (h *planHarness) mustFunction(name string) cloudtest.Function {
	h.t.Helper()
	function, ok := h.cloudFunction(name)
	require.True(h.t, ok, "云上没有函数 %s", name)
	return function
}

// cloudTimers 返回云上全部定时采集函数（不含补采函数）的名字，已排序。
func (h *planHarness) cloudTimers() []string {
	var names []string
	for _, key := range h.scf.Names() {
		name := key[strings.LastIndex(key, "/")+1:]
		if !strings.Contains(name, "backfill") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// cloudPlan 解出云上函数环境里的采集计划。
func (h *planHarness) cloudPlan(name string) *marketfetch.Plan {
	h.t.Helper()
	plan, err := marketfetch.DecodePlan(h.mustFunction(name).Info.Environment[marketfetch.PlanEnvName])
	require.NoError(h.t, err, name)
	return plan
}

// timerEnabled 报告云上函数的定时器是否启用。
func (h *planHarness) timerEnabled(name string) bool {
	function := h.mustFunction(name)
	return function.Timer != nil && function.Timer.Enabled
}

// mutate 直接改云上一个函数（模拟手工改动）。
func (h *planHarness) mutate(name string, change func(*cloudtest.Function)) {
	h.scf.Mutate(scfRef(name), change)
}

// remove 直接从云上删掉一个函数（模拟手工删除）。
func (h *planHarness) remove(name string) {
	require.NoError(h.t, h.scf.DeleteFunction(context.Background(), scfRef(name)))
}

func testSCFEntryEnvironment(string) (map[string]string, error) {
	return map[string]string{"MOOX_ENTRY_ADDRESS": "storage.example:11004", "MOOX_ENTRY_HOST": "storage", "MOOX_CALLER": "scf-collector", "MOOX_CALLER_KEY_ID": "assigned-scf-key-17", "MOOX_CALLER_KEY": strings.Repeat("a", 43) + "="}, nil
}

var errTestBoom = errors.New("boom")
