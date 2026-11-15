package rpc

import (
	"context"

	"github.com/mooyang-code/moox/packages/commonpb"
)

// 策略模块的生命周期接口（设计 3.7）：两个方法都转给模块的生命周期控制器，不分空间。

// GetLifecycle 读模块的暂停与恢复追赶状态。
func (s *Service) GetLifecycle(ctx context.Context, req *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	return s.Lifecycle.GetLifecycle(ctx, req)
}

// ControlLifecycle 暂停、恢复与放行模块。
func (s *Service) ControlLifecycle(ctx context.Context, req *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	return s.Lifecycle.ControlLifecycle(ctx, req)
}
