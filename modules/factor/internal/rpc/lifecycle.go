package rpc

import (
	"context"

	"github.com/mooyang-code/moox/packages/commonpb"
)

// 因子管理模块的生命周期接口（设计 3.7）：两个方法都转给模块的生命周期控制器（见 WithLifecycle）。

// GetLifecycle 读模块的暂停状态。
func (s *Service) GetLifecycle(ctx context.Context, req *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	return s.life.GetLifecycle(ctx, req)
}

// ControlLifecycle 暂停、恢复模块：暂停期间修改类接口被拒绝。
func (s *Service) ControlLifecycle(ctx context.Context, req *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	return s.life.ControlLifecycle(ctx, req)
}
