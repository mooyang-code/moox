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
	// Sets 是 FactorMgr 的私有因子任务接口；nil 时不能启用声明了 factors 的策略。
	Sets FactorTasks
	// ReadyWait 与 ReadyStep 是私有因子任务的结果 View 补齐成员列的最长等待与探测间隔（列由 Storage 异步扩展进 View）；
	// 零值表示不等待。
	ReadyWait time.Duration
	ReadyStep time.Duration
}

// ResolveInstance 启用实例时的解析：没有声明 factors 的策略直接解析 viewID；声明了 factors 的先为实例建立私有因子任务
// （成员恰好是声明的因子，实例是唯一的参数写入方），等它的结果 View 补齐列后绑定该 View。
func (s Service) ResolveInstance(ctx context.Context, instanceID, spaceID, viewID string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if len(strategy.Factors) == 0 {
		return s.Resolve(ctx, spaceID, viewID, strategy)
	}
	if s.Client == nil || s.Sets == nil {
		return Resolved{}, nil, errors.New("Factor 依赖未配置，不能启用声明了 factors 的策略")
	}
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	source, err := s.Client.GetView(ctx, spaceID, viewID)
	if err != nil {
		return Resolved{}, nil, err
	}
	if source.DatasetID == "" || source.Frequency == "" {
		return Resolved{}, nil, fmt.Errorf("View %s 没有数据集或频率", viewID)
	}
	definitions := make(map[string]FactorInfo, len(strategy.Factors))
	for _, factorID := range strategy.FactorIDs() {
		info, err := s.Client.GetFactor(ctx, factorID)
		if err != nil {
			return Resolved{}, nil, err
		}
		definitions[factorID] = info
	}
	if err := CheckFactorParams(strategy, definitions); err != nil {
		return Resolved{}, nil, err
	}
	set, err := s.Sets.EnsureInstanceFactorTask(ctx, InstanceFactorTaskRequest{
		InstanceID: instanceID, SpaceID: spaceID, SourceDatasetID: source.DatasetID, Freq: strings.ToLower(source.Frequency), Members: strategy.Factors,
	})
	if err != nil {
		return Resolved{}, nil, err
	}
	deadline := time.Now().Add(s.ReadyWait)
	for {
		resolved, program, err := Resolve(ctx, s.Client, spaceID, set.ViewID, strategy)
		if err == nil {
			resolved.SourceViewID, resolved.FactorTaskID = viewID, set.TaskID
			resolved.FactorParams = make(map[string][]ParamDecl, len(strategy.Factors))
			for _, decl := range strategy.Factors {
				resolved.FactorParams[decl.Alias] = definitions[decl.FactorID].Params
			}
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

// waitingForColumns 报告解析失败是否只是私有因子任务的结果 View 还没补齐成员列（或 View 尚未建出），稍后会好。
func waitingForColumns(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return errors.Is(err, ErrViewNotFound) || strings.Contains(text, "不存在于 View") || strings.Contains(text, "没有数据集或频率")
}

// Resolve 解析绑定并编译策略。
func (s Service) Resolve(ctx context.Context, spaceID, viewID string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if s.Client == nil {
		return Resolved{}, nil, errors.New("Storage 与 Factor 依赖未配置，不能解析绑定")
	}
	return Resolve(ctx, s.Client, spaceID, viewID, strategy)
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
