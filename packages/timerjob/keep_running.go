package timerjob

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
)

var keepRunningFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "moox_timer_keep_running_failures_total",
	Help: "Timer callback failures that were retried instead of stopping the service.",
}, []string{"job"})

func init() {
	prometheus.MustRegister(keepRunningFailures)
}

// KeepRunning 让定时任务的失败不影响服务。定时器库配了 startAtOnce 时，首次执行返回错误会让服务退出；
// 这时依赖（比如 EventBus）可能还没就绪，服务要留下来等下一轮重试。失败记警告和指标，就绪状态由各服务自己维护。
func KeepRunning(name string, handler func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := handler(ctx); err != nil {
			keepRunningFailures.WithLabelValues(name).Inc()
			log.WarnContextf(ctx, "定时任务 %s 执行失败，下一轮重试：%v", name, err)
		}
		return nil
	}
}
