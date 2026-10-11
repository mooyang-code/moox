package marketfetch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/seriesset"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	planJobKindCreate     = "create"
	planJobKindTransition = "transition"
	planJobKindFinal      = "final"
	// planFinalizeMargin 是旧份保留窗口结束后再多等的时间，让最后一批旧周期提交落地。
	planFinalizeMargin = 5 * time.Second
	// planVerifyInterval 是全量读回云端配置的间隔。
	planVerifyInterval = time.Hour
	// planGoneDebounce 是“数据集消失但任务仍启用”被视为真实删除前需要连续观察到的轮数。
	planGoneDebounce = 5
)

type planStore interface {
	Open(context.Context, string) (*store.FetchPlanRecord, error)
	LatestApplied(context.Context, string) (*store.FetchPlanRecord, error)
	Create(context.Context, string, string, string) (*store.FetchPlanRecord, error)
	Update(context.Context, int64, string, string, string) error
	SavePlan(context.Context, int64, string) error
}

type planCloud interface {
	ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error)
	SubmitCreateNodes(context.Context, string, []*cloudnodepb.NodeCreateItem) (string, error)
	SubmitRuntimeConfigs(context.Context, string, []*cloudnodepb.NodeRuntimeConfigPatch) (string, error)
	GetBatchResults(context.Context, string, string) (*cloudnodepb.NodeBatchSummary, []scfinvoker.BatchItemResult, error)
	AcquireCollectorPublishLease(context.Context, string, string) (*scfinvoker.CollectorPublishLease, error)
	RenewCollectorPublishLease(context.Context, *scfinvoker.CollectorPublishLease) (*scfinvoker.CollectorPublishLease, error)
	ReleaseCollectorPublishLease(context.Context, *scfinvoker.CollectorPublishLease) error
}

// PlanSnapshotRegistrar 把数据集标的快照登记到 Storage。
type PlanSnapshotRegistrar interface {
	RegisterSeriesSnapshot(ctx context.Context, spaceID, datasetID, frequency string, series []seriesset.Series, effectiveFrom int64) (marketstorage.SnapshotRegistration, error)
}

// PlanLatenessSetter 保证数据集属性 allowed_lateness_seconds 与任务设置一致。
type PlanLatenessSetter interface {
	EnsureAllowedLateness(ctx context.Context, spaceID, datasetID string, seconds int) error
}

// ActivityEntry 是写入全局活动日志的一条记录。
type ActivityEntry struct {
	SpaceID  string
	Source   string
	Category string
	Level    string
	Title    string
	Detail   string
	RefID    string
	DedupKey string
}

// ActivityLogger 写入全局活动日志；相同 DedupKey 的重放必须被忽略。
type ActivityLogger interface {
	Append(ctx context.Context, entries []ActivityEntry) error
}

// PlanDeploy 是创建与配置函数所需的部署参数。
type PlanDeploy struct {
	CloudAccountID string
	PackageID      string
	Namespace      string
	MemoryMB       int
	// BaseEnvironment 是函数的非受管环境（空间、Storage 应用密钥、日志服务等）。
	BaseEnvironment map[string]string
	Networks        map[string]PlanNetwork
}

type pendingPlanJob struct {
	id        string
	kind      string
	functions []string
}

// PlanReconciler 是一个空间的采集计划对账器：由任务与标签成员算出期望状态，
// 与已生效的计划比较，有变化时先保存采集计划，再登记快照、下发环境、收尾。
type PlanReconciler struct {
	SpaceID  string
	Options  PlanOptions
	Deploy   PlanDeploy
	Groups   *Reconciler
	Store    planStore
	Storage  PlanSnapshotRegistrar
	Cloud    planCloud
	Access   func(region string) (map[string]string, error)
	Lateness PlanLatenessSetter
	Activity ActivityLogger
	Metrics  *Metrics
	Now      func() time.Time
	// Operations 限制同时进行的 SCF 配置操作（各空间共用）；为空表示不限制。
	Operations chan struct{}
	// JobWait 与 PollInterval 控制等待一个云端作业的时间。
	JobWait      time.Duration
	PollInterval time.Duration

	mu           sync.Mutex
	lease        *scfinvoker.CollectorPublishLease
	holder       string
	pending      []pendingPlanJob
	lastVerified time.Time
	goneSeen     map[string]int
}

func (r *PlanReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *PlanReconciler) fail(reason string, err error) error {
	if r.Metrics != nil {
		r.Metrics.ObserveAssignmentFailure(r.SpaceID, reason)
	}
	return err
}

func (r *PlanReconciler) acquireOperation(ctx context.Context) (func(), error) {
	if r.Operations == nil {
		return func() {}, nil
	}
	select {
	case r.Operations <- struct{}{}:
		return func() { <-r.Operations }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Reconcile 执行一轮对账。上一轮未结束时本轮直接跳过。
func (r *PlanReconciler) Reconcile(ctx context.Context) error {
	if r == nil || r.Store == nil || r.Cloud == nil || r.Groups == nil || r.Storage == nil {
		return errors.New("plan reconciler is not initialized")
	}
	if !r.mu.TryLock() {
		return errors.New("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	open, err := r.Store.Open(ctx, r.SpaceID)
	if err != nil {
		return r.fail("store", fmt.Errorf("read open fetch plan: %w", err))
	}
	if open != nil {
		return r.advance(ctx, open)
	}
	return r.planChange(ctx)
}

// managedTasks 返回由本对账器管理的 kline 任务的目标数据集集合。
func (r *PlanReconciler) managedTasks(ctx context.Context) (map[string]struct{}, error) {
	tasks, err := r.Groups.Tasks.ListEnabled(ctx, r.SpaceID)
	if err != nil {
		return nil, fmt.Errorf("list enabled tasks: %w", err)
	}
	datasets := map[string]struct{}{}
	for _, task := range tasks {
		params, parseErr := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
		if parseErr != nil || params.Collector.DataType != "kline" || !params.TimerManaged() {
			continue
		}
		datasets[params.Target.DatasetID] = struct{}{}
	}
	return datasets, nil
}

func (r *PlanReconciler) planChange(ctx context.Context) error {
	var previousFunctions map[string]*PlanFunction
	var previousDatasets map[string]*PlanDatasetState
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil {
		return r.fail("store", fmt.Errorf("read applied fetch plan: %w", err))
	}
	if applied != nil {
		content, decodeErr := decodeFetchPlanContent(applied.Plan)
		if decodeErr != nil {
			return r.fail("store", decodeErr)
		}
		previousFunctions, previousDatasets = content.Applied()
	}
	groups, err := r.Groups.groups(ctx, r.SpaceID)
	if err != nil {
		return r.fail("rules", err)
	}
	managed, err := r.managedTasks(ctx)
	if err != nil {
		return r.fail("rules", err)
	}
	if len(groups) == 0 && len(managed) > 0 {
		// 任务启用但标的暂时取不到：属于临时数据状况，保持现状，不能据此停掉全部函数。
		log.WarnContextf(ctx, "collector fetch plan kept current state space=%s: enabled tasks have no usable kline groups", r.SpaceID)
		return nil
	}
	desired, err := BuildDesiredState(r.Options, groups, previousFunctions)
	if err != nil {
		return r.fail("rules", err)
	}
	if r.holdMissingDatasets(ctx, desired, previousDatasets, managed) {
		return nil
	}
	choose := func(frequency string, startAt int64, isNew bool) (int64, error) {
		return ChoosePeriodStart(frequency, r.now(), startAt, isNew)
	}
	content, changed, err := BuildPlanContent(previousFunctions, previousDatasets, desired, choose)
	if err != nil {
		return r.fail("rules", err)
	}
	if !changed {
		return r.verifyFleet(ctx, previousFunctions)
	}
	if err := r.validateContent(content); err != nil {
		return r.fail("capacity", err)
	}
	if r.Lateness != nil {
		for _, change := range content.Datasets {
			if change.Gone {
				continue
			}
			if err := r.Lateness.EnsureAllowedLateness(ctx, r.SpaceID, change.DatasetID, change.LatenessS); err != nil {
				return r.fail("dataset", fmt.Errorf("set allowed lateness of dataset %s: %w", change.DatasetID, err))
			}
		}
	}
	record, err := r.Store.Create(ctx, r.SpaceID, mustJSON(content), mustJSON(&FetchPlanProgress{}))
	if errors.Is(err, store.ErrFetchPlanOpen) {
		return nil
	}
	if err != nil {
		return r.fail("store", fmt.Errorf("save fetch plan: %w", err))
	}
	log.InfoContextf(ctx, "collector fetch plan created space=%s plan=%d datasets=%d functions=%d", r.SpaceID, record.ID, len(content.Datasets), len(content.Functions))
	return r.advance(ctx, record)
}

// holdMissingDatasets 对“数据集从期望状态中消失、但对应任务仍启用”做防抖：
// 这通常是标签或元数据暂时取不到，连续多轮仍消失才当作真实变更。
func (r *PlanReconciler) holdMissingDatasets(ctx context.Context, desired *DesiredState, previous map[string]*PlanDatasetState, managed map[string]struct{}) bool {
	if r.goneSeen == nil {
		r.goneSeen = map[string]int{}
	}
	hold := false
	seen := map[string]struct{}{}
	for key, state := range previous {
		if desired.Datasets[key] != nil {
			continue
		}
		if _, stillEnabled := managed[state.DatasetID]; !stillEnabled {
			continue
		}
		seen[key] = struct{}{}
		r.goneSeen[key]++
		if r.goneSeen[key] < planGoneDebounce {
			hold = true
			log.WarnContextf(ctx, "collector fetch plan holds dataset removal space=%s dataset=%s seen=%d", r.SpaceID, key, r.goneSeen[key])
		}
	}
	for key := range r.goneSeen {
		if _, ok := seen[key]; !ok {
			delete(r.goneSeen, key)
		}
	}
	return hold
}

// functionEnvironment 构造受管环境：采集计划、Access 入口、超时。
func (r *PlanReconciler) functionEnvironment(region string, plan *Plan) (map[string]string, error) {
	encoded, err := EncodePlan(plan)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(encoded))
	count := 0
	for _, dataset := range plan.Datasets {
		count += len(dataset.Copies[len(dataset.Copies)-1].Subjects)
	}
	environment := map[string]string{
		PlanEnvName:                         encoded,
		"MOOX_MARKET_FETCH_ASSIGNMENT_HASH": hex.EncodeToString(sum[:8]),
		"MOOX_MARKET_FETCH_SUBJECT_COUNT":   strconv.Itoa(count),
		"MOOX_FETCH_TIMEOUT_SECONDS":        strconv.Itoa(tencent.CollectorTimerTimeoutSeconds),
	}
	if r.Access == nil {
		return nil, errors.New("SCF Access environment is required")
	}
	access, err := r.Access(region)
	if err != nil {
		return nil, err
	}
	if err := tencent.ValidateCollectorAccessEnvironment(access); err != nil {
		return nil, err
	}
	for _, key := range []string{"MOOX_ACCESS_ADDRESS", "MOOX_ACCESS_ID", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY", "MOOX_GATEWAY_COMPRESSION"} {
		if value, ok := access[key]; ok {
			environment[key] = value
		}
	}
	return environment, nil
}

// validateContent 在保存计划前确认每个函数的过渡与最终环境都能放进 SCF 环境变量上限。
func (r *PlanReconciler) validateContent(content *FetchPlanContent) error {
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if !function.Push {
			continue
		}
		for _, plan := range []*Plan{content.TransitionalPlan(function), content.FinalPlan(function)} {
			if plan == nil {
				continue
			}
			environment, err := r.functionEnvironment(function.Region, plan)
			if err != nil {
				return fmt.Errorf("function %s: %w", name, err)
			}
			merged := map[string]string{"MOOX_CODE_PACKAGE_ID": r.Deploy.PackageID, "MOOX_CREATE_RESERVATION_ID": "00000000-0000-0000-0000-000000000000"}
			for key, value := range r.Deploy.BaseEnvironment {
				merged[key] = value
			}
			for key, value := range environment {
				merged[key] = value
			}
			if size := tencent.SCFEnvironmentBytes(merged); size > maxSCFEnvironmentBytes {
				return fmt.Errorf("function %s environment is %d bytes; reduce symbols_per_function", name, size)
			}
		}
	}
	return nil
}

// maxSCFEnvironmentBytes 是 SCF 函数环境变量的总上限。
const maxSCFEnvironmentBytes = 4096

func mustStruct(values map[string]any) *structpb.Struct {
	result, err := structpb.NewStruct(values)
	if err != nil {
		panic(err)
	}
	return result
}

func (r *PlanReconciler) saveProgress(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress, status, lastError string) error {
	if err := r.Store.Update(ctx, record.ID, status, mustJSON(progress), lastError); err != nil {
		return r.fail("store", fmt.Errorf("save fetch plan progress: %w", err))
	}
	record.Status, record.Progress, record.LastError = status, mustJSON(progress), lastError
	return nil
}

// advance 续跑一份未生效的采集计划。
func (r *PlanReconciler) advance(ctx context.Context, record *store.FetchPlanRecord) error {
	content, err := decodeFetchPlanContent(record.Plan)
	if err != nil {
		return r.fail("store", err)
	}
	progress, err := decodeFetchPlanProgress(record.Progress)
	if err != nil {
		return r.fail("store", err)
	}
	if blocked, err := r.settlePending(ctx, record, progress); blocked || err != nil {
		return err
	}
	if !progress.LoggedStart {
		if r.logPlan(ctx, record, content, false) == nil {
			progress.LoggedStart = true
			if err := r.saveProgress(ctx, record, progress, record.Status, record.LastError); err != nil {
				return err
			}
		}
	}
	ready, err := r.registerSnapshots(ctx, record, content, progress)
	if err != nil || !ready {
		return err
	}
	if record.Status == store.FetchPlanPending {
		if err := r.saveProgress(ctx, record, progress, store.FetchPlanApplying, ""); err != nil {
			return err
		}
	}
	done, err := r.pushFunctions(ctx, record, content, progress, false)
	if err != nil || !done {
		return err
	}
	if end := content.WindowEnd(); !end.IsZero() && r.now().Before(end.Add(planFinalizeMargin)) {
		return nil
	}
	done, err = r.pushFunctions(ctx, record, content, progress, true)
	if err != nil || !done {
		return err
	}
	if !progress.LoggedApply {
		if r.logPlan(ctx, record, content, true) == nil {
			progress.LoggedApply = true
			if err := r.saveProgress(ctx, record, progress, record.Status, ""); err != nil {
				return err
			}
		}
	}
	if !progress.LoggedStart || !progress.LoggedApply {
		return r.fail("activity", errors.New("activity log is not written yet; fetch plan stays open"))
	}
	if err := r.saveProgress(ctx, record, progress, store.FetchPlanApplied, ""); err != nil {
		return err
	}
	r.lastVerified = r.now()
	log.InfoContextf(ctx, "collector fetch plan applied space=%s plan=%d", r.SpaceID, record.ID)
	return nil
}

// registerSnapshots 为 hash 变化的数据集登记快照；只有 Storage 明确拒绝“P 已过期”时才重选 P。
func (r *PlanReconciler) registerSnapshots(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress) (bool, error) {
	keys := make([]string, 0, len(content.Datasets))
	for key, change := range content.Datasets {
		if change.HashChanged && !progress.Registered[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		change := content.Datasets[key]
		registration, err := r.Storage.RegisterSeriesSnapshot(ctx, r.SpaceID, change.DatasetID, change.Frequency, change.Series, change.P)
		switch {
		case errors.Is(err, marketstorage.ErrStaleEffectiveFrom):
			p, chooseErr := ChoosePeriodStart(change.Frequency, r.now(), 0, false)
			if chooseErr != nil {
				return false, r.fail("storage", chooseErr)
			}
			change.P = p
			if saveErr := r.Store.SavePlan(ctx, record.ID, mustJSON(content)); saveErr != nil {
				return false, r.fail("store", saveErr)
			}
			record.Plan = mustJSON(content)
			log.WarnContextf(ctx, "collector fetch plan reselected period space=%s dataset=%s p=%d", r.SpaceID, key, p)
			return false, nil
		case err != nil:
			// 结果不明或暂时失败：下一轮用同一份请求重放。
			_ = r.saveProgress(ctx, record, progress, record.Status, err.Error())
			return false, r.fail("storage", fmt.Errorf("register series snapshot %s: %w", key, err))
		case registration.Hash != change.Hash || registration.Count != change.Count:
			return false, r.fail("storage", fmt.Errorf("Storage computed hash %s/%d for %s, Collector computed %s/%d", registration.Hash, registration.Count, key, change.Hash, change.Count))
		}
		progress.mark(&progress.Registered, key)
		if err := r.saveProgress(ctx, record, progress, record.Status, ""); err != nil {
			return false, err
		}
	}
	return true, nil
}

// pushFunctions 创建缺失的函数并下发过渡（或最终）环境；全部确认后返回 true。
func (r *PlanReconciler) pushFunctions(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, final bool) (bool, error) {
	confirmed := &progress.Transitioned
	kind := planJobKindTransition
	if final {
		confirmed, kind = &progress.Finalized, planJobKindFinal
	}
	var todo []string
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if (*confirmed)[name] {
			continue
		}
		if !final && !function.Push {
			continue
		}
		if final && !r.finalDiffers(content, function) {
			progress.mark(confirmed, name)
			continue
		}
		todo = append(todo, name)
	}
	if len(todo) == 0 {
		return true, nil
	}
	if !final {
		created, err := r.ensureCreated(ctx, record, content, progress, todo)
		if err != nil || !created {
			return false, err
		}
	}
	patches := make([]*cloudnodepb.NodeRuntimeConfigPatch, 0, len(todo))
	for _, name := range todo {
		function := content.Functions[name]
		plan, cron := content.TransitionalPlan(function), content.TransitionalCron(function)
		if final {
			plan, cron = content.FinalPlan(function), content.FinalCron(function)
			if plan == nil {
				// 退役：保持过渡环境不变，只停用定时器。
				plan, cron = content.TransitionalPlan(function), ""
			}
		}
		environment, err := r.functionEnvironment(function.Region, plan)
		if err != nil {
			return false, r.fail("environment", fmt.Errorf("function %s: %w", name, err))
		}
		patches = append(patches, &cloudnodepb.NodeRuntimeConfigPatch{NodeId: name, ManagedEnvironment: environment, TimerEnabled: cron != "", TimerCron: cron})
	}
	if err := r.submit(ctx, record, progress, kind, todo, func(lease *scfinvoker.CollectorPublishLease) (string, error) {
		for _, patch := range patches {
			patch.CollectorPublishLeaseId, patch.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
		}
		return r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, patches)
	}); err != nil {
		return false, err
	}
	for _, name := range todo {
		if !(*confirmed)[name] {
			return false, nil
		}
	}
	return true, nil
}

// finalDiffers 判断收尾时该函数是否还需要再下发一次。
func (r *PlanReconciler) finalDiffers(content *FetchPlanContent, function *PlanFunctionChange) bool {
	if !function.Push {
		return false
	}
	if function.New == nil {
		return true
	}
	return !plansEqual(content.TransitionalPlan(function), content.FinalPlan(function)) || content.TransitionalCron(function) != content.FinalCron(function)
}

// ensureCreated 创建尚不存在的函数。返回 true 表示所有待下发函数都已存在。
func (r *PlanReconciler) ensureCreated(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, todo []string) (bool, error) {
	var missing []string
	for _, name := range todo {
		if content.Functions[name].Create && !progress.Created[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return true, nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return false, err
	}
	nodes, err := r.Cloud.ListTimerMarketFetchers(ctx, r.SpaceID)
	release()
	if err != nil {
		return false, r.fail("cloudnode", fmt.Errorf("list market fetchers: %w", err))
	}
	existing := map[string]struct{}{}
	for _, node := range nodes {
		existing[node.NodeID] = struct{}{}
	}
	var items []*cloudnodepb.NodeCreateItem
	var names []string
	for _, name := range missing {
		if _, ok := existing[name]; ok {
			progress.mark(&progress.Created, name)
			continue
		}
		function := content.Functions[name]
		item, err := r.createItem(record, content, function)
		if err != nil {
			return false, r.fail("environment", fmt.Errorf("function %s: %w", name, err))
		}
		items = append(items, item)
		names = append(names, name)
	}
	if len(items) == 0 {
		return true, r.saveProgress(ctx, record, progress, record.Status, "")
	}
	if err := r.submit(ctx, record, progress, planJobKindCreate, names, func(lease *scfinvoker.CollectorPublishLease) (string, error) {
		for _, item := range items {
			item.CollectorPublishLeaseId, item.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
		}
		return r.Cloud.SubmitCreateNodes(ctx, r.SpaceID, items)
	}); err != nil {
		return false, err
	}
	for _, name := range names {
		if !progress.Created[name] {
			return false, nil
		}
	}
	return true, nil
}

func (r *PlanReconciler) createItem(record *store.FetchPlanRecord, content *FetchPlanContent, function *PlanFunctionChange) (*cloudnodepb.NodeCreateItem, error) {
	network, ok := r.Deploy.Networks[function.Region]
	if !ok || network.VPCID == "" || network.SubnetID == "" {
		return nil, fmt.Errorf("region %s has no VPC and subnet configured", function.Region)
	}
	environment, err := r.functionEnvironment(function.Region, content.TransitionalPlan(function))
	if err != nil {
		return nil, err
	}
	for key, value := range r.Deploy.BaseEnvironment {
		if _, managed := environment[key]; !managed {
			environment[key] = value
		}
	}
	memory := r.Deploy.MemoryMB
	if memory <= 0 {
		memory = 64
	}
	timeout := strconv.Itoa(tencent.CollectorTimerTimeoutSeconds)
	reservation := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", r.SpaceID, record.ID, function.Name)))
	reservationID := hex.EncodeToString(reservation[:16])
	return &cloudnodepb.NodeCreateItem{
		CloudAccountId: r.Deploy.CloudAccountID, NodeType: "scf-event", TriggerType: "timer", Runtime: "Go1", Handler: "main",
		Namespace: r.Deploy.Namespace, Region: function.Region, PackageId: r.Deploy.PackageID, Environment: environment,
		Config: map[string]string{
			"timeout": timeout, "memory_size": strconv.Itoa(memory), "max_instance_concurrency": "1",
			"vpc_id": network.VPCID, "subnet_id": network.SubnetID, "public_net_status": "ENABLE",
		},
		Metadata:            mustStruct(map[string]any{"function_name": function.Name, "node_id": function.Name, "biz_type": "market_fetcher", "function_mode": "kline", "memory_size": memory, "timeout_seconds": tencent.CollectorTimerTimeoutSeconds}),
		CreateReservationId: reservationID,
	}, nil
}

// submit 获取发布租约、提交一个云端作业并等待结果；成功的函数记入 progress。
func (r *PlanReconciler) submit(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress, kind string, names []string, send func(*scfinvoker.CollectorPublishLease) (string, error)) error {
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return r.fail("publish_lease", fmt.Errorf("publish lease: %w", err))
	}
	if len(names) > runtimeConfigBatchSize {
		return r.fail("capacity", fmt.Errorf("%d functions exceed the batch limit %d", len(names), runtimeConfigBatchSize))
	}
	jobID, sendErr := send(lease)
	if sendErr != nil {
		_ = r.saveProgress(ctx, record, progress, record.Status, sendErr.Error())
		return r.fail("cloudnode", fmt.Errorf("submit %s: %w", kind, sendErr))
	}
	job := pendingPlanJob{id: jobID, kind: kind, functions: names}
	items, terminal, err := r.waitJob(ctx, jobID)
	if err != nil {
		r.pending = append(r.pending, job)
		return r.fail("cloudnode", err)
	}
	if !terminal {
		r.pending = append(r.pending, job)
		return nil
	}
	if err := r.applyResults(ctx, record, progress, job, items); err != nil {
		return err
	}
	r.releaseLease(ctx)
	return nil
}

func (r *PlanReconciler) applyResults(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress, job pendingPlanJob, items []scfinvoker.BatchItemResult) error {
	var failures []string
	succeeded := map[string]bool{}
	for _, item := range items {
		switch {
		case item.Success:
			succeeded[item.NodeID] = true
		case item.Failed:
			failures = append(failures, item.NodeID+": "+item.Message)
		}
	}
	set := &progress.Transitioned
	switch job.kind {
	case planJobKindCreate:
		set = &progress.Created
	case planJobKindFinal:
		set = &progress.Finalized
	}
	for _, name := range job.functions {
		if succeeded[name] {
			progress.mark(set, name)
		}
	}
	if len(failures) == 0 {
		for _, name := range job.functions {
			if !succeeded[name] {
				failures = append(failures, name+": no result reported")
			}
		}
	}
	lastError := strings.Join(failures, "; ")
	if err := r.saveProgress(ctx, record, progress, record.Status, lastError); err != nil {
		return err
	}
	if lastError != "" {
		return r.fail("cloudnode", fmt.Errorf("%s job reported failures: %s", job.kind, lastError))
	}
	return nil
}

// settlePending 在新一轮开始时先处理上一轮没等完的云端作业。blocked 为 true 表示作业仍在运行，本轮不继续。
func (r *PlanReconciler) settlePending(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress) (bool, error) {
	if len(r.pending) == 0 {
		return false, nil
	}
	pending := r.pending
	r.pending = nil
	var firstErr error
	for index, job := range pending {
		items, terminal, err := r.checkJob(ctx, job.id)
		if err != nil {
			r.pending = append(r.pending, pending[index:]...)
			return true, r.fail("cloudnode", err)
		}
		if !terminal {
			r.pending = append(r.pending, pending[index:]...)
			if renewErr := r.renewLease(ctx); renewErr != nil {
				return true, r.fail("publish_lease", renewErr)
			}
			return true, nil
		}
		if err := r.applyResults(ctx, record, progress, job, items); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	r.releaseLease(ctx)
	return false, firstErr
}

func (r *PlanReconciler) checkJob(ctx context.Context, jobID string) ([]scfinvoker.BatchItemResult, bool, error) {
	summary, items, err := r.Cloud.GetBatchResults(ctx, r.SpaceID, jobID)
	if err != nil {
		return nil, false, err
	}
	switch summary.GetStatus() {
	case cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS, cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED, cloudnodepb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL:
		return items, true, nil
	default:
		return nil, false, nil
	}
}

// waitJob 轮询云端作业直到结束或等待时间用完。
func (r *PlanReconciler) waitJob(ctx context.Context, jobID string) ([]scfinvoker.BatchItemResult, bool, error) {
	wait, interval := r.JobWait, r.PollInterval
	if wait <= 0 {
		wait = 90 * time.Second
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		items, terminal, err := r.checkJob(ctx, jobID)
		if err != nil || terminal {
			return items, terminal, err
		}
		if !time.Now().Add(interval).Before(deadline) {
			return nil, false, nil
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}

func (r *PlanReconciler) ensureLease(ctx context.Context) (*scfinvoker.CollectorPublishLease, error) {
	if r.lease != nil {
		if err := r.renewLease(ctx); err != nil {
			return nil, err
		}
		if r.lease != nil {
			return r.lease, nil
		}
	}
	if r.holder == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		r.holder = "plan-reconciler-" + hex.EncodeToString(raw[:])
	}
	leaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	lease, err := r.Cloud.AcquireCollectorPublishLease(leaseCtx, r.SpaceID, r.holder)
	if err != nil {
		return nil, err
	}
	if lease == nil || lease.LeaseID == "" || lease.FencingToken < 1 || !strings.EqualFold(lease.SpaceID, r.SpaceID) {
		return nil, errors.New("publish lease response is incomplete")
	}
	r.lease = lease
	return lease, nil
}

func (r *PlanReconciler) renewLease(ctx context.Context) error {
	if r.lease == nil {
		return nil
	}
	leaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	renewed, err := r.Cloud.RenewCollectorPublishLease(leaseCtx, r.lease)
	if err != nil {
		if errors.Is(err, scfinvoker.ErrCollectorPublishLeaseStale) {
			r.lease = nil
			return nil
		}
		return err
	}
	if renewed != nil {
		r.lease = renewed
	}
	return nil
}

func (r *PlanReconciler) releaseLease(ctx context.Context) {
	if r.lease == nil || len(r.pending) > 0 {
		return
	}
	leaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.Cloud.ReleaseCollectorPublishLease(leaseCtx, r.lease); err != nil {
		log.WarnContextf(ctx, "collector fetch plan release publish lease space=%s: %v", r.SpaceID, err)
		return
	}
	r.lease = nil
}

// verifyFleet 全量读回云端函数配置：被手工改动或缺失的函数按已生效计划补推。
func (r *PlanReconciler) verifyFleet(ctx context.Context, applied map[string]*PlanFunction) error {
	if len(applied) == 0 || (!r.lastVerified.IsZero() && r.now().Sub(r.lastVerified) < planVerifyInterval) {
		return nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	nodes, err := r.Cloud.ListTimerMarketFetchers(ctx, r.SpaceID)
	release()
	if err != nil {
		return r.fail("cloudnode", fmt.Errorf("list market fetchers: %w", err))
	}
	byName := map[string]scfinvoker.Node{}
	for _, node := range nodes {
		byName[node.NodeID] = node
	}
	var drifted []string
	patches := map[string]*cloudnodepb.NodeRuntimeConfigPatch{}
	for name, function := range applied {
		environment, envErr := r.functionEnvironment(function.Region, function.Plan)
		if envErr != nil {
			return r.fail("environment", fmt.Errorf("function %s: %w", name, envErr))
		}
		node, exists := byName[name]
		if exists && fmt.Sprint(node.Metadata["assignment_hash"]) == environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] && !timerNeedsRepair(node.Metadata, function.Cron) {
			continue
		}
		if !exists {
			log.WarnContextf(ctx, "collector fetch plan function is missing in the cloud space=%s function=%s", r.SpaceID, name)
			continue
		}
		drifted = append(drifted, name)
		patches[name] = &cloudnodepb.NodeRuntimeConfigPatch{NodeId: name, ManagedEnvironment: environment, TimerEnabled: function.Cron != "", TimerCron: function.Cron}
	}
	if len(drifted) == 0 {
		r.lastVerified = r.now()
		return nil
	}
	sort.Strings(drifted)
	log.WarnContextf(ctx, "collector fetch plan repairing drifted functions space=%s functions=%s", r.SpaceID, strings.Join(drifted, ","))
	release, err = r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return r.fail("publish_lease", err)
	}
	list := make([]*cloudnodepb.NodeRuntimeConfigPatch, 0, len(drifted))
	for _, name := range drifted {
		patch := patches[name]
		patch.CollectorPublishLeaseId, patch.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
		list = append(list, patch)
	}
	jobID, err := r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, list)
	if err != nil {
		return r.fail("cloudnode", err)
	}
	_, terminal, err := r.waitJob(ctx, jobID)
	if err != nil {
		return r.fail("cloudnode", err)
	}
	if terminal {
		r.releaseLease(ctx)
		r.lastVerified = r.now()
	}
	return nil
}

// timerNeedsRepair 判断云端读回的定时器是否与期望一致。
func timerNeedsRepair(metadata map[string]any, cron string) bool {
	enabled, hasEnabled := metadataBoolValue(metadata, "timer_actual_enabled")
	if !hasEnabled || enabled != (cron != "") {
		return true
	}
	if cron == "" {
		return false
	}
	return !strings.EqualFold(fmt.Sprint(metadata["timer_available_status"]), "available") || strings.TrimSpace(fmt.Sprint(metadata["timer_actual_cron"])) != cron
}

// logPlan 写入活动日志：计划创建（标的变更）与计划生效。写入失败返回错误，下一轮重试；幂等键保证不重复。
func (r *PlanReconciler) logPlan(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, applied bool) error {
	if r.Activity == nil {
		return nil
	}
	var entries []ActivityEntry
	refID := strconv.FormatInt(record.ID, 10)
	if !applied {
		keys := make([]string, 0, len(content.Datasets))
		for key, change := range content.Datasets {
			if change.Changed {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			change := content.Datasets[key]
			detail := map[string]any{
				"dataset_id": change.DatasetID, "frequency": change.Frequency, "old_hash": change.OldHash, "new_hash": change.Hash,
				"effective_period": time.Unix(change.P, 0).UTC().Format(time.RFC3339), "added": change.Added, "removed": change.Removed, "count": change.Count,
			}
			entries = append(entries, ActivityEntry{
				SpaceID: r.SpaceID, Source: "collector", Category: "collector.subject_change", Level: "info", RefID: refID,
				Title:    fmt.Sprintf("%s %s：新增 %d 个、移除 %d 个标的，将于 %s 生效", change.DatasetID, change.Frequency, len(change.Added), len(change.Removed), time.Unix(change.P, 0).UTC().Format("2006-01-02 15:04")),
				Detail:   mustJSON(detail),
				DedupKey: fmt.Sprintf("collector.fetch_plan.%s.%d.created.%s", r.SpaceID, record.ID, key),
			})
		}
	} else {
		entries = append(entries, ActivityEntry{
			SpaceID: r.SpaceID, Source: "collector", Category: "collector.fetch_plan_applied", Level: "info", RefID: refID,
			Title: fmt.Sprintf("采集计划 %d 已生效", record.ID), Detail: "{}", DedupKey: fmt.Sprintf("collector.fetch_plan.%s.%d.applied", r.SpaceID, record.ID),
		})
	}
	if len(entries) == 0 {
		return nil
	}
	return r.Activity.Append(ctx, entries)
}
