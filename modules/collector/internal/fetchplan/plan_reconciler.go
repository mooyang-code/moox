// Package fetchplan 是采集的控制面：由任务与标签成员算出期望状态，生成采集计划并对账下发，
// 只在 Collector 主进程里运行。执行面（Plan 的编解码、取数流水线、周期提交）在 marketfetch 包，
// 依赖方向只能是 fetchplan → marketfetch。
package fetchplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/adminrpc"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/seriesset"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// planFinalizeMargin 是旧份保留窗口结束后再多等的时间，让最后一批旧周期提交落地。
	planFinalizeMargin = 5 * time.Second
	// planUpgradeCheckInterval 是检查函数代码包是否落后的间隔。
	planUpgradeCheckInterval = 2 * time.Minute
	// planVerifyInterval 是全量读回云端配置的间隔；读回不完整或修复还在跑时按 planVerifyRetry 重试。
	planVerifyInterval = time.Hour
	planVerifyRetry    = 5 * time.Minute
	// planGoneDebounce 是“数据集消失但任务仍启用”被视为真实删除前需要连续观察到的轮数。
	planGoneDebounce = 5
	// planLeadTimeHandoff 是采集方式交接时 P 的最短提前量。
	planLeadTimeHandoff = 3 * time.Minute
	// operationStuckAge 是云端操作的卡住年龄：最早一个在跑的操作超过它，对账不再刷新成功时间，由 Monitor 的对账陈旧判定发现。
	operationStuckAge = 10 * time.Minute
)

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
	// Create、Update 末尾的时间线行与计划记录在同一个事务里写入（见 plan_timeline.go）。
	Create(context.Context, string, string, string, ...store.SeriesTimelineEntry) (*store.FetchPlanRecord, error)
	Update(context.Context, int64, string, string, string, ...store.SeriesTimelineEntry) error
	SavePlan(context.Context, int64, string) error
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
type ActivityEntry = adminrpc.ActivityEntry

// ActivityLogger 写入全局活动日志；相同 DedupKey 的重放必须被忽略。
type ActivityLogger interface {
	AppendActivityLogs(ctx context.Context, spaceID string, entries []ActivityEntry) error
}

// PlanDeploy 是创建与配置函数所需的部署参数。
type PlanDeploy struct {
	CloudAccountID string
	// PackageName 是代码包的包名：每轮取这个包名最新的可用代码包，上传新版本后函数自动升级，不需要改配置。
	PackageName string
	Namespace   string
	MemoryMB    int
	// BaseEnvironment 是函数的非受管环境（空间、Storage 应用密钥、日志服务等）。
	BaseEnvironment map[string]string
	Networks        map[string]PlanNetwork
	// NamespaceCapacity 为每个命名空间的函数数上限（0 表示不分片）。
	NamespaceCapacity int
}

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

// PlanReconciler 是一个空间的采集计划对账器：由任务与标签成员算出期望状态，
// 与已生效的计划比较，有变化时先保存采集计划，再登记快照、下发环境、收尾。
// 改云上状态的调用只经操作表（function_ops.go）。
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
	Metrics  *ControlMetrics
	Now      func() time.Time
	// Workers 读取推送与本地执行端（collector-stream）的确认。
	Workers StreamWorkerSource
	// Exclusions 是采集名单排除：期望状态去掉排除的标的；为空表示没有排除。
	Exclusions SubjectExclusionSource
	// Operations 限制同时进行的云端操作（各空间共用）；为空表示不限制。
	Operations chan struct{}
	// OtherSpaces 是其他已配置云函数的空间各自的市场与地域（空间 → 市场 → 地域）：判断计划外的同前缀函数是不是本空间的，
	// 要排除它们按同样规则能生成的名字。
	OtherSpaces map[string]map[string][]string
	// StreamOnline 判断 collector-stream 是否在线：新计划要给它下发推送或本地执行项时，它不在线就不建计划，
	// 否则生效周期早于它上线，开头缺一段 K 线。为空表示不检查。
	StreamOnline func(ctx context.Context) (bool, error)
	// StartedAt 是 Collector 进程的启动时刻，StreamTimeout 是 collector-stream 的心跳超时。collector-stream 只是旧执行端
	// 且已离线时计划不等它确认（见 confirmStream）；进程启动不满一个心跳超时不这样放行。StartedAt 为零值表示从不放行。
	StartedAt     time.Time
	StreamTimeout time.Duration
	// Preflight 在创建新计划前执行（例如 stockcn 的公共日历覆盖期检查）；返回错误则本轮不创建新计划，已有计划照常续跑。
	Preflight func(ctx context.Context) error
	// Egress 非空时，计划新建了函数之后做一次出口 IP 去重（见 egressbalance 包）：核对云端没有其他问题时探测各函数的出口 IP，
	// 共用 IP 的函数删掉重建。做完或地址池用尽之后不再探测。
	Egress *egressbalance.Balancer

	mu sync.Mutex
	// waiting 是本轮没有建计划的原因，issue 是最近一轮结束时的问题（受 issueMu 保护），collector 状态接口读取。
	waiting string
	issueMu sync.Mutex
	issue   string
	// failCount 统计 fail 的调用次数，roundFailCount 是上一轮对账结束时的值；
	// 两者相等说明自上一轮结束以来没有任何失败（包括同一 tick 内对账之前的启用、升级）。
	failCount      int
	roundFailCount int
	lastVerified   time.Time
	// verifyRequested 由 RPC 删除函数之后置位：下一轮对账立即读回，不等 lastVerified 的间隔。
	verifyRequested atomic.Bool
	// deletedFunctions 是 RPC 删掉的函数名（受 readMu 保护）：未生效计划里已经确认的函数被删后，下一轮去掉它在本阶段的确认、
	// 按阶段期望重推，不等到最终阶段。只在内存里，重启就丢，那时退回到等最终阶段（或读回修复）。
	deletedFunctions map[string]bool
	// 操作表（第一次用时创建）与“正在暂停”的标记：暂停时置位，空间的对账协程重新启动（恢复）时随新的对账器清掉。
	opsOnce    sync.Once
	operations *functionOps
	pausing    atomic.Bool
	// opWaitMax 是一轮里等操作结束的上限（没有截止时间时）；测试里调小。
	opWaitMax time.Duration
	// 最近一次读回（只在内存里）与计划外的同前缀函数，受 readMu 保护。
	readMu    sync.Mutex
	readbacks map[string]FunctionReadback
	unplanned []cloud.FunctionRef
	// 按包名解析出的最新代码包及其解析时间（见 plan_package.go）。
	packageMu       sync.Mutex
	refreshMu       sync.Mutex // 串行化包解析（含云端查询）
	resolvedPackage *cloud.Package
	resolvedAt      time.Time
	packageErr      error     // 最近一次解析失败的原因（没有可用的缓存时），解析成功后清空
	packageWarnedAt time.Time // 上一次记"没有可用代码包"警告的时间
	// lastUpgradeCheck 是上一次检查函数代码包是否落后的时间。
	lastUpgradeCheck time.Time
	goneSeen         map[string]int
	// sleep 替换本轮内的等待（测试用）。
	sleep func(context.Context, time.Duration) error

	// 补采函数：多个补采协程并发调用时串行，backfillReady 缓存已确认就绪的“代码包|Access 指纹”（受 backfillMu 保护）；
	// backfillRefs 是补采函数在云上的位置（受 readMu 保护）。
	backfillMu    sync.Mutex
	backfillReady map[string]string
	backfillRefs  map[string]cloud.FunctionRef
}

// ops 返回本空间的操作表。
func (r *PlanReconciler) ops() *functionOps {
	r.opsOnce.Do(func() {
		r.operations = newFunctionOps(r.Operations, r.pausing.Load, r.now)
	})
	return r.operations
}

// StopOperations 取消本空间全部在跑的云端操作并等它们返回。closing 为 true（进程退出）时之后不再接受新操作；
// 为 false（空间的对账协程退出）时操作表照常可用（暂停期间补采函数与 RPC 删除还要用）。
func (r *PlanReconciler) StopOperations(closing bool) {
	r.ops().stop(closing)
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

// errBackfillFunctionPending 表示补采函数还在创建或升级：调用方稍后重试。
var errBackfillFunctionPending = errors.New("补采函数还在创建或升级")

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

// operationStuck 判断最早一个在跑的云端操作是否已超过卡住年龄。
func (r *PlanReconciler) operationStuck() bool {
	since, ok := r.ops().oldestRunning()
	return ok && r.now().Sub(since) > operationStuckAge
}

// finishRound 在每轮对账结束时统一收尾：有在跑的云端操作就按最早一个的开始时间上报 pending；
// 本轮及自上一轮结束以来没有任何失败时恢复健康并清除遗留的失败原因（计划仍在进行也一样）。
func (r *PlanReconciler) finishRound(err error) {
	// 函数代码升级仍在进行（errCodeUpgradePending）是等待，不是失败。
	clean := (err == nil || errors.Is(err, errCodeUpgradePending)) && r.failCount == r.roundFailCount
	r.roundFailCount = r.failCount
	if r.Metrics == nil {
		return
	}
	if since, ok := r.ops().oldestRunning(); ok {
		r.Metrics.ObserveAssignmentPending(r.SpaceID, true, since)
	}
	if clean {
		r.Metrics.ObserveAssignmentRoundOK(r.SpaceID)
	}
}

// Issue 返回最近一轮对账的问题：失败原因或等待原因，没有问题时为空。
func (r *PlanReconciler) Issue() string {
	r.issueMu.Lock()
	defer r.issueMu.Unlock()
	return r.issue
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
	r.waiting = ""
	defer func() {
		r.finishRound(err)
		issue := r.waiting
		if err != nil {
			issue = err.Error()
		}
		r.issueMu.Lock()
		r.issue = issue
		r.issueMu.Unlock()
	}()
	// 跨轮才结束的操作失败了：本轮照常推进，但结束时按失败返回，不刷新对账成功。
	collectErr := r.collectFinishedOps(ctx)
	r.refreshPackageIfAvailable(ctx)
	if err := r.packageGate(ctx); err != nil {
		return err
	}
	// 代码包升级与采集计划是否在进行无关：计划推进期间也要让落后的函数跟上最新的代码包。升级在操作表里异步进行，不拖住本轮。
	if r.now().Sub(r.lastUpgradeCheck) >= planUpgradeCheckInterval {
		r.lastUpgradeCheck = r.now()
		if err := r.upgradeLaggingFunctions(ctx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan code upgrade check failed space=%s: %v", r.SpaceID, err)
		}
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
		if err := r.reconfirmDeleted(ctx, open); err != nil {
			return err
		}
		if err := r.advance(ctx, open); err != nil {
			return err
		}
		return collectErr
	}
	r.takeDeletedFunctions() // 没有未生效的计划：删掉的函数由读回修复重建（verifyRequested）
	if err := r.planChange(ctx); err != nil {
		return err
	}
	if collectErr != nil {
		return collectErr
	}
	// 没有未生效的计划（或本轮刚把计划推进到生效）：对账成功。
	// 在跑的云端操作只要还没超过卡住年龄就不影响成功：大批量升级可能连续跑十几分钟；
	// 最早的操作卡住时不再刷新成功时间，由 Monitor 的对账陈旧判定（默认 15 分钟）发现。
	if r.Metrics != nil && !r.operationStuck() {
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
		r.waiting = "启用的任务暂时取不到标的"
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
	desired, err := BuildDesiredState(r.Options, groups, previousFunctions)
	if err != nil {
		return r.fail("rules", err)
	}
	// 交接与标的变更分开：有数据集的标的集合在变时，本轮先按已生效的执行端完成标的变更，交接等下一份计划。
	if handoffPending(desired, previousDatasets) && subjectsChanging(desired, previousDatasets) {
		if desired, err = BuildDesiredState(r.Options, appliedIngest(groups, previousDatasets), previousFunctions); err != nil {
			return r.fail("rules", err)
		}
		log.InfoContextf(ctx, "collector fetch plan defers the ingest handoff until the subject change is applied space=%s", r.SpaceID)
	}
	handoff := handoffPending(desired, previousDatasets)
	if r.holdMissingDatasets(ctx, desired, previousDatasets, managed) {
		return nil
	}
	if blocked, err := r.waitForStream(ctx, desired, previousFunctions); blocked || err != nil {
		return err
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
			r.Egress.Arm() // 新建函数的出口 IP 要去重：计划生效后探测一次
			break
		}
	}
	if firstStreamPlan(desired, previousFunctions, previousDatasets) {
		lead = planLeadTimePushOnly
	}
	choose := func(frequency string) (int64, error) {
		return ChoosePeriodStartWithLead(frequency, r.now(), lead)
	}
	content, changed, err := BuildPlanContent(previousFunctions, previousDatasets, desired, choose)
	if err != nil {
		return r.fail("rules", err)
	}
	if !changed {
		return r.verifyFleet(ctx, scfFunctions(previousFunctions))
	}
	content.LeadSeconds = int(lead / time.Second)
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
	// 不再采集的数据集从计划建立起就在时间线上停止，和计划记录一起落库。
	record, err := r.Store.Create(ctx, r.SpaceID, mustJSON(content), mustJSON(progress), content.stoppedTimeline(r.SpaceID)...)
	if errors.Is(err, store.ErrFetchPlanOpen) {
		return nil
	}
	if err != nil {
		return r.fail("store", fmt.Errorf("save fetch plan: %w", err))
	}
	log.InfoContextf(ctx, "collector fetch plan created space=%s plan=%d datasets=%d functions=%d", r.SpaceID, record.ID, len(content.Datasets), len(content.Functions))
	return r.advance(ctx, record)
}

// firstStreamPlan 报告这是空间的第一份计划，并且全部由 collector-stream 执行。
func firstStreamPlan(desired *DesiredState, previousFunctions map[string]*PlanFunction, previousDatasets map[string]*PlanDatasetState) bool {
	if len(previousFunctions) != 0 || len(previousDatasets) != 0 || len(desired.Functions) == 0 {
		return false
	}
	for _, function := range desired.Functions {
		if function.Executor == "" || function.Executor == marketfetch.PlanExecutorSCF {
			return false
		}
	}
	return true
}

// waitForStream 在新计划要给 collector-stream 下发新的推送或本地执行项、而它不在线时返回 true：本轮不建计划。
func (r *PlanReconciler) waitForStream(ctx context.Context, desired *DesiredState, previous map[string]*PlanFunction) (bool, error) {
	if r.StreamOnline == nil {
		return false, nil
	}
	needed := false
	for name, function := range desired.Functions {
		if function.Executor != "" && previous[name] == nil {
			needed = true
			break
		}
	}
	if !needed {
		return false, nil
	}
	online, err := r.StreamOnline(ctx)
	if err != nil {
		return false, r.fail("store", fmt.Errorf("read collector-stream status: %w", err))
	}
	if online {
		return false, nil
	}
	r.waiting = "等 collector-stream 上线"
	log.InfoContextf(ctx, "collector fetch plan waits for collector-stream space=%s", r.SpaceID)
	return true, nil
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
func (r *PlanReconciler) functionEnvironment(region string, plan *marketfetch.Plan) (map[string]string, error) {
	encoded, err := marketfetch.EncodePlan(plan)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(encoded))
	count := 0
	for _, dataset := range plan.Datasets {
		count += len(dataset.Copies[len(dataset.Copies)-1].Subjects)
	}
	environment := map[string]string{
		marketfetch.PlanEnvName:             encoded,
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
		for _, plan := range []*marketfetch.Plan{content.TransitionalPlan(function), content.FinalPlan(function)} {
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
			merged := map[string]string{"MOOX_CODE_PACKAGE_ID": r.packageID()}
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

// saveProgress 保存计划的状态与进度；timeline 是随这次进度一起成立的时间线行，与进度同一个事务。
func (r *PlanReconciler) saveProgress(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress, status, lastError string, timeline ...store.SeriesTimelineEntry) error {
	if err := r.Store.Update(ctx, record.ID, status, mustJSON(progress), lastError, timeline...); err != nil {
		return r.fail("store", fmt.Errorf("save fetch plan progress: %w", err))
	}
	record.Status, record.Progress, record.LastError = status, mustJSON(progress), lastError
	return nil
}

// takeDeletedFunctions 取走 RPC 删掉的函数名。
func (r *PlanReconciler) takeDeletedFunctions() map[string]bool {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	deleted := r.deletedFunctions
	r.deletedFunctions = nil
	return deleted
}

// reconfirmDeleted 把 RPC 删掉、而在未生效计划里已经确认的函数的本阶段确认去掉并保存进度：已确认收尾的去掉 Finalized
// （最终计划与过渡计划相同、收尾时不再下发的，连 Transitioned 一起去掉），只确认了过渡的去掉 Transitioned。随后的推进按阶段期望重推（Ensure 建出来），不等到最终阶段，也不等每小时的读回。
// 调用方持有 r.mu（对账协程）。
func (r *PlanReconciler) reconfirmDeleted(ctx context.Context, record *store.FetchPlanRecord) error {
	deleted := r.takeDeletedFunctions()
	if len(deleted) == 0 {
		return nil
	}
	content, err := decodeFetchPlanContent(record.Plan)
	if err != nil {
		return r.fail("store", err)
	}
	progress, err := decodeFetchPlanProgress(record.Progress)
	if err != nil {
		return r.fail("store", err)
	}
	changed := false
	for name := range deleted {
		function := content.Functions[name]
		if function == nil || !function.IsSCF() {
			continue
		}
		switch {
		case progress.Finalized[name] && r.finalDiffers(content, function):
			delete(progress.Finalized, name)
			changed = true
		case progress.Finalized[name]:
			// 最终计划与过渡计划相同（收尾时不再下发）：去掉两个确认，由过渡阶段按同一份内容重建。
			delete(progress.Finalized, name)
			delete(progress.Transitioned, name)
			changed = true
		case progress.Transitioned[name]:
			delete(progress.Transitioned, name)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	log.InfoContextf(ctx, "collector fetch plan repushes functions deleted through the RPC space=%s plan=%d", r.SpaceID, record.ID)
	return r.saveProgress(ctx, record, progress, record.Status, record.LastError)
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
		// 过渡配置都已下发，只是在等旧周期的窗口结束（1h 最长约一小时，1d 要等到下一个 UTC 零点）：
		// 这段时间按对账成功上报，Monitor 不会把正常的等待判成对账陈旧。过了窗口结束仍没收尾的不再刷新，卡住的计划照样报出来。
		if r.Metrics != nil && r.failCount == r.roundFailCount && !r.operationStuck() {
			r.Metrics.ObserveAssignmentSuccess(r.SpaceID, r.now().Unix())
		}
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
			lead := planLeadTimeWithCreate
			if content.LeadSeconds > 0 {
				lead = time.Duration(content.LeadSeconds) * time.Second
			}
			p, chooseErr := ChoosePeriodStartWithLead(change.Frequency, r.now(), lead)
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
		// Storage 已确认登记：登记标记与时间线上“从 P 起是这份序列”的一行一起落库。P 只在登记之前重选，
		// 所以写进时间线的生效点不会再变。
		progress.mark(&progress.Registered, key)
		if err := r.saveProgress(ctx, record, progress, record.Status, "", seriesTimelineEntry(r.SpaceID, change)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// pushFunctions 下发过渡（或最终）阶段：每个云函数一个 Ensure（建函数与下发环境、定时器合成一次），退役的函数只停定时器；
// 推送与本地执行项靠 collector-stream 心跳确认。全部确认后返回 true。
// 对每个云函数只有三种情况：没有操作在跑就起一个；有操作在跑就本轮跳过（不算失败）；
// 操作已结束：成功且用途是本阶段就记确认，失败就按原因上报、下一轮重来。
func (r *PlanReconciler) pushFunctions(ctx context.Context, record *store.FetchPlanRecord, content *FetchPlanContent, progress *FetchPlanProgress, final bool) (bool, error) {
	confirmed, stage := &progress.Transitioned, planStageTransition
	if final {
		confirmed, stage = &progress.Finalized, planStageFinal
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
	var cloudNames, streamItems []string
	for _, name := range todo {
		if content.Functions[name].IsSCF() {
			cloudNames = append(cloudNames, name)
		} else {
			streamItems = append(streamItems, name)
		}
	}
	if len(streamItems) > 0 {
		if err := r.confirmStream(ctx, record, content, progress, streamItems, final); err != nil {
			return false, err
		}
	}
	if len(cloudNames) == 0 {
		return r.allConfirmed(content, progress, final), nil
	}
	key := planOpKey(record.ID, stage)
	ops := r.ops()
	changed := false
	var failures []string
	var firstErr error
	settle := func(name string, op *functionOp) {
		if op.confirmed() {
			progress.mark(confirmed, name)
			changed = true
			return
		}
		if op.err != nil {
			failures = append(failures, name+": "+op.err.Error())
			if firstErr == nil {
				firstErr = op.err
			}
		}
		// 定时器因为暂停被设成停用的成功不算确认：下一轮（恢复之后）重推。
	}
	var started []*functionOp
	names := map[string]string{} // 操作表的键 → 函数名
	for _, name := range cloudNames {
		opName := refKey(r.functionRef(name, content.Functions[name].Region))
		names[opName] = name
		if op := ops.take(opName, key); op != nil {
			settle(name, op)
			if op.confirmed() || op.err != nil {
				continue
			}
		}
		if ops.busy(opName) {
			continue // 另一个操作在跑：本轮跳过，等它结束
		}
		op, err := r.startStage(content, content.Functions[name], key, stage)
		switch {
		case errors.Is(err, errOpBusy), errors.Is(err, errOpPausing), errors.Is(err, errOpsClosed):
			continue
		case err != nil:
			// 期望内容算不出来（环境、网络配置）：不碰云端，按环境问题上报。
			_ = r.saveProgress(ctx, record, progress, record.Status, err.Error())
			return false, r.fail("environment", fmt.Errorf("function %s: %w", name, err))
		}
		started = append(started, op)
	}
	ops.wait(ctx, started, r.roundWait(ctx))
	for _, op := range started {
		if done := ops.take(op.name, key); done != nil {
			settle(names[op.name], done)
		}
	}
	if changed || len(failures) > 0 {
		lastError := strings.Join(failures, "; ")
		if err := r.saveProgress(ctx, record, progress, record.Status, lastError); err != nil {
			return false, err
		}
	}
	if firstErr != nil {
		return false, r.opFailure(fmt.Errorf("fetch plan %d %s stage: %d functions failed, first: %w", record.ID, stage, len(failures), firstErr))
	}
	return r.allConfirmed(content, progress, final), nil
}

// startStage 为计划推进起一个操作：期望内容由调用方传入的阶段决定；退役的函数（最终阶段没有最终计划）只停定时器。
func (r *PlanReconciler) startStage(content *FetchPlanContent, function *PlanFunctionChange, key, stage string) (*functionOp, error) {
	target := stageTarget(content, function, stage)
	if stage == planStageFinal && content.FinalPlan(function) == nil {
		return r.startDisableTimer(r.functionRef(function.Name, function.Region), key)
	}
	if target.Plan == nil {
		return nil, fmt.Errorf("function %s has no plan for the %s stage", function.Name, stage)
	}
	spec, err := r.desiredSpec(target)
	if err != nil {
		return nil, err
	}
	return r.startEnsure(key, spec)
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
// 固定规则：计划里没有数据集正切到推送或本地执行端（collector-stream 只是旧执行端，或数据集留在它上面只是标的变了），
// 并且 collector-stream 已离线（心跳超时）时，这份计划不等它确认；有数据集正切到它上面的计划仍要等。
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
	ok := false
	if !movesToWorker(content) {
		offline, err := r.streamOffline(ctx)
		if err != nil {
			return err
		}
		ok = offline
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

// streamOffline 判断 collector-stream 是否已离线到可以不等它确认：心跳超时，并且 Collector 进程启动已满一个心跳超时。
// 重启后心跳表里的时间是旧的，要先给它一次上报的机会。
func (r *PlanReconciler) streamOffline(ctx context.Context) (bool, error) {
	if r.StreamOnline == nil || r.StartedAt.IsZero() || r.now().Sub(r.StartedAt) < r.StreamTimeout {
		return false, nil
	}
	online, err := r.StreamOnline(ctx)
	if err != nil {
		return false, r.fail("store", fmt.Errorf("read collector-stream status: %w", err))
	}
	return !online, nil
}

// movesToWorker 判断计划里有数据集正从别的执行端切到推送或本地执行端（collector-stream 是新执行端）。
func movesToWorker(content *FetchPlanContent) bool {
	for _, change := range content.Datasets {
		if change.Changed && change.OldIngest != "" && change.IngestOrSCF() != marketfetch.PlanExecutorSCF {
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

// appliedIngest 把已生效数据集的任务组还原成它们现在的执行端（推迟交接），新数据集保持任务的采集方式。
func appliedIngest(groups []TaskGroup, previous map[string]*PlanDatasetState) []TaskGroup {
	current := map[string]string{}
	for _, state := range previous {
		current[state.DatasetID] = state.IngestOrSCF()
	}
	result := append([]TaskGroup(nil), groups...)
	for i := range result {
		if ingest, ok := current[result[i].DatasetID]; ok {
			result[i].Ingest = ingest
		}
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

// verifyUntouched 在有未生效计划时，对不受该计划影响的已生效函数做云端校验。
func (r *PlanReconciler) verifyUntouched(ctx context.Context, open *store.FetchPlanRecord) error {
	if !r.verifyRequested.Load() && !r.lastVerified.IsZero() && r.now().Sub(r.lastVerified) < planVerifyInterval {
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

// upgradeLaggingFunctions 是两分钟一次的代码包升级检查：不做完整读回，优先用内存里最近一次读回的包 ID，没有时只调 DescribeCode。
// 落后的函数分批起 UpgradeCode 操作（只换代码，环境取云上读回的值，不动定时器），不等它们结束。
func (r *PlanReconciler) upgradeLaggingFunctions(ctx context.Context) error {
	pkg := strings.TrimSpace(r.packageID())
	if pkg == "" {
		return nil
	}
	refs, err := r.timerFunctionRefs(ctx)
	if err != nil {
		return err
	}
	targets, err := r.confirmedTargets(ctx)
	if err != nil {
		return err
	}
	started := 0
	for _, name := range sortedKeys(refs) {
		if started >= runtimeConfigBatchSize {
			break // 每次检查最多一批，下一次检查接着升级
		}
		if r.ops().busy(refKey(refs[name])) {
			continue
		}
		readback, ok := r.cachedReadback(refs[name])
		if !ok || readback.Error != "" {
			state, err := r.Cloud.DescribeCode(ctx, refs[name])
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan cannot read the code package of %s space=%s: %v", name, r.SpaceID, err)
				continue
			}
			readback = FunctionReadback{FunctionState: state, CodeOnly: true}
			r.storeReadback(refs[name], readback)
		}
		if !readback.Exists || strings.TrimSpace(readback.PackageID) == "" || readback.PackageID == pkg {
			continue
		}
		var fallback *cloud.FunctionSpec
		if target, ok := targets[name]; ok {
			if spec, err := r.desiredSpec(target); err == nil {
				fallback = &spec
			}
		}
		if _, err := r.startUpgrade(refs[name], fallback); err == nil {
			started++
		}
	}
	if started > 0 {
		log.WarnContextf(ctx, "collector fetch plan upgrading function code space=%s package=%s functions=%d", r.SpaceID, pkg, started)
	}
	return nil
}

// timerFunctionRefs 返回已生效计划与未生效计划里的全部定时函数在云上的位置。
func (r *PlanReconciler) timerFunctionRefs(ctx context.Context) (map[string]cloud.FunctionRef, error) {
	refs := map[string]cloud.FunctionRef{}
	for _, load := range []func(context.Context, string) (*store.FetchPlanRecord, error){r.Store.LatestApplied, r.Store.Open} {
		record, err := load(ctx, r.SpaceID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		content, err := decodeFetchPlanContent(record.Plan)
		if err != nil {
			return nil, err
		}
		for name, function := range content.Functions {
			if function.IsSCF() {
				refs[name] = r.functionRef(name, function.Region)
			}
		}
	}
	return refs, nil
}

// verifyFleet 每小时读回一次已生效计划的函数：不存在、受管环境（分配哈希、Access 三项）或定时器（cron、开关、可用状态、
// Qualifier、Message）不一致的起 Ensure（期望内容按已确认的状态算）；只是代码包落后的起 UpgradeCode。
// 同时停掉计划外的同前缀函数的定时器。有函数这次没读到或修复还在跑时，5 分钟后再读，不宣布核对完成。
func (r *PlanReconciler) verifyFleet(ctx context.Context, applied map[string]*PlanFunction) error {
	requested := r.verifyRequested.Load()
	if !requested && !r.lastVerified.IsZero() && r.now().Sub(r.lastVerified) < planVerifyInterval {
		return nil
	}
	r.verifyRequested.Store(false)
	if len(applied) == 0 {
		// 没有已生效的云函数（例如全部切到了推送）：计划外的同前缀函数照样要停。
		if err := r.disableUnplanned(ctx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan cannot list unplanned functions space=%s: %v", r.SpaceID, err)
			r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
			return nil
		}
		r.lastVerified = r.now()
		return nil
	}
	targets, err := r.confirmedTargets(ctx)
	if err != nil {
		return r.fail("store", err)
	}
	pkg := strings.TrimSpace(r.packageID())
	ops := r.ops()
	var repair, upgrade, present []string
	missing, unverified := false, 0
	specs := map[string]cloud.FunctionSpec{}
	for _, name := range sortedKeys(applied) {
		target, ok := targets[name]
		if !ok {
			continue
		}
		spec, err := r.desiredSpec(target)
		if err != nil {
			return r.fail("environment", fmt.Errorf("function %s: %w", name, err))
		}
		specs[name] = spec
		if ops.busy(refKey(spec.Ref)) {
			unverified++
			continue
		}
		state, err := r.describe(ctx, spec.Ref)
		if err != nil {
			log.WarnContextf(ctx, "collector fetch plan cannot read back %s space=%s: %v", name, r.SpaceID, err)
			unverified++
			continue
		}
		switch {
		case !state.Exists:
			missing = true
			repair = append(repair, name)
		case managedDrift(state.Environment, spec.Environment) || !TimerMatches(state.Timer, target.Cron):
			present = append(present, name)
			repair = append(repair, name)
		case pkg != "" && state.PackageID != pkg:
			present = append(present, name)
			upgrade = append(upgrade, name)
		default:
			present = append(present, name)
		}
	}
	if missing {
		// 云端函数被删要重建：新建出来的函数同样要做一次出口 IP 去重。
		r.Egress.Arm()
	}
	if err := r.disableUnplanned(ctx); err != nil {
		log.WarnContextf(ctx, "collector fetch plan cannot list unplanned functions space=%s: %v", r.SpaceID, err)
		unverified++
	}
	// 没有其他问题时才做出口 IP 去重：共用 IP 的函数先删再按期望内容重建；重建后几分钟再探测下一轮。
	var egress []string
	if len(repair) == 0 && len(upgrade) == 0 && unverified == 0 {
		egress = r.Egress.Next(ctx, r, r.SpaceID, present, r.now())
	}
	if len(repair) == 0 && len(upgrade) == 0 && len(egress) == 0 {
		if unverified > 0 || r.Egress.Pending() {
			r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
			return nil
		}
		r.lastVerified = r.now()
		return nil
	}
	log.WarnContextf(ctx, "collector fetch plan repairing space=%s repair=%s upgrade=%s egress=%s", r.SpaceID, strings.Join(repair, ","), strings.Join(upgrade, ","), strings.Join(egress, ","))
	type pending struct {
		op  *functionOp
		key string
	}
	var started []pending
	startOp := func(op *functionOp, err error, key string) {
		if err == nil {
			started = append(started, pending{op: op, key: key})
		} else {
			unverified++
		}
	}
	for _, name := range repair {
		op, err := r.startEnsure(opKeyRepair, specs[name])
		startOp(op, err, opKeyRepair)
	}
	for _, name := range upgrade {
		spec := specs[name]
		op, err := r.startUpgrade(spec.Ref, &spec)
		startOp(op, err, opKeyUpgrade)
	}
	for _, name := range egress {
		spec := specs[name]
		op, err := r.ops().start(refKey(spec.Ref), opKeyEgress, true, func(ctx context.Context, op *functionOp) error {
			if err := r.Cloud.Delete(ctx, spec.Ref); err != nil {
				return err
			}
			spec, err := r.withPackage(ctx, op, spec)
			if err != nil {
				return err
			}
			spec.Pausing = r.pausingHook(op)
			return r.Cloud.Ensure(ctx, spec)
		})
		startOp(op, err, opKeyEgress)
	}
	ops.wait(ctx, func() []*functionOp {
		out := make([]*functionOp, 0, len(started))
		for _, item := range started {
			out = append(out, item.op)
		}
		return out
	}(), r.roundWait(ctx))
	var firstErr error
	failed := 0
	for _, item := range started {
		done := ops.take(item.op.name, item.key)
		switch {
		case done == nil:
			unverified++ // 还在跑：结束前不会重复起，5 分钟后再读回
		case done.err != nil:
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s %s: %w", item.key, item.op.name, done.err)
			}
		}
	}
	if failed > 0 {
		// 有失败项：不更新读回时间，下一轮继续修复。
		return r.opFailure(fmt.Errorf("repair of drifted or missing functions did not fully succeed (%d failed): %w", failed, firstErr))
	}
	if unverified > 0 || len(egress) > 0 || r.Egress.Pending() {
		r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
		return nil
	}
	r.lastVerified = r.now()
	return nil
}

// disableUnplanned 列出计划外的同前缀函数并停掉它们的定时器，不删除：旧部署留下的函数会继续按旧计划拉行情源，
// 和新函数挤同一批出口 IP 的限流额度，还占函数个数与命名空间配额。由人用 collector function delete 清理。
func (r *PlanReconciler) disableUnplanned(ctx context.Context) error {
	planned, err := r.plannedNames(ctx)
	if err != nil {
		return err
	}
	unplanned, err := r.scanUnplanned(ctx, planned)
	if err != nil {
		return err
	}
	for _, ref := range unplanned {
		if r.ops().busy(refKey(ref)) {
			continue
		}
		// 定时器已经停用（或没有）的不再发停用，也不记日志：每小时重发一遍会刷屏。读不到就照常发一次停用。
		if state, err := r.describe(ctx, ref); err == nil && (!state.Exists || state.Timer == nil || !state.Timer.Enabled) {
			continue
		}
		// 结果在下一轮由 collectFinishedOps 取走：成功才记“已停”，失败按 cloud 报。
		_, _ = r.startDisableTimer(ref, opKeyUnplanned)
	}
	return nil
}

// collectFinishedOps 取走跨轮才结束的读回修复、升级、出口去重、计划外停用的结果（当轮没等到、也没别的取走方），
// 失败计入对账失败（opFailure），不让卡了十几分钟才失败的操作悄悄消失。只在持有 r.mu 的对账协程里调用。
// 计划推进（plan=…）、补采、删除、停用、启用的结果各有取走方，这里不动。
func (r *PlanReconciler) collectFinishedOps(ctx context.Context) error {
	var first error
	for _, op := range r.ops().takeFinished(opKeyRepair, opKeyUpgrade, opKeyEgress, opKeyUnplanned) {
		if op.err != nil {
			log.WarnContextf(ctx, "collector fetch plan cloud operation failed space=%s operation=%s function=%s: %v", r.SpaceID, op.key, op.name, op.err)
			if err := r.opFailure(fmt.Errorf("%s %s: %w", op.key, op.name, op.err)); first == nil {
				first = err
			}
			continue
		}
		if op.key == opKeyUnplanned {
			log.WarnContextf(ctx, "collector fetch plan stopped the timer of an unplanned function space=%s function=%s; delete it with moox-cli collector function delete", r.SpaceID, op.name)
		}
	}
	return first
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
	return fmt.Sprintf("moox-fetch%sbackfill-%s-%s-%s", marketfetch.PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(spaceID)),
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

// backfillPrefix 是本空间补采函数的名字前缀。
func (r *PlanReconciler) backfillPrefix() string {
	return fmt.Sprintf("moox-fetch%sbackfill-%s-", marketfetch.PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(r.SpaceID)))
}

// backfillRef 返回某市场补采函数在云上的位置（补采函数不分片，在配置的命名空间里）。
func (r *PlanReconciler) backfillRef(marketType, region string) cloud.FunctionRef {
	return cloud.FunctionRef{AccountID: r.Deploy.CloudAccountID, Region: region, Namespace: r.Deploy.Namespace, Name: BackfillFunctionName(r.SpaceID, marketType, region)}
}

// backfillSpec 是补采函数的期望内容：不带定时器与采集计划，同步调用，预留并发 5 份内存硬性限制并发实例数。
func (r *PlanReconciler) backfillSpec(ref cloud.FunctionRef, access map[string]string) (cloud.FunctionSpec, error) {
	network, ok := r.Deploy.Networks[ref.Region]
	if !ok || network.VPCID == "" || network.SubnetID == "" {
		return cloud.FunctionSpec{}, fmt.Errorf("region %s has no VPC and subnet configured", ref.Region)
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
	memory := r.memoryMB()
	return cloud.FunctionSpec{
		Ref: ref, Environment: environment, MemoryMB: int64(memory), TimeoutS: tencent.CollectorTimerTimeoutSeconds,
		VPCID: network.VPCID, SubnetID: network.SubnetID, ReservedMem: int64(BackfillReservedConcurrency * memory),
	}, nil
}

// EnsureBackfillFunction 保证该市场的补采函数就绪（与定时函数同一代码包，不带定时器），返回函数名。
// 就绪缓存里已有它、并且代码包与 Access 指纹（Access 入口与调用方身份）都没变就直接返回；否则没有操作在跑就起一个 Ensure，
// 本次调用等一会儿，做完了就返回函数名，还在做就返回“仍在创建”（errBackfillFunctionPending），调用方下一轮再试。
// 补采函数没有定时器，暂停期间照常创建（恢复追赶时系统暂停还开着，补采却要跑）。
func (r *PlanReconciler) EnsureBackfillFunction(ctx context.Context, marketType string) (string, error) {
	if err := r.refreshPackage(ctx); err != nil {
		return "", err
	}
	region, ok := r.BackfillRegion(marketType)
	if !ok {
		return "", fmt.Errorf("no SCF region is configured for market %s", marketType)
	}
	if r.Access == nil {
		return "", errors.New("SCF Access environment is required")
	}
	access, err := r.Access(region)
	if err != nil {
		return "", err
	}
	ref := r.backfillRef(marketType, region)
	name := ref.Name
	fingerprint := backfillAccessFingerprint(access)
	key := opKeyBackfill + "|" + fingerprint
	r.readMu.Lock()
	if r.backfillRefs == nil {
		r.backfillRefs = map[string]cloud.FunctionRef{}
	}
	r.backfillRefs[name] = ref
	r.readMu.Unlock()
	r.backfillMu.Lock()
	defer r.backfillMu.Unlock()
	// 持有补采锁之后才读取目标代码包：在锁外读取，较早读到旧包的调用等到锁后会把已升级的函数降回旧版本。
	pkg := r.packageID()
	ready := func() bool { return pkg != "" && r.backfillReady[name] == pkg+"|"+fingerprint }
	settle := func(op *functionOp) error {
		if op.err != nil {
			return fmt.Errorf("ensure backfill function %s: %w", name, op.err)
		}
		r.markBackfillReady(name, op.packageID+"|"+fingerprint)
		return nil
	}
	if ready() {
		return name, nil
	}
	if op := r.ops().take(refKey(ref), key); op != nil {
		if err := settle(op); err != nil {
			return "", err
		}
		if ready() {
			return name, nil
		}
	}
	if r.ops().busy(refKey(ref)) {
		return "", fmt.Errorf("backfill function %s: %w", name, errBackfillFunctionPending)
	}
	spec, err := r.backfillSpec(ref, access)
	if err != nil {
		return "", err
	}
	op, err := r.ops().start(refKey(ref), key, false, func(ctx context.Context, op *functionOp) error {
		spec, err := r.withPackage(ctx, op, spec)
		if err != nil {
			return err
		}
		return r.Cloud.Ensure(ctx, spec)
	})
	if err != nil {
		return "", fmt.Errorf("backfill function %s: %w", name, err)
	}
	r.ops().wait(ctx, []*functionOp{op}, r.roundWait(ctx))
	done := r.ops().take(refKey(ref), key)
	if done == nil {
		return "", fmt.Errorf("backfill function %s: %w", name, errBackfillFunctionPending)
	}
	if err := settle(done); err != nil {
		return "", err
	}
	return name, nil
}

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
