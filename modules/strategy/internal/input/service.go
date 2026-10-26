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

// ResolveInstance 启用实例时的解析（设计 8.1）：先把任务与实例永久关联（幂等：第一次启用时写入，以后只是核对原来的关联），
// 要求任务已启用；再解析绑定——DSL 引用的列都存在，任务的可变成员与 factor_params 一一对应。
// 任务刚加成员时，结果 View 的列由 Storage 异步补齐，所以列不存在时在限定时间内重试。
func (s Service) ResolveInstance(ctx context.Context, instanceID, spaceID, taskID string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if s.Client == nil || s.Sets == nil {
		return Resolved{}, nil, errors.New("Factor 依赖未配置，不能启用实例")
	}
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	bound, err := s.Sets.BindFactorTask(ctx, taskID, instanceID)
	if err != nil {
		return Resolved{}, nil, err
	}
	if bound.BoundInstanceID != instanceID {
		return Resolved{}, nil, fmt.Errorf("因子任务 %s 关联的是实例 %q，不是本实例", taskID, bound.BoundInstanceID)
	}
	if bound.Status != "enabled" {
		return Resolved{}, nil, fmt.Errorf("因子任务 %s 的状态是 %q，只有已启用的任务才能启用实例", taskID, bound.Status)
	}
	deadline := time.Now().Add(s.ReadyWait)
	for {
		resolved, program, err := Resolve(ctx, s.Client, spaceID, taskID, strategy)
		if err == nil {
			return resolved, program, nil
		}
		if !waitingForColumns(err) || !time.Now().Add(s.ReadyStep).Before(deadline) {
			return Resolved{}, nil, err
		}
		select {
		case <-ctx.Done():
			return Resolved{}, nil, ctx.Err()
		case <-time.After(s.ReadyStep):
		}
	}
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
func (s Service) Resolve(ctx context.Context, spaceID, taskID string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if s.Client == nil {
		return Resolved{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能解析绑定")
	}
	return Resolve(ctx, s.Client, spaceID, taskID, strategy)
}

// ReplayWindow 读取 View 的覆盖范围，校验回放起点并截断终点，返回截断后的终点与校验所用的活动索引代次。
func (s Service) ReplayWindow(ctx context.Context, spaceID string, resolved Resolved, program *dsl.Program, start, end, now time.Time) (time.Time, string, error) {
	if s.Client == nil {
		return time.Time{}, "", errors.New("Storage 与 Factor 依赖未配置，不能回放")
	}
	view, err := s.Client.GetView(ctx, spaceID, resolved.ViewID)
	if err != nil {
		return time.Time{}, "", err
	}
	if view, err = WithCoverage(ctx, s.Client, spaceID, view, true); err != nil {
		return time.Time{}, "", err
	}
	end, err = ReplayWindow(resolved, program, view, start, end, now)
	return end, view.Generation, err
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
		if len(loaded.Frame.Rows) > 0 {
			return loaded, nil
		}
		last = loaded
		barStart, err = PreviousStorageStart(resolved.Calendar, resolved.Bar, barStart)
		if err != nil {
			return Loaded{}, err
		}
	}
	return last, fmt.Errorf("View %s 最近三个周期都没有数据", resolved.ViewID)
}
