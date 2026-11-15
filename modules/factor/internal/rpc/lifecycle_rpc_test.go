package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 模块服务的 GetLifecycle、ControlLifecycle 转给模块自己的生命周期控制器（批 4 T6）；控制器还没设置时回答还没有就绪。
func TestServiceLifecycleMethodsUseTheModuleController(t *testing.T) {
	ctx := context.Background()
	life, err := lifecycle.New(lifecycle.Options{Module: "factor-mgr"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, nil, WithLifecycle(life))
	control, err := svc.ControlLifecycle(ctx, &commonpb.ControlLifecycleReq{Command: "pause", Reason: "维护", By: "ops"})
	if err != nil || control.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS || control.GetLifecycle().GetStage() != "paused" {
		t.Fatalf("ControlLifecycle = %v, %v", control, err)
	}
	if !life.Halted() {
		t.Fatal("暂停没有落到模块的控制器上")
	}
	get, err := svc.GetLifecycle(ctx, &commonpb.GetLifecycleReq{})
	if err != nil || get.GetLifecycle().GetModule() != "factor-mgr" || get.GetLifecycle().GetPause().GetBy() != "ops" {
		t.Fatalf("GetLifecycle = %v, %v", get, err)
	}
	if rsp, _ := NewService(nil, nil).GetLifecycle(ctx, &commonpb.GetLifecycleReq{}); rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INNER_ERR {
		t.Fatalf("控制器没设置时应回答还没有就绪：%v", rsp)
	}
}
