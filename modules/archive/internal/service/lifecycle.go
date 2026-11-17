package service

import (
	"context"

	"github.com/mooyang-code/moox/packages/commonpb"
)

// 归档管理模块的生命周期接口（设计 3.7）：两个方法都转给模块的生命周期控制器（见 WithLifecycle）。

// GetLifecycle 读模块的暂停状态。
func (m *MgrService) GetLifecycle(ctx context.Context, req *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	return m.svc.life.GetLifecycle(ctx, req)
}

// ControlLifecycle 暂停、恢复模块：暂停期间定时触发与定时维护跳过。
func (m *MgrService) ControlLifecycle(ctx context.Context, req *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	return m.svc.life.ControlLifecycle(ctx, req)
}
