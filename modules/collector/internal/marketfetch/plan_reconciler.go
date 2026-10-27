package marketfetch

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
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
	// planUpgradeCheckInterval 是检查函数代码包是否落后的间隔。
	planUpgradeCheckInterval = 2 * time.Minute
	// planUpgradeBudget 是单轮代码升级的时间上限，超出后放弃本轮，留给计划推进。
	planUpgradeBudget = 90 * time.Second
	// planVerifyInterval 是全量读回云端配置的间隔；读回不完整时按 planVerifyRetry 重试。
	planVerifyInterval = time.Hour
	planVerifyRetry    = 5 * time.Minute
	// planGoneDebounce 是“数据集消失但任务仍启用”被视为真实删除前需要连续观察到的轮数。
	planGoneDebounce = 5
	// planLeadTimeHandoff 是采集方式交接时 P 的最短提前量。
	planLeadTimeHandoff = 3 * time.Minute
)

// IngestModeSource 是按数据集的采集方式（store.IngestModeRepository）。
type IngestModeSource interface {
	List(context.Context, string) (map[string]store.IngestMode, error)
	Initialize(context.Context, []store.IngestMode) error
	ClearForce(context.Context, string, []string) error
}

// SubjectExclusionSource 读取采集名单排除（store.SubjectExclusionRepository）。
type SubjectExclusionSource interface {
	Active(context.Context, string, time.Time) ([]store.SubjectExclusion, error)
}

// StreamWorkerSource 读取 collector-stream 的心跳（store.StreamWorkerRepository）。
type StreamWorkerSource interface {
	Get(context.Context, string) (*store.StreamWorker, error)
}

// StreamApplied 是 collector-stream 已应用的计划版本（t_collector_stream_workers.c_applied）。
type StreamApplied struct {
	ID    int64  `json:"id"`
	Stage string `json:"stage"`
}

type planStore interface {
	Open(context.Context, string) (*store.FetchPlanRecord, error)
	LatestApplied(context.Context, string) (*store.FetchPlanRecord, error)
	Create(context.Context, string, string, string) (*store.FetchPlanRecord, error)
	Update(context.Context, int64, string, string, string) error
	SavePlan(context.Context, int64, string) error
}

type planCloud interface {
	LatestPackage(context.Context, string, string) (string, error)
	ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.FunctionInstance, error)
	ListTimerMarketFetchersFresh(context.Context, string) ([]scfinvoker.FunctionInstance, error)
	ListMarketFetchers(context.Context, string) ([]scfinvoker.FunctionInstance, error)
	ListUnreadyMarketFetchers(context.Context, string, string) ([]scfinvoker.FunctionInstance, error)
	SubmitDeleteFunctionInstances(context.Context, string, []string, *scfinvoker.CollectorPublishLease) (string, error)
	SubmitDeployFunctionInstances(context.Context, string, []string, string, *scfinvoker.CollectorPublishLease) (string, error)
	SubmitCreateFunctionInstances(context.Context, string, []*cloudfunctionpb.FunctionInstanceCreateItem) (string, error)
	SubmitRuntimeConfigs(context.Context, string, []*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch) (string, error)
	GetBatchResults(context.Context, string, string) (*cloudfunctionpb.FunctionJobSummary, []scfinvoker.BatchItemResult, error)
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
type ActivityEntry = scfinvoker.ActivityEntry

// ActivityLogger 写入全局活动日志；相同 DedupKey 的重放必须被忽略。
type ActivityLogger interface {
	AppendActivityLogs(ctx context.Context, spaceID string, entries []ActivityEntry) error
}

// PlanDeploy 是创建与配置函数所需的部署参数。
type PlanDeploy struct {
	CloudAccountID string
	// PackageID 固定使用某个代码包；PackageName 非空时每轮解析该名称的最新可用代码包（优先于 PackageID），
	// 上传新版本后函数自动升级，不需要改配置。
	PackageID   string
	PackageName string
	Namespace   string
	MemoryMB    int
	// BaseEnvironment 是函数的非受管环境（空间、Storage 应用密钥、日志服务等）。
	BaseEnvironment map[string]string
	Networks        map[string]PlanNetwork
	// NamespaceCapacity 为每个命名空间的函数数上限（0 表示不分片）。
	NamespaceCapacity int
}

// pendingJobWarnAge 是云端作业的告警年龄：超过后仍须等到终态，不会因年龄丢弃而重复下发。
const pendingJobWarnAge = 10 * time.Minute

// NamespaceFor 返回函数所在的命名空间：每个命名空间最多 NamespaceCapacity 个函数，按函数名末尾的序号分片；
// 第一片使用配置的命名空间名，之后依次加 -2、-3……
func (d PlanDeploy) NamespaceFor(functionName string) string {
	capacity := d.NamespaceCapacity
	if capacity <= 0 {
		return d.Namespace
	}
	index := 0
	if cut := strings.LastIndex(functionName, "-"); cut >= 0 {
		index, _ = strconv.Atoi(functionName[cut+1:])
	}
	if shard := index / capacity; shard > 0 {
		return fmt.Sprintf("%s-%d", d.Namespace, shard+1)
	}
	return d.Namespace
}

type pendingPlanJob struct {
	at        time.Time
	id        string
	kind      string
	functions []string
	warned    bool // 超龄告警只发一次
}

// PlanReconciler 是一个空间的采集计划对账器：由任务与标签成员算出期望状态，
// 与已生效的计划比较，有变化时先保存采集计划，再登记快照、下发环境、收尾。
type PlanReconciler struct {
	SpaceID  string
	Options  PlanOptions
	Deploy   PlanDeploy
	Groups   *GroupBuilder
	Store    planStore
	Storage  PlanSnapshotRegistrar
	Cloud    planCloud
	Access   func(region string) (map[string]string, error)
	Lateness PlanLatenessSetter
	Activity ActivityLogger
	Metrics  *Metrics
	Now      func() time.Time
	// Modes 是按数据集的采集方式；DefaultMode 是新数据集的初值（空间配置的 default_ingest、default_executor）。
	Modes       IngestModeSource
	DefaultMode DatasetMode
	// Workers 读取推送与本地执行端（collector-stream）的确认。
	Workers StreamWorkerSource
	// Exclusions 是采集名单排除：期望状态去掉排除的标的；为空表示没有排除。
	Exclusions SubjectExclusionSource
	// Operations 限制同时进行的 SCF 配置操作（各空间共用）；为空表示不限制。
	Operations chan struct{}
	// Preflight 在创建新计划前执行（例如 stockcn 的公共日历覆盖期检查）；返回错误则本轮不创建新计划，已有计划照常续跑。
	Preflight func(ctx context.Context) error
	// JobWait 与 PollInterval 控制等待一个云端作业的时间。
	JobWait      time.Duration
	PollInterval time.Duration

	mu sync.Mutex
	// failCount 统计 fail 的调用次数，roundFailCount 是上一轮对账结束时的值；
	// 两者相等说明自上一轮结束以来没有任何失败（包括同一 tick 内对账之前的启用、升级）。
	failCount      int
	roundFailCount int
	lease          *scfinvoker.CollectorPublishLease
	holder         string
	pending        []pendingPlanJob
	aux            auxJobs
	lastVerified   time.Time
	// 按包名解析出的最新代码包及其解析时间（见 plan_package.go）。
	packageMu       sync.Mutex
	refreshMu       sync.Mutex // 串行化包解析（含云端查询）
	resolvedPackage string
	resolvedAt      time.Time
	packageErr      error     // 最近一次解析失败的原因（没有可用的缓存时），解析成功后清空
	packageWarnedAt time.Time // 上一次记"没有可用代码包"警告的时间
	// lastUpgradeCheck 是上一次检查函数代码包是否落后的时间。
	lastUpgradeCheck time.Time
	goneSeen         map[string]int
	// sleep 替换本轮内的等待（测试用）。
	sleep func(context.Context, time.Duration) error

	// 补采函数的创建与对账协程互不干扰：单独的互斥与租约，并缓存已确认存在的函数。
	backfillMu     sync.Mutex
	backfillReady  map[string]string // 补采函数名 → 已确认就绪的代码包 ID：目标包变了就重新校验升级
	backfillLease  *scfinvoker.CollectorPublishLease
	backfillHolder string
	// backfillJobs 是仍在云端运行的补采作业（函数名 → 作业），受 backfillMu 保护。
	// 作业结束之前不释放补采租约，也不对同一函数重复提交。
	backfillJobs map[string]backfillJob
}

// wait 在本轮里等待一段时间（测试替换为推进假时钟）。
func (r *PlanReconciler) wait(ctx context.Context, d time.Duration) error {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backfillJob 是一个仍在云端运行的补采函数作业（创建、升级、删除或清理遗留预留）。
type backfillJob struct {
	id   string
	kind string
	at   time.Time
}

// errBackfillJobRunning 表示补采作业在等待上限内没有结束；作业已登记在途，调用方稍后重试。
var errBackfillJobRunning = errors.New("backfill operation is still running")

func (r *PlanReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *PlanReconciler) fail(reason string, err error) error {
	r.failCount++
	if r.Metrics != nil {
		r.Metrics.ObserveAssignmentFailure(r.SpaceID, reason)
	}
	return err
}

// failItem 上报创建函数项失败：缺代码包单独归为 package（Monitor 提示去检查代码包），其余是环境问题。
func (r *PlanReconciler) failItem(err error) error {
	if errors.Is(err, errNoCodePackage) {
		return r.fail("package", err)
	}
	return r.fail("environment", err)
}

// failCloud 上报云端协作类失败：Admin 明确答复「发布租约或在途操作占用仍被持有」时单独归类，
// 其余（包括 Admin 不可达、无权限、响应异常）仍归为 cloudfunction，照常告警。
func (r *PlanReconciler) failCloud(err error) error {
	reason := "cloudfunction"
	if errors.Is(err, scfinvoker.ErrCollectorPublishLeaseHeld) {
		reason = ReasonPublishLeaseHeld
	}
	return r.fail(reason, err)
}

// inFlightSince 返回最早一个在途云端作业（计划作业或辅助作业）的提交时间。
func (r *PlanReconciler) inFlightSince() (time.Time, bool) {
	var since time.Time
	found := false
	consider := func(at time.Time) {
		if !found || at.Before(since) {
			since, found = at, true
		}
	}
	for _, job := range r.pending {
		consider(job.at)
	}
	for _, job := range r.aux.snapshot() {
		consider(job.at)
	}
	return since, found
}

// inFlightStuck 判断最老的在途云端作业是否已超过告警年龄（pendingJobWarnAge）。
func (r *PlanReconciler) inFlightStuck() bool {
	since, ok := r.inFlightSince()
	return ok && r.now().Sub(since) > pendingJobWarnAge
}

// finishRound 在每轮对账结束时统一收尾：有在途云端作业就按作业的提交时间上报 pending；
// 本轮及自上一轮结束以来没有任何失败时恢复健康并清除遗留的失败原因（计划仍在进行也一样）。
func (r *PlanReconciler) finishRound(err error) {
	// 函数代码升级仍在进行（errCodeUpgradePending）是等待，不是失败。
	clean := (err == nil || errors.Is(err, errCodeUpgradePending)) && r.failCount == r.roundFailCount
	r.roundFailCount = r.failCount
	if r.Metrics == nil {
		return
	}
	if since, ok := r.inFlightSince(); ok {
		r.Metrics.ObserveAssignmentPending(r.SpaceID, true, since)
	}
	if clean {
		r.Metrics.ObserveAssignmentRoundOK(r.SpaceID)
	}
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
func (r *PlanReconciler) Reconcile(ctx context.Context) (err error) {
	if r == nil || r.Store == nil || r.Cloud == nil || r.Groups == nil || r.Storage == nil {
		return errors.New("plan reconciler is not initialized")
	}
	if !r.mu.TryLock() {
		return errors.New("plan reconciliation already running")
	}
	defer r.mu.Unlock()
	defer func() { r.finishRound(err) }()
	r.refreshPackageIfAvailable(ctx)
	if err := r.packageGate(ctx); err != nil {
		return err
	}
	r.refreshAux(ctx)
	// 代码包升级与采集计划是否在进行无关：计划推进期间也要让落后的函数跟上配置的代码包。
	if r.now().Sub(r.lastUpgradeCheck) >= planUpgradeCheckInterval {
		r.lastUpgradeCheck = r.now()
		// 升级失败只记日志：不能因为代码升级慢或失败而拖住计划推进与重建。
		upgradeCtx, cancel := upgradeContext(ctx)
		if err := r.upgradeLaggingFunctions(upgradeCtx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan code upgrade failed space=%s: %v", r.SpaceID, err)
		}
		cancel()
	}
	open, err := r.Store.Open(ctx, r.SpaceID)
	if err != nil {
		return r.fail("store", fmt.Errorf("read open fetch plan: %w", err))
	}
	if open != nil {
		if r.Metrics != nil {
			r.Metrics.ObserveAssignmentPending(r.SpaceID, true, open.CreatedAt)
		}
		// 计划推进可能持续很久（整点频率要等到下一个整点才收尾），期间不受该计划影响的函数照常做云端校验与修复。
		if err := r.verifyUntouched(ctx, open); err != nil {
			log.WarnContextf(ctx, "collector fetch plan verification skipped space=%s: %v", r.SpaceID, err)
		}
		return r.advance(ctx, open)
	}
	if err := r.planChange(ctx); err != nil {
		return err
	}
	// 没有未生效的计划（或本轮刚把计划推进到生效）：对账成功。
	// 在途云端作业只要还没超过告警年龄就不影响成功：大批量升级可能连续跑十几分钟，期间一直有正常推进的作业；
	// 最老的作业超龄说明卡住了，此时不再刷新成功时间，由 Monitor 的对账陈旧判定（默认 15 分钟）发现。
	if r.Metrics != nil && !r.inFlightStuck() {
		if still, openErr := r.Store.Open(ctx, r.SpaceID); openErr == nil && still == nil {
			r.Metrics.ObserveAssignmentPending(r.SpaceID, false, time.Time{})
			r.Metrics.ObserveAssignmentSuccess(r.SpaceID, r.now().Unix())
			r.Metrics.ClearAssignmentFailure(r.SpaceID)
		}
	}
	return nil
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
		if parseErr != nil || params.Collector.DataType != "kline" {
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
	// 排除名单叠加在标签成员之上，放在“标的暂时取不到”的保护之后：全部标的都被排除是操作员的决定，照常生效。
	// 它的变化与标的变更一样生成新计划（已打开的计划，包括进行中的交接，先推进完）。
	if r.Exclusions != nil {
		exclusions, err := r.Exclusions.Active(ctx, r.SpaceID, r.now())
		if err != nil {
			return r.fail("store", fmt.Errorf("read subject exclusions: %w", err))
		}
		groups = excludeSubjects(groups, exclusions)
	}
	modes, forced, err := r.datasetModes(ctx, groups, previousDatasets)
	if err != nil {
		return r.fail("store", err)
	}
	options := r.Options
	options.Modes = modes
	desired, err := BuildDesiredState(options, groups, previousFunctions)
	if err != nil {
		return r.fail("rules", err)
	}
	// 交接与标的变更分开：有数据集的标的集合在变时，本轮先按已生效的执行端完成标的变更，交接等下一份计划。
	if handoffPending(desired, previousDatasets) && subjectsChanging(desired, previousDatasets) {
		options.Modes = appliedModes(modes, previousDatasets)
		if desired, err = BuildDesiredState(options, groups, previousFunctions); err != nil {
			return r.fail("rules", err)
		}
		log.InfoContextf(ctx, "collector fetch plan defers the ingest handoff until the subject change is applied space=%s", r.SpaceID)
	}
	handoff := handoffPending(desired, previousDatasets)
	if r.holdMissingDatasets(ctx, desired, previousDatasets, managed) {
		return nil
	}
	for _, estimate := range EstimateRateBudget(scfFunctions(desired.Functions), r.Options.AssumedEgressIPs) {
		if estimate.Exceeds() {
			log.WarnContextf(ctx, "collector fetch plan may hit the provider rate limit space=%s %s: add functions or egress IPs, or lower symbols per function", r.SpaceID, estimate)
		}
	}
	// 计划里有需要新建的函数时拉长提前量：创建 VPC 函数比改环境慢得多。
	lead := planLeadTime
	if handoff {
		lead = planLeadTimeHandoff
	}
	for name, function := range desired.Functions {
		if previousFunctions[name] == nil && function.Executor == "" {
			lead = planLeadTimeWithCreate
			break
		}
	}
	choose := func(frequency string) (int64, error) {
		return ChoosePeriodStartWithLead(frequency, r.now(), lead)
	}
	content, changed, err := BuildPlanContent(previousFunctions, previousDatasets, desired, choose)
	if err != nil {
		return r.fail("rules", err)
	}
	if !changed {
		if len(forced) > 0 && r.Modes != nil {
			// 切换命令没有带来任何变化：--force 标记不再需要。
			if err := r.Modes.ClearForce(ctx, r.SpaceID, nil); err != nil {
				return r.fail("store", err)
			}
		}
		return r.verifyFleet(ctx, scfFunctions(previousFunctions))
	}
	if r.Preflight != nil {
		if err := r.Preflight(ctx); err != nil {
			return r.fail("preflight", err)
		}
	}
	if err := r.validateContent(content); err != nil {
		return r.fail("capacity", err)
	}
	if r.Lateness != nil {
		for _, change := range content.Datasets {
			// 交接中的数据集在 drain 结束时再改：此刻改会让旧执行端最后几个周期按新截止建立。
			if change.Gone || change.HandoffLateness() {
				continue
			}
			if err := r.Lateness.EnsureAllowedLateness(ctx, r.SpaceID, change.DatasetID, change.LatenessS); err != nil {
				return r.fail("dataset", fmt.Errorf("set allowed lateness of dataset %s: %w", change.DatasetID, err))
			}
		}
	}
	progress := &FetchPlanProgress{}
	for _, change := range content.Datasets {
		if change.Changed && forced[change.DatasetID] {
			progress.Forced = true
		}
	}
	record, err := r.Store.Create(ctx, r.SpaceID, mustJSON(content), mustJSON(progress))
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
	encoded, err := EncodePlan(r.SpaceID, plan)
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
	for _, key := range []string{"MOOX_ACCESS_ADDRESS", "MOOX_ACCESS_ID", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
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
			if !function.IsSCF() {
				if err := plan.Validate(); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				continue
			}
			environment, err := r.functionEnvironment(function.Region, plan)
			if err != nil {
				return fmt.Errorf("function %s: %w", name, err)
			}
			merged := map[string]string{"MOOX_CODE_PACKAGE_ID": r.packageID(), "MOOX_CREATE_RESERVATION_ID": "00000000-0000-0000-0000-000000000000"}
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
	ready, err := r.registerSnapshots(ctx, record, content, progress)
	if err != nil || !ready {
		return err
	}
	// 登记成功后才记“开始”日志：登记因 P 过期而重选时，日志里的生效周期必须是 Storage 确认的那个。
	if !progress.LoggedStart {
		if r.logPlan(ctx, record, content, false) == nil {
			progress.LoggedStart = true
			if err := r.saveProgress(ctx, record, progress, record.Status, record.LastError); err != nil {
				return err
			}
		}
	}
	if record.Status == store.FetchPlanPending {
		if err := r.saveProgress(ctx, record, progress, store.FetchPlanApplying, ""); err != nil {
			return err
		}
	}
	// 交接数据集的截止各自在自己的窗口里改（旧周期都已建立之后、新执行端的第一个周期建立之前），只看时间：
	// 不等过渡阶段的确认，也不等整份计划收尾。旧端迟迟不确认（失联、云端作业慢）时，晚改截止会让新执行端的周期整期被拒。
	if err := r.applyHandoffLateness(ctx, record, content, progress, false); err != nil {
		return err
	}
	done, err := r.pushFunctions(ctx, record, content, progress, false)
	if err != nil || !done {
		return err
	}
	if end := content.WindowEnd(); !end.IsZero() && r.now().Before(end.Add(planFinalizeMargin)) {
		return nil
	}
	if err := r.applyHandoffLateness(ctx, record, content, progress, true); err != nil {
		return err
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
	if r.Modes != nil && content.HasHandoff() {
		// 只清这份计划完成交接的数据集的 --force 标记：其他数据集的切换（例如逐个数据集执行的 --force）
		// 还在等下一份计划，标记要留给它。被 --force 推进的标的变更计划没有交接，不清标记。
		if err := r.Modes.ClearForce(ctx, r.SpaceID, handedOff(content)); err != nil {
			log.WarnContextf(ctx, "collector fetch plan clear force flags space=%s: %v", r.SpaceID, err)
		}
	}
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
			p, chooseErr := ChoosePeriodStartWithLead(change.Frequency, r.now(), planLeadTimeWithCreate)
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
	var createErr error
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
	var cloud, streamItems []string
	for _, name := range todo {
		if content.Functions[name].IsSCF() {
			cloud = append(cloud, name)
		} else {
			streamItems = append(streamItems, name)
		}
	}
	if len(streamItems) > 0 {
		if err := r.confirmStream(ctx, record, content, progress, streamItems, final); err != nil {
			return false, err
		}
	}
	todo = cloud
	if len(todo) == 0 {
		return r.allConfirmed(content, progress, final), nil
	}
	// 创建失败或尚未完成的函数不阻塞其他函数：已存在的函数先下发，缺的下一轮再试。
	notReady := map[string]bool{}
	if !final {
		created, err := r.ensureCreated(ctx, record, content, progress, todo)
		if err != nil {
			createErr = err
		}
		if !created {
			for _, name := range todo {
				if content.Functions[name].Create && !progress.Created[name] {
					notReady[name] = true
				}
			}
		}
	}
	patches := make(map[string]*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch, len(todo))
	pushNames := make([]string, 0, len(todo))
	for _, name := range todo {
		if notReady[name] {
			continue
		}
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
		patches[name] = &cloudfunctionpb.FunctionInstanceRuntimeConfigPatch{InstanceId: name, ManagedEnvironment: environment, TimerEnabled: cron != "", TimerCron: timerCronOrPlaceholder(cron)}
		pushNames = append(pushNames, name)
	}
	if len(pushNames) > 0 {
		if err := r.submit(ctx, record, progress, kind, pushNames, func(lease *scfinvoker.CollectorPublishLease, batch []string) (string, error) {
			chunk := make([]*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch, 0, len(batch))
			for _, name := range batch {
				patch := patches[name]
				patch.CollectorPublishLeaseId, patch.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
				chunk = append(chunk, patch)
			}
			return r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, chunk)
		}); err != nil {
			return false, err
		}
	}
	if createErr != nil {
		_ = r.saveProgress(ctx, record, progress, record.Status, createErr.Error())
		return false, createErr
	}
	return r.allConfirmed(content, progress, final), nil
}

// allConfirmed 判断这一阶段需要下发的项都已确认。
func (r *PlanReconciler) allConfirmed(content *FetchPlanContent, progress *FetchPlanProgress, final bool) bool {
	confirmed := progress.Transitioned
	if final {
		confirmed = progress.Finalized
	}
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		needed := function.Push
		if final {
			needed = r.finalDiffers(content, function)
		}
		if needed && !confirmed[name] {
			return false
		}
	}
	return true
}

// confirmStream 把推送与本地执行端的项“下发”到这一阶段：记下要求的阶段（collector-stream 心跳拿到新版本后拉取），
// 看到实例回报已应用该记录的这一阶段（或更后的阶段）才算确认。
// --force：带 --force 创建的计划只在 collector-stream 是旧执行端（没有数据集正切到推送或本地执行端）时不等确认，新执行端仍要确认；
// 计划打开之后才执行的 --force（库里留下了标记）说明这份计划卡在失联的 collector-stream 上，不再等它确认。
func (r *PlanReconciler) confirmStream(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, names []string, final bool) error {
	stage, confirmed := StreamStageTransition, &progress.Transitioned
	if final {
		stage, confirmed = StreamStageFinal, &progress.Finalized
	}
	if streamStageRank(progress.StreamStage) < streamStageRank(stage) {
		progress.StreamStage = stage
		if err := r.saveProgress(ctx, record, progress, record.Status, record.LastError); err != nil {
			return err
		}
	}
	ok := progress.Forced && !movesToWorker(content)
	if !ok && !progress.Forced {
		later, err := r.forceRequested(ctx)
		if err != nil {
			return err
		}
		ok = later
	}
	if !ok && r.Workers != nil {
		worker, err := r.Workers.Get(ctx, r.SpaceID)
		if err != nil {
			return r.fail("store", err)
		}
		if worker != nil {
			var applied StreamApplied
			if json.Unmarshal([]byte(worker.Applied), &applied) == nil {
				ok = applied.ID == record.ID && streamStageRank(applied.Stage) >= streamStageRank(stage)
			}
		}
	}
	if !ok {
		return nil
	}
	for _, name := range names {
		progress.mark(confirmed, name)
	}
	return r.saveProgress(ctx, record, progress, record.Status, record.LastError)
}

// forceRequested 判断计划打开之后有没有执行过 --force：标记由切换命令原子写进采集方式表，
// 对账器在这里读取，不与对账器保存的进度互相覆盖。
func (r *PlanReconciler) forceRequested(ctx context.Context) (bool, error) {
	if r.Modes == nil {
		return false, nil
	}
	modes, err := r.Modes.List(ctx, r.SpaceID)
	if err != nil {
		return false, r.fail("store", err)
	}
	for _, mode := range modes {
		if mode.Force {
			return true, nil
		}
	}
	return false, nil
}

// handedOff 返回计划里完成交接（换了执行端）的数据集。
func handedOff(content *FetchPlanContent) []string {
	seen := map[string]bool{}
	var ids []string
	for _, change := range content.Datasets {
		if change.Changed && change.OldIngest != "" && !seen[change.DatasetID] {
			seen[change.DatasetID] = true
			ids = append(ids, change.DatasetID)
		}
	}
	sort.Strings(ids)
	return ids
}

// movesToWorker 判断计划里有数据集正从别的执行端切到推送或本地执行端（collector-stream 是新执行端）。
func movesToWorker(content *FetchPlanContent) bool {
	for _, change := range content.Datasets {
		if change.Changed && change.OldIngest != "" && change.IngestOrSCF() != PlanExecutorSCF {
			return true
		}
	}
	return false
}

func streamStageRank(stage string) int {
	switch stage {
	case StreamStageTransition:
		return 1
	case StreamStageFinal:
		return 2
	default:
		return 0
	}
}

// handoffLatenessMaxWait 是一轮对账里为等到交接截止的可改时刻最多等待的时间（对账每分钟一轮，1m 的窗口只有约 35 秒）。
const handoffLatenessMaxWait = 40 * time.Second

// handoffLatenessDue 返回一个交接数据集可以改截止的时刻与必须改完的时刻：
// 截止在周期第一次建立时冻结，旧执行端的最后一个周期（P 之前）到 P + 旧截止之后不能再建立；
// 新执行端的第一个周期 P 在它收盘（P 加一个周期）之后才建立，要在那之前改成新值。
func handoffLatenessDue(change *PlanDatasetChange) (time.Time, time.Time) {
	start := time.Unix(change.P, 0).UTC()
	due := start.Add(time.Duration(change.OldLatenessS)*time.Second + planFinalizeMargin)
	deadline := start.Add(time.Minute)
	if frequency, err := marketdata.ParseFrequency(change.Frequency); err == nil {
		deadline = nextBoundary(frequency, start.Add(time.Second))
	}
	return due, deadline
}

// applyHandoffLateness 把交接数据集的截止改成新值，每个数据集在自己的窗口里改（见 handoffLatenessDue）：
// 可改时刻在本轮 handoffLatenessMaxWait 之内就等到那一刻；all 为真（整份计划收尾、下发最终计划之前）时把剩下的都改掉。
func (r *PlanReconciler) applyHandoffLateness(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, all bool) error {
	if r.Lateness == nil {
		return nil
	}
	type pending struct {
		key           string
		due, deadline time.Time
	}
	var items []pending
	for key, change := range content.Datasets {
		if change.HandoffLateness() && !progress.LatenessSet[key] {
			due, deadline := handoffLatenessDue(change)
			items = append(items, pending{key: key, due: due, deadline: deadline})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].due.Equal(items[j].due) {
			return items[i].due.Before(items[j].due)
		}
		return items[i].key < items[j].key
	})
	for _, item := range items {
		if wait := item.due.Sub(r.now()); wait > 0 && !all {
			if wait > handoffLatenessMaxWait {
				continue
			}
			if err := r.wait(ctx, wait); err != nil {
				return nil
			}
		}
		key, change := item.key, content.Datasets[item.key]
		if r.now().After(item.deadline) {
			log.WarnContextf(ctx, "collector fetch plan switched allowed lateness late space=%s dataset=%s: periods from %s were created with the old lateness", r.SpaceID, key, time.Unix(change.P, 0).UTC().Format(time.RFC3339))
		}
		if err := r.Lateness.EnsureAllowedLateness(ctx, r.SpaceID, change.DatasetID, change.LatenessS); err != nil {
			return r.fail("dataset", fmt.Errorf("set allowed lateness of dataset %s: %w", change.DatasetID, err))
		}
		progress.mark(&progress.LatenessSet, key)
		if err := r.saveProgress(ctx, record, progress, record.Status, ""); err != nil {
			return err
		}
		log.InfoContextf(ctx, "collector fetch plan switched allowed lateness space=%s dataset=%s %ds -> %ds", r.SpaceID, key, change.OldLatenessS, change.LatenessS)
	}
	return nil
}

// datasetModes 返回各数据集的采集方式，并为第一次出现的数据集写入初值：已在生效计划里的按它现在的执行端，
// 真正新出现的取空间默认值（default_ingest、default_executor）。forced 是带 --force 标记的数据集。
func (r *PlanReconciler) datasetModes(ctx context.Context, groups []TaskGroup, previous map[string]*PlanDatasetState) (map[string]DatasetMode, map[string]bool, error) {
	modes := map[string]DatasetMode{}
	forced := map[string]bool{}
	if r.Modes == nil {
		return modes, forced, nil
	}
	stored, err := r.Modes.List(ctx, r.SpaceID)
	if err != nil {
		return nil, nil, err
	}
	current := map[string]string{}
	for _, state := range previous {
		current[state.DatasetID] = state.IngestOrSCF()
	}
	var initial []store.IngestMode
	for _, group := range groups {
		if _, ok := stored[group.DatasetID]; ok {
			continue
		}
		mode := r.DefaultMode
		// 任务参数指定的初值优先于空间默认值。
		if group.Ingest != "" {
			mode.Mode = group.Ingest
		}
		if group.Executor != "" {
			mode.Executor = group.Executor
		}
		if executor, ok := current[group.DatasetID]; ok {
			switch executor {
			case PlanExecutorStreaming:
				mode.Mode = IngestStreaming
			case PlanExecutorLocal:
				mode = DatasetMode{Mode: IngestPull, Executor: PlanExecutorLocal}
			default:
				mode = DatasetMode{Mode: IngestPull, Executor: PlanExecutorSCF}
			}
		}
		if mode.Mode == "" {
			mode.Mode = IngestPull
		}
		if mode.Executor == "" {
			mode.Executor = PlanExecutorSCF
		}
		entry := store.IngestMode{SpaceID: r.SpaceID, DatasetID: group.DatasetID, Mode: mode.Mode, Executor: mode.Executor}
		stored[group.DatasetID] = entry
		initial = append(initial, entry)
	}
	if err := r.Modes.Initialize(ctx, initial); err != nil {
		return nil, nil, err
	}
	for datasetID, mode := range stored {
		modes[datasetID] = DatasetMode{Mode: mode.Mode, Executor: mode.Executor}
		if mode.Force {
			forced[datasetID] = true
		}
	}
	return modes, forced, nil
}

// excludeSubjects 从采集组里去掉排除的标的；标的全被排除的组不再采集。
func excludeSubjects(groups []TaskGroup, exclusions []store.SubjectExclusion) []TaskGroup {
	if len(exclusions) == 0 {
		return groups
	}
	result := make([]TaskGroup, 0, len(groups))
	for _, group := range groups {
		excluded := map[string]bool{}
		for _, exclusion := range exclusions {
			if exclusion.Excludes(group.MarketType) {
				excluded[exclusion.SubjectID] = true
			}
		}
		subjects := make([]string, 0, len(group.Subjects))
		for _, subject := range group.Subjects {
			if !excluded[strings.ToUpper(subject)] {
				subjects = append(subjects, subject)
			}
		}
		if len(subjects) == len(group.Subjects) {
			result = append(result, group)
			continue
		}
		if len(subjects) == 0 {
			continue
		}
		group.Subjects = subjects
		group.ExternalSymbols = keepKeys(group.ExternalSymbols, subjects)
		group.Pairs = keepKeys(group.Pairs, subjects)
		result = append(result, group)
	}
	return result
}

func keepKeys(values map[string]string, keys []string) map[string]string {
	if values == nil {
		return nil
	}
	kept := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := values[key]; ok {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// handoffPending 判断期望状态里有已生效的数据集换了执行端。
func handoffPending(desired *DesiredState, previous map[string]*PlanDatasetState) bool {
	for key, dataset := range desired.Datasets {
		if old := previous[key]; old != nil && old.IngestOrSCF() != dataset.IngestOrSCF() {
			return true
		}
	}
	return false
}

// subjectsChanging 判断有已生效的数据集标的集合在变。
func subjectsChanging(desired *DesiredState, previous map[string]*PlanDatasetState) bool {
	for key, dataset := range desired.Datasets {
		if old := previous[key]; old != nil && old.Hash != dataset.Hash {
			return true
		}
	}
	return false
}

// appliedModes 把已生效的数据集的方式还原成它们现在的执行端（推迟交接），新数据集保持库里的方式。
func appliedModes(modes map[string]DatasetMode, previous map[string]*PlanDatasetState) map[string]DatasetMode {
	result := make(map[string]DatasetMode, len(modes))
	for id, mode := range modes {
		result[id] = mode
	}
	for _, state := range previous {
		mode := result[state.DatasetID]
		switch state.IngestOrSCF() {
		case PlanExecutorStreaming:
			mode.Mode = IngestStreaming
		case PlanExecutorLocal:
			mode.Mode, mode.Executor = IngestPull, PlanExecutorLocal
		default:
			mode.Mode, mode.Executor = IngestPull, PlanExecutorSCF
		}
		result[state.DatasetID] = mode
	}
	return result
}

// scfFunctions 只保留 SCF 函数（云端校验、修复、频控估算只作用于它们）。
func scfFunctions(functions map[string]*PlanFunction) map[string]*PlanFunction {
	result := make(map[string]*PlanFunction, len(functions))
	for name, function := range functions {
		if function != nil && function.Executor == "" {
			result[name] = function
		}
	}
	return result
}

// timerCronOrPlaceholder 停用定时器时仍要给 FunctionInstance 一个受支持的 cron（预检要求非空），只靠 enabled=false 停用。
func timerCronOrPlaceholder(cron string) string {
	if cron == "" {
		return "0 * * * * * *"
	}
	return cron
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
		return false, r.fail("cloudfunction", fmt.Errorf("list market fetchers: %w", err))
	}
	existing := map[string]struct{}{}
	for _, node := range nodes {
		existing[node.InstanceID] = struct{}{}
	}
	var absent []string
	for _, name := range missing {
		if _, ok := existing[name]; !ok {
			absent = append(absent, name)
		}
	}
	if cleared, err := r.clearStaleReservations(ctx, "timer", absent); err != nil {
		return false, r.failCloud(err)
	} else if !cleared {
		return false, nil
	}
	var items []*cloudfunctionpb.FunctionInstanceCreateItem
	var names []string
	for _, name := range missing {
		if _, ok := existing[name]; ok {
			progress.mark(&progress.Created, name)
			continue
		}
		function := content.Functions[name]
		item, err := r.createItem(record, content, function)
		if err != nil {
			return false, r.failItem(fmt.Errorf("function %s: %w", name, err))
		}
		items = append(items, item)
		names = append(names, name)
	}
	if len(items) == 0 {
		return true, r.saveProgress(ctx, record, progress, record.Status, "")
	}
	byName := make(map[string]*cloudfunctionpb.FunctionInstanceCreateItem, len(items))
	for index, item := range items {
		byName[names[index]] = item
	}
	if err := r.submit(ctx, record, progress, planJobKindCreate, names, func(lease *scfinvoker.CollectorPublishLease, batch []string) (string, error) {
		chunk := make([]*cloudfunctionpb.FunctionInstanceCreateItem, 0, len(batch))
		for _, name := range batch {
			item := byName[name]
			item.CollectorPublishLeaseId, item.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
			chunk = append(chunk, item)
		}
		return r.Cloud.SubmitCreateFunctionInstances(ctx, r.SpaceID, chunk)
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

// staleReservationAge 是创建预留的最长等待：超过后仍未就绪的预留视为创建失败后的遗留。
const staleReservationAge = 15 * time.Minute

// clearStaleReservations 清理要创建的函数名下遗留的创建预留（目录里有记录但没有部署完成）。
// 创建失败后预留一直留在 FunctionInstance 目录里，不清理就无法重新创建同名函数。
// 清理走受发布租约保护的删除作业；作业未结束时返回 false，下一轮再继续。
func (r *PlanReconciler) clearStaleReservations(ctx context.Context, trigger string, names []string) (bool, error) {
	if len(names) == 0 {
		return true, nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return r.clearStale(ctx, trigger, names, false)
}

// clearStale 是 clearStaleReservations 的实现，调用方须已持有操作槽。
func (r *PlanReconciler) clearStale(ctx context.Context, trigger string, names []string, backfill bool) (bool, error) {
	unready, err := r.Cloud.ListUnreadyMarketFetchers(ctx, r.SpaceID, trigger)
	if err != nil {
		return false, fmt.Errorf("list unready market fetchers: %w", err)
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	inFlight := map[string]struct{}{}
	if !backfill {
		// 补采路径与实时对账并发运行，不能读写实时对账的在途作业与租约。
		for _, job := range r.pending {
			for _, name := range job.functions {
				inFlight[name] = struct{}{}
			}
		}
	}
	var stale []string
	for _, node := range unready {
		if _, ok := wanted[node.InstanceID]; !ok {
			continue
		}
		if _, busy := inFlight[node.InstanceID]; busy {
			continue
		}
		if !node.ModifiedAt.IsZero() && r.now().Sub(node.ModifiedAt) < staleReservationAge {
			// 刚创建的预留可能仍在创建中，等待超时后再清理。
			return false, nil
		}
		stale = append(stale, node.InstanceID)
	}
	if len(stale) == 0 {
		return true, nil
	}
	sort.Strings(stale)
	if !backfill {
		if stale = r.excludeBusy(ctx, stale); len(stale) == 0 {
			return false, nil
		}
	}
	var items []scfinvoker.BatchItemResult
	if backfill {
		// 补采路径：作业未结束时保留租约并登记在途（见 runBackfillJob），本轮视为清理尚未完成。
		items, err = r.runBackfillJob(ctx, names[0], "cleanup", "stale reservation cleanup", func(lease *scfinvoker.CollectorPublishLease) (string, error) {
			jobID, submitErr := r.Cloud.SubmitDeleteFunctionInstances(ctx, r.SpaceID, stale, lease)
			if submitErr != nil {
				return "", fmt.Errorf("submit delete of stale reservations: %w", submitErr)
			}
			return jobID, nil
		})
		if errors.Is(err, errBackfillJobRunning) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	} else {
		lease, leaseErr := r.ensureLease(ctx)
		if leaseErr != nil {
			return false, fmt.Errorf("publish lease: %w", leaseErr)
		}
		jobID, submitErr := r.Cloud.SubmitDeleteFunctionInstances(ctx, r.SpaceID, stale, lease)
		if submitErr != nil {
			return false, fmt.Errorf("submit delete of stale reservations: %w", submitErr)
		}
		var terminal bool
		items, terminal, err = r.waitAux(ctx, auxJobKindCleanup, stale, jobID)
		if err != nil {
			return false, err
		}
		if !terminal {
			return false, nil
		}
	}
	for _, item := range items {
		if !item.Success {
			return false, fmt.Errorf("delete stale reservation %s: %s", item.InstanceID, item.Message)
		}
	}
	log.Infof("collector fetch plan removed stale create reservations space=%s functions=%s", r.SpaceID, strings.Join(stale, ","))
	// 实时对账路径的租约由调用方统一持有与释放：清理后调用方还要继续创建与补推，不能在此释放；
	// 补采路径的租约已由 runBackfillJob 在作业结束后释放。
	return true, nil
}

// upgradeCode 把代码包版本落后的已生效函数升级到 scf_plan 配置的代码包：只更新代码，环境与定时器不动。
// 升级走受发布租约保护的部署作业，分批进行；单个函数更新代码的几秒内可能漏掉一次触发，缺口由自动补采补回。
func (r *PlanReconciler) upgradeCode(ctx context.Context, applied map[string]*PlanFunction, byName map[string]scfinvoker.FunctionInstance) error {
	names := make([]string, 0, len(applied))
	for name := range applied {
		names = append(names, name)
	}
	return r.upgradeCodeOf(ctx, names, byName)
}

// verifyUntouched 在有未生效计划时，对不受该计划影响的已生效函数做云端校验。
func (r *PlanReconciler) verifyUntouched(ctx context.Context, open *store.FetchPlanRecord) error {
	if !r.lastVerified.IsZero() && r.now().Sub(r.lastVerified) < planVerifyInterval {
		return nil
	}
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil || applied == nil {
		return err
	}
	appliedContent, err := decodeFetchPlanContent(applied.Plan)
	if err != nil {
		return err
	}
	openContent, err := decodeFetchPlanContent(open.Plan)
	if err != nil {
		return err
	}
	previous, _ := appliedContent.Applied()
	previous = scfFunctions(previous)
	untouched := map[string]*PlanFunction{}
	for name, function := range previous {
		change := openContent.Functions[name]
		if change != nil && (change.Push || change.Create || r.finalDiffers(openContent, change)) {
			continue
		}
		untouched[name] = function
	}
	return r.verifyFleet(ctx, untouched)
}

// upgradeLaggingFunctions 找出本空间代码包落后的计划函数并升级。
func (r *PlanReconciler) upgradeLaggingFunctions(ctx context.Context) error {
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	nodes, err := r.Cloud.ListTimerMarketFetchers(ctx, r.SpaceID)
	release()
	if err != nil {
		return r.fail("cloudfunction", fmt.Errorf("list market fetchers: %w", err))
	}
	prefix := r.fleetPrefix()
	byName := map[string]scfinvoker.FunctionInstance{}
	var names []string
	for _, node := range nodes {
		if strings.HasPrefix(node.InstanceID, prefix) {
			byName[node.InstanceID] = node
			names = append(names, node.InstanceID)
		}
	}
	return r.upgradeCodeOf(ctx, names, byName)
}

// upgradeCodeOf 升级给定函数名里代码包落后的函数。
func (r *PlanReconciler) upgradeCodeOf(ctx context.Context, names []string, byName map[string]scfinvoker.FunctionInstance) error {
	if strings.TrimSpace(r.packageID()) == "" {
		return nil
	}
	var stale []string
	for _, name := range names {
		node, ok := byName[name]
		if ok && strings.TrimSpace(node.PackageID) != "" && node.PackageID != r.packageID() {
			stale = append(stale, name)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	if stale = r.excludeBusy(ctx, stale); len(stale) == 0 {
		return nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return r.failCloud(err)
	}
	log.WarnContextf(ctx, "collector fetch plan upgrading function code space=%s package=%s functions=%d", r.SpaceID, r.packageID(), len(stale))
	for start := 0; start < len(stale); start += runtimeConfigBatchSize {
		batch := stale[start:min(start+runtimeConfigBatchSize, len(stale))]
		jobID, submitErr := r.Cloud.SubmitDeployFunctionInstances(ctx, r.SpaceID, batch, r.packageID(), lease)
		if submitErr != nil {
			return r.fail("cloudfunction", submitErr)
		}
		results, terminal, waitErr := r.waitAux(ctx, auxJobKindUpgrade, batch, jobID)
		if waitErr != nil {
			return r.fail("cloudfunction", waitErr)
		}
		if !terminal {
			// 作业已记入在途集合，后续轮次不会对这批函数重复提交。
			log.WarnContextf(ctx, "collector fetch plan code upgrade job still running space=%s job=%s", r.SpaceID, jobID)
			return nil
		}
		for _, result := range results {
			if !result.Success {
				return r.fail("cloudfunction", fmt.Errorf("upgrade code of %s: %s", result.InstanceID, result.Message))
			}
		}
	}
	r.releaseLease(ctx)
	return nil
}

func (r *PlanReconciler) createItem(record *store.FetchPlanRecord, content *FetchPlanContent, function *PlanFunctionChange) (*cloudfunctionpb.FunctionInstanceCreateItem, error) {
	network, ok := r.Deploy.Networks[function.Region]
	if !ok || network.VPCID == "" || network.SubnetID == "" {
		return nil, fmt.Errorf("region %s has no VPC and subnet configured", function.Region)
	}
	pkg, err := r.requirePackage()
	if err != nil {
		return nil, err
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
	return &cloudfunctionpb.FunctionInstanceCreateItem{
		CloudAccountId: r.Deploy.CloudAccountID, FunctionType: "scf-event", TriggerType: "timer", Runtime: "Go1", Handler: "main",
		Namespace: r.Deploy.NamespaceFor(function.Name), Region: function.Region, PackageId: pkg, Environment: environment,
		Config: map[string]string{
			"timeout": timeout, "memory_size": strconv.Itoa(memory), "max_instance_concurrency": "1",
			"vpc_id": network.VPCID, "subnet_id": network.SubnetID, "public_net_status": "ENABLE",
		},
		Metadata:            mustStruct(map[string]any{"function_name": function.Name, "instance_id": function.Name, "workload_type": "market_fetcher", "function_mode": "kline", "memory_size": memory, "timeout_seconds": tencent.CollectorTimerTimeoutSeconds}),
		CreateReservationId: reservationID,
	}, nil
}

// submit 获取发布租约，按批提交云端作业并等待结果；成功的函数记入 progress。
func (r *PlanReconciler) submit(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress, kind string, names []string, send func(lease *scfinvoker.CollectorPublishLease, batch []string) (string, error)) error {
	// 仍有辅助作业在途的函数本轮不提交，等作业结束后下一轮继续。
	if names = r.excludeBusy(ctx, names); len(names) == 0 {
		return nil
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return r.failCloud(fmt.Errorf("publish lease: %w", err))
	}
	var firstErr error
	for start := 0; start < len(names); start += runtimeConfigBatchSize {
		batch := names[start:min(start+runtimeConfigBatchSize, len(names))]
		jobID, sendErr := send(lease, batch)
		if sendErr != nil {
			_ = r.saveProgress(ctx, record, progress, record.Status, sendErr.Error())
			return r.fail("cloudfunction", fmt.Errorf("submit %s: %w", kind, sendErr))
		}
		job := pendingPlanJob{id: jobID, kind: kind, functions: batch, at: r.now()}
		items, terminal, waitErr := r.waitJobWithin(ctx, jobID)
		if waitErr != nil {
			r.pending = append(r.pending, job)
			return r.fail("cloudfunction", waitErr)
		}
		if !terminal {
			r.pending = append(r.pending, job)
			return nil
		}
		if err := r.applyResults(ctx, record, progress, job, items); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
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
			succeeded[item.InstanceID] = true
		case item.Failed:
			failures = append(failures, item.InstanceID+": "+item.Message)
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
		return r.fail("cloudfunction", fmt.Errorf("%s job reported failures: %s", job.kind, lastError))
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
	for index := range pending {
		r.warnStalePending(ctx, &pending[index])
		job := pending[index]
		items, terminal, err := r.checkJob(ctx, job.id)
		if err != nil {
			r.pending = append(r.pending, pending[index:]...)
			return true, r.fail("cloudfunction", err)
		}
		if !terminal {
			r.pending = append(r.pending, pending[index:]...)
			if renewErr := r.renewLease(ctx); renewErr != nil {
				return true, r.failCloud(renewErr)
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
	case cloudfunctionpb.FunctionJobStatus_FUNCTION_JOB_STATUS_SUCCESS, cloudfunctionpb.FunctionJobStatus_FUNCTION_JOB_STATUS_FAILED, cloudfunctionpb.FunctionJobStatus_FUNCTION_JOB_STATUS_PARTIAL:
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

// waitJobReserve 是 waitJobWithin 给本轮剩余时间留出的余量上限：等待上限 = min(JobWait, 剩余时间 − 余量)，
// 轮次的时间用尽之前就把作业交给下一轮继续，而不是等到 ctx 到期再把「慢」记成失败。
// 余量取 5 秒与剩余时间四分之一的较小者：剩余时间很短时仍会查询作业，只是等待上限随之缩小。
const waitJobReserve = 5 * time.Second

// waitJobWithin 轮询云端作业，到等待上限仍未结束时返回「未结束」（terminal=false, err=nil），由调用方登记在途作业，
// 下一轮继续。只有 checkJob 本身出错、或父 ctx 被取消/超时才返回错误。
// 对账、漂移修复等实时路径使用它；补采路径仍用 waitJob（它们没有在途作业记录，且返回时就释放租约）。
func (r *PlanReconciler) waitJobWithin(ctx context.Context, jobID string) ([]scfinvoker.BatchItemResult, bool, error) {
	wait, interval := r.JobWait, r.PollInterval
	if wait <= 0 {
		wait = 90 * time.Second
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if room := remaining - min(waitJobReserve, remaining/4); room < wait {
			wait = room
		}
	}
	if wait <= 0 {
		// 父 ctx 已经到期：与 waitJob 一样返回其错误。
		return nil, false, ctx.Err()
	}
	// 局部 ctx 同时约束每一次 checkJob，一次慢查询不会越过等待上限。
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	limit := time.Now().Add(wait)
	for {
		items, terminal, err := r.checkJob(waitCtx, jobID)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return nil, false, ctx.Err()
			case waitCtx.Err() != nil:
				return nil, false, nil
			default:
				return nil, false, err
			}
		}
		if terminal {
			return items, true, nil
		}
		if !time.Now().Add(interval).Before(limit) {
			return nil, false, nil
		}
		select {
		case <-time.After(interval):
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			return nil, false, nil
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
	if r.lease == nil || len(r.pending) > 0 || r.aux.count() > 0 {
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
	// 小时校验必须读回云端的真实状态，而不是目录缓存：直接在 SCF 删函数、停触发器、改环境都要能发现。
	nodes, err := r.Cloud.ListTimerMarketFetchersFresh(ctx, r.SpaceID)
	release()
	if err != nil {
		return r.fail("cloudfunction", fmt.Errorf("list market fetchers: %w", err))
	}
	byName := map[string]scfinvoker.FunctionInstance{}
	for _, node := range nodes {
		byName[node.InstanceID] = node
	}
	// 配置里的代码包换了新版本：先把已有函数的代码升级到该版本，再比对环境。
	// 升级失败不阻断后面的重建与修复（例如云端函数已被删除，升级必然失败）。
	upgradeCtx, cancelUpgrade := upgradeContext(ctx)
	err = r.upgradeCode(upgradeCtx, applied, byName)
	cancelUpgrade()
	if err != nil {
		log.WarnContextf(ctx, "collector fetch plan code upgrade failed space=%s: %v", r.SpaceID, err)
	}
	var drifted, missing, gone []string
	unverified := 0
	patches := map[string]*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch{}
	names := make([]string, 0, len(applied))
	for name := range applied {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		function := applied[name]
		environment, envErr := r.functionEnvironment(function.Region, function.Plan)
		if envErr != nil {
			return r.fail("environment", fmt.Errorf("function %s: %w", name, envErr))
		}
		patches[name] = &cloudfunctionpb.FunctionInstanceRuntimeConfigPatch{InstanceId: name, ManagedEnvironment: environment, TimerEnabled: function.Cron != "", TimerCron: timerCronOrPlaceholder(function.Cron)}
		node, exists := byName[name]
		if !exists {
			missing = append(missing, name)
			continue
		}
		switch readbackState(node.Metadata, r.now()) {
		case readbackStale:
			unverified++
			continue
		case readbackFailed:
			unverified++
			continue
		case readbackGone:
			gone = append(gone, name)
			continue
		}
		if actualAssignmentHash(node.Metadata) != environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] || timerNeedsRepair(node.Metadata, function.Cron) || accessDrifted(node.Metadata, environment) {
			drifted = append(drifted, name)
		}
	}
	if len(drifted) == 0 && len(missing) == 0 && len(gone) == 0 {
		if unverified > 0 {
			// 有函数这次没读回成功：不能宣布全部核对完毕，几分钟后重试。
			log.WarnContextf(ctx, "collector fetch plan cloud readback incomplete space=%s unverified=%d", r.SpaceID, unverified)
			r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
			return nil
		}
		r.lastVerified = r.now()
		return nil
	}
	// 仍有在途辅助作业的函数本轮不再提交，等作业结束后下一轮继续。
	drifted, missing, gone = r.excludeBusy(ctx, drifted), r.excludeBusy(ctx, missing), r.excludeBusy(ctx, gone)
	if len(drifted) == 0 && len(missing) == 0 && len(gone) == 0 {
		return nil
	}
	log.WarnContextf(ctx, "collector fetch plan repairing space=%s drifted=%s missing=%s gone=%s", r.SpaceID, strings.Join(drifted, ","), strings.Join(missing, ","), strings.Join(gone, ","))
	release, err = r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()
	lease, err := r.ensureLease(ctx)
	if err != nil {
		return r.failCloud(err)
	}
	// allOK 表示没有失败项也没有在途作业；failed 表示有作业已到终态但失败。
	// allOK 为 false 而 failed 为 false，说明只有仍在云端运行的作业（不是失败）。
	allOK, failed := true, false
	// 云端函数已不存在但目录里还有记录：先走受租约保护的删除清掉记录，才能重新创建。
	if len(gone) > 0 {
		sort.Strings(gone)
		jobID, submitErr := r.Cloud.SubmitDeleteFunctionInstances(ctx, r.SpaceID, gone, lease)
		if submitErr != nil {
			return r.fail("cloudfunction", submitErr)
		}
		results, terminal, waitErr := r.waitAux(ctx, auxJobKindRepair, gone, jobID)
		if waitErr != nil {
			return r.fail("cloudfunction", waitErr)
		}
		removed := map[string]bool{}
		for _, result := range results {
			removed[result.InstanceID] = result.Success
		}
		for _, name := range gone {
			switch {
			case terminal && removed[name]:
				missing = append(missing, name)
			case !terminal:
				allOK = false
			default:
				allOK, failed = false, true
			}
		}
	}
	// 云端缺失的函数（例如被手工删除）按已生效计划重建，再补推定时器；先清掉上次重建失败遗留的预留。
	if len(missing) > 0 {
		cleared, clearErr := r.clearStale(ctx, "timer", missing, false)
		if clearErr != nil {
			return r.failCloud(clearErr)
		}
		if !cleared {
			// 清理作业仍在途或预留刚创建还要再等：不是失败，下一轮继续。
			missing = nil
			allOK = false
		}
	}
	for start := 0; start < len(missing); start += runtimeConfigBatchSize {
		batch := missing[start:min(start+runtimeConfigBatchSize, len(missing))]
		items := make([]*cloudfunctionpb.FunctionInstanceCreateItem, 0, len(batch))
		for _, name := range batch {
			function := applied[name]
			change := &PlanFunctionChange{Name: name, Region: function.Region, Market: function.Market, Old: function.Plan, New: function.Plan}
			item, itemErr := r.createItem(&store.FetchPlanRecord{ID: r.now().Unix()}, &FetchPlanContent{}, change)
			if itemErr != nil {
				return r.failItem(fmt.Errorf("function %s: %w", name, itemErr))
			}
			item.CollectorPublishLeaseId, item.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
			items = append(items, item)
		}
		jobID, submitErr := r.Cloud.SubmitCreateFunctionInstances(ctx, r.SpaceID, items)
		if submitErr != nil {
			return r.fail("cloudfunction", submitErr)
		}
		results, terminal, waitErr := r.waitAux(ctx, auxJobKindRepair, batch, jobID)
		if waitErr != nil {
			return r.fail("cloudfunction", waitErr)
		}
		created := map[string]bool{}
		for _, result := range results {
			created[result.InstanceID] = result.Success
		}
		for _, name := range batch {
			switch {
			case terminal && created[name]:
				drifted = append(drifted, name)
			case !terminal:
				allOK = false
			default:
				allOK, failed = false, true
			}
		}
	}
	sort.Strings(drifted)
	for start := 0; start < len(drifted); start += runtimeConfigBatchSize {
		batch := drifted[start:min(start+runtimeConfigBatchSize, len(drifted))]
		list := make([]*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch, 0, len(batch))
		for _, name := range batch {
			patch := patches[name]
			patch.CollectorPublishLeaseId, patch.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
			list = append(list, patch)
		}
		jobID, submitErr := r.Cloud.SubmitRuntimeConfigs(ctx, r.SpaceID, list)
		if submitErr != nil {
			return r.fail("cloudfunction", submitErr)
		}
		results, terminal, waitErr := r.waitAux(ctx, auxJobKindRepair, batch, jobID)
		if waitErr != nil {
			return r.fail("cloudfunction", waitErr)
		}
		pushed := map[string]bool{}
		for _, result := range results {
			pushed[result.InstanceID] = result.Success
		}
		for _, name := range batch {
			switch {
			case !terminal:
				allOK = false
			case !pushed[name]:
				allOK, failed = false, true
			}
		}
	}
	if allOK {
		r.releaseLease(ctx)
		if unverified > 0 {
			// 修复完成，但有函数这次没读回成功：几分钟后重新读回。
			r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
			return nil
		}
		r.lastVerified = r.now()
		return nil
	}
	// 有失败项：不更新读回时间，下一轮继续修复。
	if failed {
		return r.fail("cloudfunction", errors.New("repair of drifted or missing functions did not fully succeed"))
	}
	// 只有在途作业：不是失败，也不能宣布核对完毕。作业已登记在在途集合里，结束前不会重复提交，
	// 结束后下一轮重新读回并继续。
	return nil
}

// accessDrifted 判断云端记录的 Access 入口与调用方身份是否与期望一致。
func accessDrifted(metadata map[string]any, environment map[string]string) bool {
	for metaKey, envKey := range map[string]string{"access_address": "MOOX_ACCESS_ADDRESS", "access_id": "MOOX_ACCESS_ID", "access_key_id": "MOOX_CALLER_KEY_ID"} {
		// 读回得到的云端实际值优先；没有读回值时才用目录里记录的值。
		actual, ok := metadata["actual_"+metaKey]
		if !ok {
			actual = metadata[metaKey]
		}
		if fmt.Sprint(actual) != environment[envKey] {
			return true
		}
	}
	return false
}

// actualAssignmentHash 返回云端函数环境里实际生效的分配 hash；没有读回值时用目录记录。
func actualAssignmentHash(metadata map[string]any) string {
	if actual, ok := metadata["actual_assignment_hash"]; ok {
		return fmt.Sprint(actual)
	}
	return fmt.Sprint(metadata["assignment_hash"])
}

type readbackResult int

const (
	readbackFresh  readbackResult = iota // 刚读回成功
	readbackStale                        // 没有新的读回时间
	readbackFailed                       // 读回失败（云端暂时不可读）
	readbackGone                         // 云端函数已不存在
)

// readbackMaxAge 是读回时间的有效期：超过说明这次列表没有真正读回云端。
const readbackMaxAge = 10 * time.Minute

// readbackState 判断节点元数据里的云端读回是否可信：必须是刚做的读回，失败要区分“不存在”和“读不到”。
func readbackState(metadata map[string]any, now time.Time) readbackResult {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(fmt.Sprint(metadata["timer_last_readback_at"])))
	if err != nil || now.Sub(at) > readbackMaxAge {
		return readbackStale
	}
	if message := strings.ToLower(strings.TrimSpace(fmt.Sprint(metadata["timer_status_error"]))); message != "" && message != "<nil>" {
		if strings.Contains(message, "timer trigger is missing") {
			return readbackFresh // 触发器缺失由 timerNeedsRepair 按元数据修复
		}
		if strings.Contains(message, "resourcenotfound") || strings.Contains(message, "not found") {
			if strings.Contains(message, "function") {
				return readbackGone
			}
			// 触发器不存在由 timerNeedsRepair 按元数据修复。
			return readbackFresh
		}
		return readbackFailed
	}
	return readbackFresh
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
	// 定时器的 Message 与 Qualifier 决定函数是否接受事件，漂移后同样需要修复。
	return !strings.EqualFold(fmt.Sprint(metadata["timer_available_status"]), "available") ||
		strings.TrimSpace(fmt.Sprint(metadata["timer_actual_cron"])) != cron ||
		strings.TrimSpace(fmt.Sprint(metadata["timer_actual_qualifier"])) != timerTriggerQualifier ||
		strings.TrimSpace(fmt.Sprint(metadata["timer_actual_message"])) != timerTriggerMessage
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
	return r.Activity.AppendActivityLogs(ctx, r.SpaceID, entries)
}

// BackfillFunctionName 返回某市场、地域的补采函数名，名称含 PlanFleetMarker。
func BackfillFunctionName(spaceID, marketType, region string) string {
	region = strings.TrimPrefix(strings.ToLower(region), "ap-")
	return fmt.Sprintf("moox-fetch%sbackfill-%s-%s-%s", PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(spaceID)),
		planNameSanitizer.Replace(strings.ToLower(marketType)), planNameSanitizer.Replace(region))
}

// BackfillRegion 返回某市场补采函数所在的地域（市场白名单中的第一个地域）。
func (r *PlanReconciler) BackfillRegion(marketType string) (string, bool) {
	regions := r.Options.Markets[strings.ToLower(marketType)]
	if len(regions) == 0 {
		return "", false
	}
	return regions[0], true
}

// BackfillReservedConcurrency 是补采函数的最大并发实例数，由平台的函数级预留并发硬性限制。
const BackfillReservedConcurrency = 5

// EnsureBackfillFunction 保证该市场的补采函数存在（与实时函数同一代码包，不带定时器），返回函数名。
// 多个补采协程并发调用时串行执行，已确认存在的函数直接返回。
func (r *PlanReconciler) EnsureBackfillFunction(ctx context.Context, marketType string) (string, error) {
	if err := r.refreshPackage(ctx); err != nil {
		return "", err
	}
	region, ok := r.BackfillRegion(marketType)
	if !ok {
		return "", fmt.Errorf("no SCF region is configured for market %s", marketType)
	}
	name := BackfillFunctionName(r.SpaceID, marketType, region)
	access, err := r.Access(region)
	if err != nil {
		return "", err
	}
	fingerprint := backfillAccessFingerprint(access)
	r.backfillMu.Lock()
	defer r.backfillMu.Unlock()
	// 持有补采锁之后才读取并固定目标代码包：在锁外读取，较早读到旧包的调用等到锁后会把已升级的函数降回旧版本。
	// 期间对账协程解析到新版本不影响这次的判断与创建，下一次调用再跟进。
	pkg := r.packageID()
	if pkg != "" && r.backfillReady[name] == pkg+"|"+fingerprint {
		return name, nil
	}
	// 上一次调用留下的在途作业：先结算。仍在运行就不重复提交，也不碰租约；到终态后继续按云端实际状态判断。
	if busy, settleErr := r.settleBackfillJob(ctx, name); settleErr != nil {
		return "", settleErr
	} else if busy {
		return "", fmt.Errorf("backfill function %s has an operation still running (%w)", name, errBackfillJobRunning)
	}
	release, err := r.acquireOperation(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	nodes, err := r.Cloud.ListMarketFetchers(ctx, r.SpaceID)
	if err != nil {
		return "", fmt.Errorf("list market fetchers: %w", err)
	}
	for _, node := range nodes {
		if node.InstanceID != name {
			continue
		}
		if fmt.Sprint(node.Metadata[backfillAccessMetadataKey]) != fingerprint {
			// 访问入口或调用方身份变了（主机改名、密钥轮换）：补采函数没有定时器，不能走运行配置更新，删掉后按当前身份重建。
			if err := r.deleteBackfillFunction(ctx, name); err != nil {
				return "", err
			}
			break
		}
		if strings.TrimSpace(node.PackageID) != "" && node.PackageID != pkg {
			if err := r.upgradeBackfillCode(ctx, name, pkg); err != nil {
				return "", err
			}
		}
		r.markBackfillReady(name, pkg+"|"+fingerprint)
		return name, nil
	}
	if cleared, clearErr := r.clearStale(ctx, "invoke", []string{name}, true); clearErr != nil {
		return "", clearErr
	} else if !cleared {
		return "", fmt.Errorf("backfill function %s has an unfinished create reservation", name)
	}
	network, ok := r.Deploy.Networks[region]
	if !ok || network.VPCID == "" || network.SubnetID == "" {
		return "", fmt.Errorf("region %s has no VPC and subnet configured", region)
	}
	environment := map[string]string{"MOOX_MARKET_FETCH_MODE": "backfill", "MOOX_FETCH_TIMEOUT_SECONDS": strconv.Itoa(tencent.CollectorTimerTimeoutSeconds)}
	for _, key := range []string{"MOOX_ACCESS_ADDRESS", "MOOX_ACCESS_ID", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		if value, ok := access[key]; ok {
			environment[key] = value
		}
	}
	for key, value := range r.Deploy.BaseEnvironment {
		if _, set := environment[key]; !set {
			environment[key] = value
		}
	}
	memory := r.Deploy.MemoryMB
	if memory <= 0 {
		memory = 64
	}
	reservation := sha256.Sum256([]byte("backfill|" + r.SpaceID + "|" + name))
	item := &cloudfunctionpb.FunctionInstanceCreateItem{
		CloudAccountId: r.Deploy.CloudAccountID, FunctionType: "scf-event", TriggerType: "invoke", Runtime: "Go1", Handler: "main",
		Namespace: r.Deploy.Namespace, Region: region, PackageId: pkg, Environment: environment,
		Config: map[string]string{
			"timeout": strconv.Itoa(tencent.CollectorTimerTimeoutSeconds), "memory_size": strconv.Itoa(memory), "max_instance_concurrency": "1",
			"vpc_id": network.VPCID, "subnet_id": network.SubnetID, "public_net_status": "ENABLE",
			"reserved_concurrency_mem": strconv.Itoa(BackfillReservedConcurrency * memory),
		},
		Metadata:            mustStruct(map[string]any{"function_name": name, "instance_id": name, "workload_type": "market_fetcher", "function_mode": "kline", "memory_size": memory, "timeout_seconds": tencent.CollectorTimerTimeoutSeconds, backfillAccessMetadataKey: fingerprint}),
		CreateReservationId: hex.EncodeToString(reservation[:16]),
	}
	items, err := r.runBackfillJob(ctx, name, "create", "is still being created", func(lease *scfinvoker.CollectorPublishLease) (string, error) {
		item.CollectorPublishLeaseId, item.CollectorPublishFencingToken = lease.LeaseID, lease.FencingToken
		return r.Cloud.SubmitCreateFunctionInstances(ctx, r.SpaceID, []*cloudfunctionpb.FunctionInstanceCreateItem{item})
	})
	if err != nil {
		return "", err
	}
	for _, result := range items {
		if !result.Success {
			return "", fmt.Errorf("create backfill function %s: %s", name, result.Message)
		}
	}
	r.markBackfillReady(name, pkg+"|"+fingerprint)
	return name, nil
}

// upgradeBackfillCode 把已存在的补采函数升级到配置的代码包（调用方已持有操作槽）。
func (r *PlanReconciler) upgradeBackfillCode(ctx context.Context, name, pkg string) error {
	items, err := r.runBackfillJob(ctx, name, "upgrade", "code upgrade is still running", func(lease *scfinvoker.CollectorPublishLease) (string, error) {
		return r.Cloud.SubmitDeployFunctionInstances(ctx, r.SpaceID, []string{name}, pkg, lease)
	})
	if err != nil {
		return err
	}
	for _, result := range items {
		if !result.Success {
			return fmt.Errorf("upgrade backfill function %s: %s", name, result.Message)
		}
	}
	return nil
}

// deleteBackfillFunction 删除已存在的补采函数（调用方已持有操作槽），之后由调用方重新创建。
func (r *PlanReconciler) deleteBackfillFunction(ctx context.Context, name string) error {
	items, err := r.runBackfillJob(ctx, name, "delete", "deletion is still running", func(lease *scfinvoker.CollectorPublishLease) (string, error) {
		return r.Cloud.SubmitDeleteFunctionInstances(ctx, r.SpaceID, []string{name}, lease)
	})
	if err != nil {
		return err
	}
	for _, result := range items {
		if !result.Success {
			return fmt.Errorf("delete backfill function %s: %s", name, result.Message)
		}
	}
	return nil
}

// backfillAccessMetadataKey 是补采函数元数据里记录创建时 Access 入口与调用方身份指纹的键。
const backfillAccessMetadataKey = "access_fingerprint"

// backfillAccessFingerprint 由 Access 地址、实例、调用方与密钥 ID 算出指纹，不含密钥本身。
func backfillAccessFingerprint(access map[string]string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{access["MOOX_ACCESS_ADDRESS"], access["MOOX_ACCESS_ID"], access["MOOX_CALLER"], access["MOOX_CALLER_KEY_ID"]}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// markBackfillReady 记下补采函数已确认的“代码包|Access 指纹”。
func (r *PlanReconciler) markBackfillReady(name, key string) {
	if r.backfillReady == nil {
		r.backfillReady = map[string]string{}
	}
	r.backfillReady[name] = key
}

// ensureBackfillLease 返回补采租约：已持有的租约先续租，续租失败（已过期、被取代、通信失败）就丢弃重新申请。
// 有在途补采作业时租约一直保留，多个函数的作业共用它。
func (r *PlanReconciler) ensureBackfillLease(ctx context.Context) (*scfinvoker.CollectorPublishLease, error) {
	if r.backfillLease != nil {
		renewed, err := r.renewBackfillLease(ctx)
		if err == nil {
			return renewed, nil
		}
		// 只有明确得知租约已过期或被取代才丢弃句柄；通信错误时租约可能仍然有效，丢掉句柄后 Admin 会拒绝重新申请
		// （租约未过期），在途作业也失去续租保护。保留句柄，把错误交给调用方，下次再试。
		if !errors.Is(err, scfinvoker.ErrCollectorPublishLeaseStale) {
			return nil, err
		}
		r.backfillLease = nil
	}
	if r.backfillHolder == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return nil, err
		}
		r.backfillHolder = "backfill-" + hex.EncodeToString(raw[:])
	}
	leaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	lease, err := r.Cloud.AcquireCollectorPublishLease(leaseCtx, r.SpaceID, r.backfillHolder)
	if err != nil {
		return nil, err
	}
	if lease == nil || lease.LeaseID == "" || lease.FencingToken < 1 {
		return nil, errors.New("publish lease response is incomplete")
	}
	r.backfillLease = lease
	return lease, nil
}

func (r *PlanReconciler) renewBackfillLease(ctx context.Context) (*scfinvoker.CollectorPublishLease, error) {
	leaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	renewed, err := r.Cloud.RenewCollectorPublishLease(leaseCtx, r.backfillLease)
	if err != nil {
		return nil, err
	}
	if renewed != nil {
		r.backfillLease = renewed
	}
	return r.backfillLease, nil
}

func (r *PlanReconciler) releaseBackfillLease(ctx context.Context) {
	if r.backfillLease == nil {
		return
	}
	leaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = r.Cloud.ReleaseCollectorPublishLease(leaseCtx, r.backfillLease)
	r.backfillLease = nil
}

// releaseBackfillLeaseIfIdle 只在没有在途补采作业时释放租约：租约一释放就立即过期，
// 排队中的作业开始执行时会因租约已过期而失败，下一次调用还会对同一函数重复提交。
func (r *PlanReconciler) releaseBackfillLeaseIfIdle(ctx context.Context) {
	if len(r.backfillJobs) == 0 {
		r.releaseBackfillLease(ctx)
	}
}

// runBackfillJob 取得补采租约，提交一个作业并等待。调用方须已持有 backfillMu。
// 作业在等待上限内结束就释放租约（没有其他在途作业时）并返回结果；
// 没有结束，或读取结果出错时作业仍可能在云端运行：登记在途并保留租约，返回 errBackfillJobRunning
// （读取出错时返回该错误），由后续调用经 settleBackfillJob 结算。
func (r *PlanReconciler) runBackfillJob(ctx context.Context, name, kind, running string, submit func(lease *scfinvoker.CollectorPublishLease) (string, error)) ([]scfinvoker.BatchItemResult, error) {
	lease, err := r.ensureBackfillLease(ctx)
	if err != nil {
		return nil, fmt.Errorf("publish lease: %w", err)
	}
	jobID, err := submit(lease)
	if err != nil {
		r.releaseBackfillLeaseIfIdle(ctx)
		return nil, err
	}
	items, terminal, err := r.waitJob(ctx, jobID)
	if err != nil || !terminal {
		if r.backfillJobs == nil {
			r.backfillJobs = map[string]backfillJob{}
		}
		r.backfillJobs[name] = backfillJob{id: jobID, kind: kind, at: r.now()}
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("backfill function %s %s (%w)", name, running, errBackfillJobRunning)
	}
	r.releaseBackfillLeaseIfIdle(ctx)
	return items, nil
}

// backfillJobGiveUpAge 是在途补采作业的放弃期限。FunctionInstance 单个节点批次项的总预算是 16 分钟，超过它仍无法确认终态
// （作业记录丢失、状态一直读不到）就不再等：停止续租、移除在途记录，再按云端实际状态判断下一步。
// 旧作业即使还活着也会被新租约的围栏令牌隔离；不能更早放弃，否则会对仍在运行的作业重复提交。
const backfillJobGiveUpAge = 30 * time.Minute

// settleBackfillJob 结算函数上一次调用留下的在途作业，调用方须已持有 backfillMu。
// busy 为 true 表示作业仍在运行或结果暂时读不到（都会尽力续租，防止排队中的作业因租约过期失败）；
// 到终态就移出在途集合，没有其他在途作业时释放租约。作业结果不在这里判断：调用方随后读取云端实际状态，
// 缺失就重建，失败就重试。超过放弃期限仍无法确认终态时见 backfillJobGiveUpAge。
func (r *PlanReconciler) settleBackfillJob(ctx context.Context, name string) (busy bool, err error) {
	job, ok := r.backfillJobs[name]
	if !ok {
		return false, nil
	}
	age := r.now().Sub(job.at)
	if age > backfillJobGiveUpAge {
		log.WarnContextf(ctx, "collector backfill gives up waiting for a cloud job space=%s function=%s job=%s kind=%s age=%s", r.SpaceID, name, job.id, job.kind, age.Round(time.Second))
		delete(r.backfillJobs, name)
		r.releaseBackfillLeaseIfIdle(ctx)
		return false, nil
	}
	_, terminal, err := r.checkJob(ctx, job.id)
	if err != nil {
		// 结果暂时读不到：作业仍可能在运行，同样要续租，否则持续重试超过租约 TTL 后租约过期。
		r.keepBackfillLeaseAlive(ctx, name)
		return true, err
	}
	if !terminal {
		r.keepBackfillLeaseAlive(ctx, name)
		if age > pendingJobWarnAge {
			log.WarnContextf(ctx, "collector backfill cloud job is still not finished space=%s function=%s job=%s kind=%s age=%s", r.SpaceID, name, job.id, job.kind, age.Round(time.Second))
		}
		return true, nil
	}
	delete(r.backfillJobs, name)
	r.releaseBackfillLeaseIfIdle(ctx)
	return false, nil
}

// keepBackfillLeaseAlive 尽力续租在途补采作业持有的租约：失败只记日志，明确得知租约已失效时丢弃句柄。
func (r *PlanReconciler) keepBackfillLeaseAlive(ctx context.Context, name string) {
	if r.backfillLease == nil {
		return
	}
	if _, err := r.renewBackfillLease(ctx); err != nil {
		if errors.Is(err, scfinvoker.ErrCollectorPublishLeaseStale) {
			r.backfillLease = nil
		}
		log.WarnContextf(ctx, "collector backfill renew publish lease for in-flight job space=%s function=%s: %v", r.SpaceID, name, err)
	}
}
