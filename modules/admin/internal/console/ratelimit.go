package console

import (
	"context"

	"golang.org/x/time/rate"
	"trpc.group/trpc-go/trpc-go/log"
)

// defaultMethodLimits 是配置里没写时的接口级限额；配置里写了的以配置为准。
var defaultMethodLimits = map[string]MethodLimit{
	"/api/admin/auth/Login":       {QPS: 1, Burst: 2},  // 登录接口限制更严格
	"/api/admin/auth/GetUserInfo": {QPS: 5, Burst: 10}, // 获取用户信息
}

// methodLimiters 只对配置了接口级限额的方法限流；未配置的方法不共用全局桶，避免一处浏览把别的请求挤掉。
type methodLimiters map[string]*rate.Limiter

func newMethodLimiters(cfg *Config) methodLimiters {
	limits := make(map[string]MethodLimit, len(defaultMethodLimits))
	for method, limit := range defaultMethodLimits {
		limits[method] = limit
	}
	if cfg != nil {
		for method, limit := range cfg.RateLimit.MethodLimits {
			limits[method] = limit
		}
	}
	limiters := make(methodLimiters, len(limits))
	for method, limit := range limits {
		limiters[method] = rate.NewLimiter(rate.Limit(limit.QPS), limit.Burst)
	}
	return limiters
}

func (l methodLimiters) allow(ctx context.Context, method string) bool {
	limiter, exists := l[method]
	if !exists || limiter.Allow() {
		return true
	}
	log.WarnContextf(ctx, "接口 [%s] 超过流量限制", method)
	return false
}
