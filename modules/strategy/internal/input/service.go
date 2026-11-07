package input

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// Service 把解析与装配封装成管理接口可用的形式。
type Service struct {
	Client Client
	// Sets 是 FactorMgr 的任务关联接口；nil 时不能启用实例。
	Sets FactorTasks
	// ReadyWait 与 ReadyStep 是任务的结果 View 补齐成员列的最长等待与探测间隔（列由 Storage 异步扩展进 View）；
	// 零值表示不等待。
	ReadyWait time.Duration
	ReadyStep time.Duration
}

// ResolveInstance 启用实例时的解析（设计 8.1）：先把每个任务与实例永久关联（幂等：第一次启用时写入，以后只是核对原来的关联），
// 要求任务已启用；再解析绑定——DSL 引用的列都存在，任务的可变成员与 factor_params 一一对应。tasks 是实例登记的市场 → 因子任务，
// 解析出的市场必须与登记一致。任务刚加成员时，结果 View 的列由 Storage 异步补齐，所以列不存在时在限定时间内重试。
func (s Service) ResolveInstance(ctx context.Context, instanceID, spaceID string, tasks map[string]string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	var program *dsl.Program
	resolved, err := s.bindAndResolve(ctx, instanceID, spaceID, tasks, func(taskIDs []string) (Resolved, error) {
		resolved, compiled, err := Resolve(ctx, s.Client, spaceID, taskIDs, strategy)
		program = compiled
		return resolved, err
	})
	return resolved, program, err
}

// ResolvePoolInstance 是策略池启用时的解析：与 ResolveInstance 相同的任务关联与重试，绑定按成员分别解析。
func (s Service) ResolvePoolInstance(ctx context.Context, instanceID, spaceID string, tasks map[string]string, pool dsl.Strategy, members []PoolMemberDef) (PoolResolved, error) {
	if err := dsl.Validate(&pool); err != nil {
		return PoolResolved{}, err
	}
	var out PoolResolved
	_, err := s.bindAndResolve(ctx, instanceID, spaceID, tasks, func(taskIDs []string) (Resolved, error) {
		var err error
		out, err = ResolvePool(ctx, s.Client, spaceID, taskIDs, pool, members)
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

// bindAndResolve 完成启用解析里与策略无关的部分：核对登记的市场、把任务与实例关联，再在限定时间内重试 resolve（任务的结果 View 还没补齐列时）。
func (s Service) bindAndResolve(ctx context.Context, instanceID, spaceID string, tasks map[string]string, resolve func(taskIDs []string) (Resolved, error)) (Resolved, error) {
	if s.Client == nil || s.Sets == nil {
		return Resolved{}, errors.New("Factor 依赖未配置，不能启用实例")
	}
	taskIDs := TaskIDs(tasks)
	deadline := time.Now().Add(s.ReadyWait)
	// 关联是永久的：先核对市场，填错的任务不能被关联占住。任务刚建、结果 View 或数据集还没建出时在限定时间内重试，
	// 其他错误（包括市场对不上）立即返回；关联只在核对通过之后。
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
	for _, taskID := range taskIDs {
		bound, err := s.Sets.BindFactorTask(ctx, taskID, instanceID)
		if err != nil {
			return Resolved{}, err
		}
		if bound.BoundInstanceID != instanceID {
			return Resolved{}, fmt.Errorf("因子任务 %s 关联的是实例 %q，不是本实例", taskID, bound.BoundInstanceID)
		}
		if bound.Status != "enabled" {
			return Resolved{}, fmt.Errorf("因子任务 %s 的状态是 %q，只有已启用的任务才能启用实例", taskID, bound.Status)
		}
	}
	for {
		resolved, err := resolve(taskIDs)
		if err == nil {
			err = CheckResolvedMarkets(resolved, tasks)
		}
		if err == nil {
			return resolved, nil
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

// CheckRegisteredMarkets 核对实例登记的“市场 → 任务”：每个任务的源数据集市场（由 marketOf 查询）必须就是登记的市场。填错会让任务被永久占住、
// 实例又启用不了，所以创建实例时就要核对，启用时也要在关联任务之前再核对一次。
func CheckRegisteredMarkets(ctx context.Context, tasks map[string]string, marketOf func(ctx context.Context, taskID string) (string, error)) error {
	for _, taskID := range TaskIDs(tasks) {
		market := ""
		for registered, id := range tasks {
			if id == taskID {
				market = registered
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

// TaskIDs 返回实例登记的因子任务（按市场先后：现货、永续、股票）。
func TaskIDs(tasks map[string]string) []string {
	ids := make([]string, 0, len(tasks))
	for _, market := range []string{dsl.MarketSpot, dsl.MarketSwap, dsl.MarketEquity} {
		if id, ok := tasks[market]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// CheckResolvedMarkets 核对解析结果里的市场与实例登记的一致（解析之后的最后一道核对，不查 Storage）。
func CheckResolvedMarkets(resolved Resolved, tasks map[string]string) error {
	if len(resolved.Tasks) != len(tasks) {
		return fmt.Errorf("登记了 %d 个因子任务，按策略的市场模式解析出 %d 个", len(tasks), len(resolved.Tasks))
	}
	for market, taskID := range tasks {
		if got := resolved.Tasks[market].FactorTaskID; got != taskID {
			for actual, task := range resolved.Tasks {
				if task.FactorTaskID == taskID {
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
	return errors.Is(err, ErrViewNotFound) || strings.Contains(text, "不存在于 View") || strings.Contains(text, "没有数据集或频率")
}

// Resolve 解析绑定并编译策略（不关联任务，只读；校验接口用）。
func (s Service) Resolve(ctx context.Context, spaceID string, taskIDs []string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if s.Client == nil {
		return Resolved{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能解析绑定")
	}
	return Resolve(ctx, s.Client, spaceID, taskIDs, strategy)
}

// ReplayWindow 逐个读取绑定 View 的覆盖范围，校验回放起点并截断终点：起点要在每个 View 的可用范围内，终点取各 View 中最早的。
// 返回截断后的终点与各市场校验所用的活动索引代次（市场 → 代次）。
func (s Service) ReplayWindow(ctx context.Context, spaceID string, resolved Resolved, program *dsl.Program, start, end, now time.Time) (time.Time, map[string]string, error) {
	if s.Client == nil {
		return time.Time{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能回放")
	}
	generations := make(map[string]string, len(resolved.Tasks))
	for _, market := range resolved.Markets() {
		view, err := s.Client.GetView(ctx, spaceID, resolved.Tasks[market].ViewID)
		if err != nil {
			return time.Time{}, nil, err
		}
		if view, err = WithCoverage(ctx, s.Client, spaceID, view, true); err != nil {
			return time.Time{}, nil, err
		}
		if end, err = ReplayWindow(resolved, program, view, start, end, now); err != nil {
			if len(resolved.Tasks) > 1 {
				err = fmt.Errorf("%s 市场的 View %s：%w", market, view.ViewID, err)
			}
			return time.Time{}, nil, err
		}
		generations[market] = view.Generation
	}
	return end, generations, nil
}

// CheckPoolCoverage 在启用策略池时按每个成员校验 View 的覆盖（见包级 CheckPoolCoverage）。
func (s Service) CheckPoolCoverage(ctx context.Context, spaceID string, members []PoolMember, now time.Time) error {
	if s.Client == nil {
		return errors.New("Storage 与 Factor 依赖未配置，不能启用实例")
	}
	return CheckPoolCoverage(ctx, s.Client, spaceID, members, now)
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
	period, err := ClosedPeriod(resolved.Calendar, resolved.Bar, now)
	if err != nil {
		return Loaded{}, err
	}
	loader := Loader{Client: s.Client}
	barStart := period.StorageStart
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
		barStart, err = PreviousStorageStart(resolved.Calendar, resolved.Bar, barStart)
		if err != nil {
			return Loaded{}, err
		}
	}
	return last, fmt.Errorf("View %s 最近三个周期都没有数据", strings.Join(resolved.ViewIDs(), "、"))
}
