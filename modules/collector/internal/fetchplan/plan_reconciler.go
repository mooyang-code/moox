// Package fetchplan 是采集的控制面：由任务与标签成员算出期望状态，生成采集计划并对账下发，
// 只在 Collector 主进程里运行。执行面（Plan 的编解码、取数流水线、周期提交）在 marketfetch 包，
// 依赖方向只能是 fetchplan → marketfetch。
package fetchplan

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/adminrpc"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// planUpgradeCheckInterval 是检查函数代码包是否落后的间隔。
	planUpgradeCheckInterval = 2 * time.Minute
	// planVerifyInterval 是全量读回云端配置的间隔；读回不完整或修复还在跑时按 planVerifyRetry 重试。
	planVerifyInterval = time.Hour
	planVerifyRetry    = 5 * time.Minute
	// planGoneDebounce 是“数据集消失但任务仍启用”被视为真实删除前需要连续观察到的轮数。
	planGoneDebounce = 5
	// planLeadTimeHandoff 是采集方式交接时 P 的最短提前量。
	planLeadTimeHandoff = 3 * time.Minute
	// operationStuckAge 是云端操作的卡住年龄：最早一个在跑的操作超过它，对账不再刷新成功时间，由对账陈旧判定（关注项 fetch_coordination_failing）发现。
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

type planStore interface {
	Open(context.Context, string) (*store.FetchPlanRecord, error)
	LatestApplied(context.Context, string) (*store.FetchPlanRecord, error)
	// Create、Update 末尾的时间线行与计划记录在同一个事务里写入（见 plan_timeline.go）。
	Create(context.Context, string, string, string, ...store.SeriesTimelineEntry) (*store.FetchPlanRecord, error)
	Update(context.Context, int64, string, string, string, ...store.SeriesTimelineEntry) error
	SavePlan(context.Context, int64, string) error
	// DeleteOpen 删掉一份 pending 的未生效计划（数据集全部撤回之后已经没有变化时）。
	DeleteOpen(context.Context, int64) error
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
	// BaseEnvironment 是函数的非受管环境（空间、日志服务等）。
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

// PlanReconciler 是一个空间的采集计划对账器：由任务与标签成员算出期望状态，按数据集与已生效状态比较（决策表），
// 有变化时先保存采集计划，再登记快照、下发；下发确认就生效。
// 改云上状态的调用只经操作表（function_ops.go）。
type PlanReconciler struct {
	SpaceID  string
	Options  PlanOptions
	Deploy   PlanDeploy
	Groups   *GroupBuilder
	Store    planStore
	Storage  PlanSnapshotRegistrar
	Cloud    planCloud
	Entry    func(region string) (map[string]string, error)
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
	// deletedFunctions 是 RPC 删掉的计划内函数（键是 refKey：地域/命名空间/函数名，受 readMu 保护）：未生效计划里已经确认的函数被删后，
	// 下一轮去掉它的确认、按 Target 重推。只在内存里，重启就丢，那时由计划生效后的读回修复重建。
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

	// 补采函数：多个补采协程并发调用时串行，backfillReady 缓存已确认就绪的“代码包|外部入口指纹”（受 backfillMu 保护）；
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
	// 交接截止每轮到点幂等地改，与计划推进无关；返回本轮已改成功的数据集，裁旧份以它为前提。
	// 改不成功不挡住本轮别的数据集，结束时按失败返回。
	latenessOK, latenessErr := r.applyHandoffLateness(ctx, open)
	if collectErr == nil {
		collectErr = latenessErr
	}
	if open != nil {
		if r.Metrics != nil {
			r.Metrics.ObserveAssignmentPending(r.SpaceID, true, open.CreatedAt)
		}
		// 未生效的计划通常几分钟内生效，期间不受该计划影响的函数照常做云端校验与修复。
		if err := r.verifyUntouched(ctx, open); err != nil {
			log.WarnContextf(ctx, "collector fetch plan verification skipped space=%s: %v", r.SpaceID, err)
		}
		if err := r.reconfirmDeleted(ctx, open); err != nil {
			return err
		}
		if err := r.advance(ctx, open); err != nil {
			return err
		}
	} else {
		r.takeDeletedFunctions() // 没有未生效的计划：删掉的函数由读回修复重建（verifyRequested）
		if err := r.planChange(ctx, latenessOK); err != nil {
			return err
		}
	}
	if collectErr != nil {
		return collectErr
	}
	// 没有未生效的计划（或本轮刚把计划推进到生效），并且自上一轮结束以来没有失败：对账成功。
	// 在跑的云端操作只要还没超过卡住年龄就不影响成功：大批量升级可能连续跑十几分钟；
	// 最早的操作卡住时不再刷新成功时间，由对账陈旧判定（关注项 fetch_coordination_failing，plan_health.stale_after 默认 15 分钟）发现。
	if r.Metrics != nil && r.failCount == r.roundFailCount && !r.operationStuck() {
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
		if !strings.EqualFold(strings.TrimSpace(task.DataType), domain.DataTypeKline) {
			continue
		}
		if _, parseErr := domain.ParseCollectParams(task.CollectParams, task.DataType); parseErr != nil {
			continue
		}
		datasets[taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID).DatasetID] = struct{}{}
	}
	return datasets, nil
}

// saveProgress 保存计划的状态与进度；timeline 是随这次进度一起成立的时间线行，与进度同一个事务。
func (r *PlanReconciler) saveProgress(ctx context.Context, record *store.FetchPlanRecord, progress *FetchPlanProgress, status, lastError string, timeline ...store.SeriesTimelineEntry) error {
	if err := r.Store.Update(ctx, record.ID, status, mustJSON(progress), lastError, timeline...); err != nil {
		return r.fail("store", fmt.Errorf("save fetch plan progress: %w", err))
	}
	record.Status, record.Progress, record.LastError = status, mustJSON(progress), lastError
	return nil
}

// takeDeletedFunctions 取走 RPC 删掉的计划内函数（键是 refKey）。
func (r *PlanReconciler) takeDeletedFunctions() map[string]bool {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	deleted := r.deletedFunctions
	r.deletedFunctions = nil
	return deleted
}

// reconfirmDeleted 把 RPC 删掉、而在未生效计划里已经确认下发的函数的确认去掉并保存进度：随后的推进按 Target 重推（Ensure 建出来），
// 不等每小时的读回。调用方持有 r.mu（对账协程）。
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
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if function == nil || !function.IsSCF() || !deleted[refKey(r.functionRef(name, function.Region))] || !progress.Pushed[name] {
			continue
		}
		delete(progress.Pushed, name)
		changed = true
	}
	if !changed {
		return nil
	}
	log.InfoContextf(ctx, "collector fetch plan repushes functions deleted through the RPC space=%s plan=%d", r.SpaceID, record.ID)
	return r.saveProgress(ctx, record, progress, record.Status, record.LastError)
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
