package cloudnode

import (
	"context"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// 全局保活探测实例（供定时器使用）
var globalKeepaliveInstance *ServiceImpl

// InitKeepaliveInstance 初始化保活探测全局实例
func InitKeepaliveInstance(service Service) error {
	impl, ok := service.(*ServiceImpl)
	if !ok || impl == nil {
		return fmt.Errorf("keepalive instance must be *ServiceImpl")
	}
	globalKeepaliveInstance = impl
	log.Info("[Keepalive] Global keepalive instance initialized")
	return nil
}

// HandleKeepaliveSchedule 保活探测定时器入口函数
func HandleKeepaliveSchedule(ctx context.Context, params string) error {
	ctxClone := trpc.CloneContext(ctx)
	log.InfoContextf(ctxClone, "[Keepalive] Starting keepalive probe, params: %s", params)
	startTime := time.Now()

	if globalKeepaliveInstance == nil {
		err := fmt.Errorf("keepalive instance not initialized")
		log.ErrorContext(ctxClone, "[Keepalive] "+err.Error())
		return err
	}

	if err := globalKeepaliveInstance.RunKeepaliveProbe(ctxClone); err != nil {
		log.ErrorContextf(ctxClone, "[Keepalive] Keepalive probe failed: %v", err)
		return err
	}

	log.InfoContextf(ctxClone, "[Keepalive] Keepalive probe completed in %v", time.Since(startTime))
	return nil
}
