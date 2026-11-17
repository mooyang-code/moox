package rpc

import (
	"context"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 采集模块的生命周期接口（设计 3.7）：两个方法都转给模块的生命周期控制器（bootstrap/lifecycle.go 建好后设置进来）。

// SetLifecycle 设置模块的生命周期控制器；设置之前两个方法回答“还没有就绪”。
func (s *Service) SetLifecycle(life *lifecycle.Controller) { s.life = life }

// GetLifecycle 读模块的暂停与恢复追赶状态。
func (s *Service) GetLifecycle(ctx context.Context, req *commonpb.GetLifecycleReq) (*commonpb.GetLifecycleRsp, error) {
	return s.life.GetLifecycle(ctx, req)
}

// ControlLifecycle 暂停、恢复与放行模块。
func (s *Service) ControlLifecycle(ctx context.Context, req *commonpb.ControlLifecycleReq) (*commonpb.ControlLifecycleRsp, error) {
	return s.life.ControlLifecycle(ctx, req)
}
