package input

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// Service 把解析与装配封装成管理接口可用的形式。
type Service struct {
	Client Client
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
