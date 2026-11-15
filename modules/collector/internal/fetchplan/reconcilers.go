package fetchplan

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

// Reconcilers 按空间登记对账器：工厂每建好一个就登记（同一空间的新对账器替换旧的），
// 补采、RPC（列出、删除、探测云函数）与进程退出按空间找到它。
type Reconcilers struct {
	mu     sync.Mutex
	spaces map[string]*PlanReconciler
}

// Store 登记空间的对账器。
func (s *Reconcilers) Store(spaceID string, reconciler *PlanReconciler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spaces == nil {
		s.spaces = map[string]*PlanReconciler{}
	}
	s.spaces[strings.ToLower(strings.TrimSpace(spaceID))] = reconciler
}

// Get 返回空间的对账器；空间不在对账的管理集合里时返回 nil。
func (s *Reconcilers) Get(spaceID string) *PlanReconciler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spaces[strings.ToLower(strings.TrimSpace(spaceID))]
}

// Require 返回空间的对账器；没有时返回错误。
func (s *Reconcilers) Require(spaceID string) (*PlanReconciler, error) {
	if reconciler := s.Get(spaceID); reconciler != nil {
		return reconciler, nil
	}
	return nil, fmt.Errorf("空间 %s 没有采集计划对账器（不在管理集合里，或 Collector 刚启动还没建好）", spaceID)
}

// Invoke 同步调用空间的补采函数（klinecheck.Invoker）。
func (s *Reconcilers) Invoke(ctx context.Context, spaceID, functionName string, event map[string]any) (marketfetch.InvocationResult, error) {
	reconciler, err := s.Require(spaceID)
	if err != nil {
		return marketfetch.InvocationResult{}, err
	}
	return reconciler.InvokeBackfill(ctx, functionName, event)
}

// Close 在进程退出时取消全部空间在跑的云端操作并等它们返回，之后不再接受新操作。
func (s *Reconcilers) Close() {
	s.mu.Lock()
	all := make([]*PlanReconciler, 0, len(s.spaces))
	for _, reconciler := range s.spaces {
		all = append(all, reconciler)
	}
	s.mu.Unlock()
	for _, reconciler := range all {
		reconciler.StopOperations(true)
	}
}
