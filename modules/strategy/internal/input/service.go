package input

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"

	"github.com/mooyang-code/moox/modules/strategy/internal/period"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// Service 把解析与装配封装成管理接口可用的形式。
type Service struct {
	Client upstream.Client
	// Sets 是 FactorMgr 的任务关联与读取接口；nil 时不能启用实例。
	Sets upstream.FactorTasks
	// ReadyWait 与 ReadyStep 是任务的结果 View 补齐成员列的最长等待与探测间隔（列由 Storage 异步扩展进 View）；
	// 零值表示不等待。
	ReadyWait time.Duration
	ReadyStep time.Duration
}

// ResolveInstance 启用实例时的解析（设计 8.1）：所有任务都要已启用；解析绑定——DSL 引用的列都存在，声明了 factor_params 时任务的
// 可变成员与它一一对应；解析通过之后，声明了 factor_params 的策略才把主帧任务与实例永久关联（幂等：第一次启用时写入，
// 以后只是核对原来的关联），没有声明的不关联。tasks 是实例登记的任务键 → 因子任务（附加来源的键是 来源名:市场），解析出的市场必须与登记一致。
// 任务刚加成员时，结果 View 的列由 Storage 异步补齐，所以列不存在时在限定时间内重试。
func (s Service) ResolveInstance(ctx context.Context, instanceID, spaceID string, tasks map[string]string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	var program *dsl.Program
	resolved, err := s.bindAndResolve(ctx, instanceID, spaceID, tasks, len(strategy.FactorParams) > 0, func() (Resolved, error) {
		resolved, compiled, err := ResolveTasks(ctx, s.Client, spaceID, tasks, strategy)
		program = compiled
		return resolved, err
	})
	return resolved, program, err
}

// ResolvePoolInstance 是策略池启用时的解析：与 ResolveInstance 相同的任务核对与重试，绑定按成员分别解析。策略池与成员都不能写
// factor_params，所以不关联任务。
func (s Service) ResolvePoolInstance(ctx context.Context, instanceID, spaceID string, tasks map[string]string, pool dsl.Strategy, members []PoolMemberDef) (PoolResolved, error) {
	if err := dsl.Validate(&pool); err != nil {
		return PoolResolved{}, err
	}
	var out PoolResolved
	_, err := s.bindAndResolve(ctx, instanceID, spaceID, tasks, false, func() (Resolved, error) {
		var err error
		out, err = ResolvePool(ctx, s.Client, spaceID, TaskIDs(tasks), pool, members)
		return out.Resolved, err
	})
	return out, err
}

// ResolvePool 解析策略池的绑定（不关联任务，只读；校验接口用）。
func (s Service) ResolvePool(ctx context.Context, spaceID string, taskIDs []string, pool dsl.Strategy, members []PoolMemberDef) (PoolResolved, error) {
	if s.Client == nil {
		return PoolResolved{}, errors.New("Storage 与 Factor 依赖未配置，不能解析绑定")
	}
	return ResolvePool(ctx, s.Client, spaceID, taskIDs, pool, members)
}

// bindAndResolve 完成启用解析里与策略无关的部分：核对登记的市场、核对任务都已启用，在限定时间内重试 resolve（任务的结果 View 还没补齐列时）；
// 解析与市场核对都通过之后，tuned 为真时才把主帧任务与实例关联。关联是永久的，解析没通过（DSL 写错、来源任务有可变成员、
// factor_params 与可变成员对不上）时不能先把任务占住，否则启用失败的实例会一直占着任务，交叉登记时两个实例都删不掉。
func (s Service) bindAndResolve(ctx context.Context, instanceID, spaceID string, tasks map[string]string, tuned bool, resolve func() (Resolved, error)) (Resolved, error) {
	if s.Client == nil || s.Sets == nil {
		return Resolved{}, errors.New("Factor 依赖未配置，不能启用实例")
	}
	deadline := time.Now().Add(s.ReadyWait)
	// 先核对市场：任务刚建、结果 View 或数据集还没建出时在限定时间内重试，其他错误（包括市场对不上）立即返回。
	for {
		err := CheckRegisteredMarkets(ctx, tasks, func(ctx context.Context, taskID string) (string, error) { return s.TaskMarket(ctx, spaceID, taskID) })
		if err == nil {
			break
		}
		if !waitingForColumns(err) || !time.Now().Add(s.ReadyStep).Before(deadline) {
			return Resolved{}, err
		}
		select {
		case <-ctx.Done():
			return Resolved{}, ctx.Err()
		case <-time.After(s.ReadyStep):
		}
	}
	keys := sortedKeys(tasks)
	for _, key := range keys {
		task, err := s.Sets.GetFactorTask(ctx, tasks[key])
		if err != nil {
			return Resolved{}, err
		}
		if err := requireEnabledTask(tasks[key], task.Status); err != nil {
			return Resolved{}, err
		}
	}
	var resolved Resolved
	for {
		var err error
		resolved, err = resolve()
		if err == nil {
			err = CheckResolvedMarkets(resolved, tasks)
		}
		if err == nil {
			break
		}
		if !waitingForColumns(err) || !time.Now().Add(s.ReadyStep).Before(deadline) {
			return Resolved{}, err
		}
		select {
		case <-ctx.Done():
			return Resolved{}, ctx.Err()
		case <-time.After(s.ReadyStep):
		}
	}
	if !tuned {
		return resolved, nil
	}
	// 参数提交给每个主帧任务（现货加永续时两个任务都关联、提交同一份参数；写了 factor_params 的策略没有参考市场），附加来源的任务只读不关联。
	for _, key := range keys {
		if source, _ := dsl.SplitTaskKey(key); source != "" {
			continue
		}
		taskID := tasks[key]
		bound, err := s.Sets.BindFactorTask(ctx, taskID, instanceID)
		if err != nil {
			return Resolved{}, err
		}
		if bound.BoundInstanceID != instanceID {
			return Resolved{}, fmt.Errorf("因子任务 %s 关联的是实例 %q，不是本实例", taskID, bound.BoundInstanceID)
		}
		if err := requireEnabledTask(taskID, bound.Status); err != nil {
			return Resolved{}, err
		}
	}
	return resolved, nil
}

// requireEnabledTask 要求任务已启用。
func requireEnabledTask(taskID, status string) error {
	if status != "enabled" {
		return fmt.Errorf("因子任务 %s 的状态是 %q，只有已启用的任务才能启用实例", taskID, status)
	}
	return nil
}

// TaskMarket 返回因子任务的市场：任务结果 View → 数据集 → 源数据集的市场类型。创建实例和启用前用它核对“市场 → 任务”的对应。
func (s Service) TaskMarket(ctx context.Context, spaceID, taskID string) (string, error) {
	if s.Client == nil {
		return "", errors.New("Storage 与 Factor 依赖未配置，不能确定因子任务的市场")
	}
	item, err := resolveView(ctx, s.Client, spaceID, strings.TrimSpace(taskID), "")
	if err != nil {
		return "", err
	}
	return item.market, nil
}

// CheckRegisteredMarkets 核对实例登记的“任务键 → 任务”：每个任务的源数据集市场（由 marketOf 查询）必须就是登记的市场（附加来源的键
// 来源名:市场 取冒号后的市场）。填错会让任务被永久占住、实例又启用不了，所以创建实例时就要核对，
// 启用时也要在关联任务之前再核对一次。
func CheckRegisteredMarkets(ctx context.Context, tasks map[string]string, marketOf func(ctx context.Context, taskID string) (string, error)) error {
	for _, taskID := range TaskIDs(tasks) {
		market := ""
		for registered, id := range tasks {
			if id == taskID {
				_, market = dsl.SplitTaskKey(registered)
			}
		}
		actual, err := marketOf(ctx, taskID)
		if err != nil {
			return fmt.Errorf("无法确定因子任务 %s 的市场（它的结果 View 或数据集还没有就绪？）：%w", taskID, err)
		}
		if actual != market {
			return fmt.Errorf("因子任务 %s 登记为 %s 市场，但它的源数据集是 %s 市场", taskID, market, actual)
		}
	}
	return nil
}

// TaskIDs 返回实例登记的全部因子任务：先是主帧与参考市场（按现货、永续、股票的先后），再是附加来源（按任务键）。
func TaskIDs(tasks map[string]string) []string {
	ids := make([]string, 0, len(tasks))
	for _, market := range []string{dsl.MarketSpot, dsl.MarketSwap, dsl.MarketEquity} {
		if id, ok := tasks[market]; ok {
			ids = append(ids, id)
		}
	}
	for _, key := range sortedKeys(tasks) {
		if source, _ := dsl.SplitTaskKey(key); source != "" {
			ids = append(ids, tasks[key])
		}
	}
	return ids
}

// CheckResolvedMarkets 核对解析结果里的任务键与实例登记的一致（解析之后的最后一道核对，不查 Storage）。
func CheckResolvedMarkets(resolved Resolved, tasks map[string]string) error {
	all := resolved.AllTasks()
	if len(all) != len(tasks) {
		return fmt.Errorf("登记了 %d 个因子任务，按策略的市场模式解析出 %d 个", len(tasks), len(all))
	}
	for market, taskID := range tasks {
		if got := all[market]; got != taskID {
			for actual, id := range all {
				if id == taskID {
					return fmt.Errorf("因子任务 %s 登记为 %s 市场，但它的源数据集是 %s 市场", taskID, market, actual)
				}
			}
			return fmt.Errorf("实例登记的 %s 市场任务是 %s，解析出的是 %q", market, taskID, got)
		}
	}
	return nil
}

// waitingForColumns 报告解析失败是否只是任务的结果 View 还没补齐成员列（或 View 尚未建出），稍后会好。
func waitingForColumns(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return errors.Is(err, upstream.ErrViewNotFound) || strings.Contains(text, "不存在于 View") || strings.Contains(text, "没有数据集或频率")
}

// Resolve 按登记的任务（任务键 → 任务）解析绑定并编译策略（不关联任务，只读；校验接口用）。
func (s Service) Resolve(ctx context.Context, spaceID string, tasks map[string]string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if s.Client == nil {
		return Resolved{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能解析绑定")
	}
	return ResolveTasks(ctx, s.Client, spaceID, tasks, strategy)
}

// ReplayWindow 逐个读取绑定 View 的覆盖范围，校验回放起点并截断终点：起点要在每个 View 的可用范围内，终点取各 View 中最早的。
// 返回截断后的终点与各任务键校验所用的活动索引代次（任务键 → 代次）。
func (s Service) ReplayWindow(ctx context.Context, spaceID string, resolved Resolved, program *dsl.Program, start, end, now time.Time) (time.Time, map[string]string, error) {
	if s.Client == nil {
		return time.Time{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能回放")
	}
	generations := make(map[string]string, len(resolved.Tasks))
	for _, key := range resolved.TaskKeys() {
		view, err := s.Client.GetView(ctx, spaceID, resolved.Tasks[key].DatasetID)
		if err != nil {
			return time.Time{}, nil, err
		}
		if view, err = WithCoverage(ctx, s.Client, spaceID, view, true); err != nil {
			return time.Time{}, nil, err
		}
		if end, err = ReplayWindow(resolved, program, view, start, end, now); err != nil {
			if len(resolved.Tasks) > 1 {
				err = fmt.Errorf("%s 市场的 View %s：%w", key, view.DatasetID, err)
			}
			return time.Time{}, nil, err
		}
		generations[key] = view.Generation
	}
	// 附加来源与参考市场不截断区间，只记下提交时的索引代次。
	for key, task := range resolved.AllTaskBindings() {
		if _, main := generations[key]; main {
			continue
		}
		generation, err := s.Client.ViewGeneration(ctx, spaceID, task.DatasetID)
		if err != nil {
			return time.Time{}, nil, err
		}
		generations[key] = generation
	}
	return end, generations, nil
}

// ComputeReplayWindow 是“因子现算”来源的提交校验：主帧不读 View，不按 View 的覆盖范围校验起点或截断终点（可回放的各期
// 由因子引擎按本地数据在回放开始时给出），只把终点截到 now 时最近一根已收盘的 bar 之前（它可能还没同步到因子机本地，与
// View 来源一样不回放最新一根）。返回截断后的终点、各任务键的活动索引代次（附加来源与参考市场执行时仍读 View；主帧任务
// 只登记，不再按它复查），以及主帧的序列数（数据集的标的数 × 市场数，用来估算单步大小）。
func (s Service) ComputeReplayWindow(ctx context.Context, spaceID string, resolved Resolved, start, end, now time.Time) (time.Time, map[string]string, int, error) {
	if s.Client == nil {
		return time.Time{}, nil, 0, errors.New("Storage 与 Factor 依赖未配置，不能回放")
	}
	closed, err := period.ClosedPeriod(resolved.Calendar, resolved.Bar, now)
	if err != nil {
		return time.Time{}, nil, 0, err
	}
	if end.After(closed.StorageStart) {
		end = closed.StorageStart
	}
	if !end.After(start) {
		return time.Time{}, nil, 0, fmt.Errorf("%w：最近一根已收盘的 bar 是 %s，回放只到它之前", ErrNoCompleteBar, period.PeriodLabel(resolved.Calendar, resolved.Bar, closed.StorageStart))
	}
	generations := make(map[string]string, len(resolved.Tasks))
	for key, task := range resolved.AllTaskBindings() {
		generation, err := s.Client.ViewGeneration(ctx, spaceID, task.DatasetID)
		if err != nil {
			return time.Time{}, nil, 0, err
		}
		generations[key] = generation
	}
	// 单步大小按全部主帧会话合计估算：每个市场一个会话，序列数是各市场数据集的标的数之和。
	series := 0
	for _, market := range resolved.Markets() {
		subjects, err := s.Client.ListDatasetSubjects(ctx, spaceID, resolved.TaskOf(market).DatasetID)
		if err != nil {
			return time.Time{}, nil, 0, err
		}
		series += len(subjects)
	}
	return end, generations, series, nil
}

// PoolReplayWindow 校验并截断策略池研究回放的区间：每个成员按自己的 min_age_bars 与回看校验起点、终点取各 View 各成员中最早的
// （见 PoolWindow；研究回放不含最新一根）。返回截断后的终点与各市场校验所用的活动索引代次（市场 → 代次）。
func (s Service) PoolReplayWindow(ctx context.Context, spaceID string, pooled PoolResolved, start, end, now time.Time) (time.Time, map[string]string, error) {
	if s.Client == nil {
		return time.Time{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能回放")
	}
	views := make(map[string]upstream.ViewInfo, len(pooled.Resolved.Tasks))
	generations := make(map[string]string, len(pooled.Resolved.Tasks))
	for market, task := range pooled.Resolved.Tasks {
		view, err := s.Client.GetView(ctx, spaceID, task.DatasetID)
		if err != nil {
			return time.Time{}, nil, err
		}
		views[market], generations[market] = view, view.Generation
	}
	end, err := PoolWindow(ctx, s.Client, spaceID, pooled.Resolved, pooled.Members, pooled.Programs, views, start, end, now, false)
	if err != nil {
		return time.Time{}, nil, err
	}
	return end, generations, nil
}

// CheckPoolCoverage 在启用策略池时校验 View 的覆盖：按每个成员的 min_age_bars 与 A 股行键（见 CheckPoolMemberCoverage），
// 再按精确的覆盖统计核对 View 实际的历史够不够预热（见 CheckPoolWarmCoverage）。
func (s Service) CheckPoolCoverage(ctx context.Context, spaceID string, pooled PoolResolved, now time.Time) error {
	if s.Client == nil {
		return errors.New("Storage 与 Factor 依赖未配置，不能启用实例")
	}
	if err := CheckPoolMemberCoverage(ctx, s.Client, spaceID, pooled.Members, now); err != nil {
		return err
	}
	return CheckPoolWarmCoverage(ctx, s.Client, spaceID, pooled, now)
}

// CheckCoverage 在启用实例时校验 View 的覆盖（见包级 CheckCoverage）。
func (s Service) CheckCoverage(ctx context.Context, spaceID string, resolved Resolved, now time.Time) error {
	if s.Client == nil {
		return errors.New("Storage 与 Factor 依赖未配置，不能启用实例")
	}
	return CheckCoverage(ctx, s.Client, spaceID, resolved, now)
}

// LoadLatest 装配 now 之前最近一个已闭合周期的输入；该周期没有行时向前最多再找两根。
func (s Service) LoadLatest(ctx context.Context, spaceID string, resolved Resolved, program *dsl.Program, now time.Time) (Loaded, error) {
	if s.Client == nil {
		return Loaded{}, errors.New("Storage 与 Factor 依赖未配置，不能试算")
	}
	boundary, err := period.ClosedPeriod(resolved.Calendar, resolved.Bar, now)
	if err != nil {
		return Loaded{}, err
	}
	loader := Loader{Client: s.Client}
	barStart := boundary.StorageStart
	var last Loaded
	for attempt := 0; attempt < 3; attempt++ {
		loaded, err := loader.LoadBar(ctx, spaceID, resolved, program, Bar{BarStart: barStart})
		if err != nil {
			return Loaded{}, err
		}
		if loaded.hasRows() {
			return loaded, nil
		}
		last = loaded
		barStart, err = period.PreviousStorageStart(resolved.Calendar, resolved.Bar, barStart)
		if err != nil {
			return Loaded{}, err
		}
	}
	return last, fmt.Errorf("View %s 最近三个周期都没有数据", strings.Join(resolved.DatasetIDs(), "、"))
}
