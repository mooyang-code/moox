package dnsproxy

import (
	"context"

	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// HandleDNSProbeSchedule trpc定时器[入口函数] - 定时探测DNS并更新缓存
// 该定时器独立运行，不与心跳上报耦合
// 执行频率: 每30秒一次
// 执行逻辑: 合并本地DNS + 所有终端DNS → 探测所有IP → 缓存结果(5分钟)
func HandleDNSProbeSchedule(ctx context.Context, params string) error {
	ctxClone := trpc.CloneContext(ctx)
	log.InfoContextf(ctxClone, "[DNSProxy] ========== DNS Probe Task Started ==========")

	// 执行核心探测逻辑
	if err := MergeAndDNSProbeAllDomains(ctxClone); err != nil {
		log.ErrorContextf(ctxClone, "[DNSProxy] DNS Probe task failed: %v", err)
		// 不返回错误，避免导致服务启动失败
		return nil
	}

	log.InfoContextf(ctxClone, "[DNSProxy] ========== DNS Probe Task Completed ==========")
	return nil
}
