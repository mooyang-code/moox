package console

import (
	"context"
	"sync"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"golang.org/x/time/rate"
	"trpc.group/trpc-go/trpc-go/log"
)

// 全局流量控制管理器
var (
	rateLimitManager *RateLimitManager
	rateLimitOnce    sync.Once
)

// RateLimitManager 流量控制管理器
type RateLimitManager struct {
	config         *RateLimitConfig
	globalLimiter  *rate.Limiter            // 全局限流器
	methodLimiters map[string]*rate.Limiter // 接口级别限流器
}

// getRateLimitManager 获取流量控制管理器实例（单例模式）
func getRateLimitManager() *RateLimitManager {
	rateLimitOnce.Do(func() {
		config := loadRateLimitConfig()
		rateLimitManager = newRateLimitManager(config)
	})
	return rateLimitManager
}

// getDefaultRateLimitConfig 获取默认流量控制配置
func getDefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		DefaultQPS:   10,
		DefaultBurst: 20,
		MethodLimits: map[string]MethodLimit{
			"/api/admin/auth/Login":        {QPS: 1, Burst: 2},  // 登录接口限制更严格
			"/api/admin/auth/GetLoginSalt": {QPS: 2, Burst: 4},  // 获取盐值接口
			"/api/admin/auth/GetUserInfo":  {QPS: 5, Burst: 10}, // 获取用户信息
		},
	}
}

// loadRateLimitConfig 加载流量控制配置
func loadRateLimitConfig() *RateLimitConfig {
	// 从控制台配置获取
	cfg := GetConfig()
	if cfg == nil {
		log.Warn("控制台配置未初始化，使用默认限流配置")
		return getDefaultRateLimitConfig()
	}

	// 获取配置文件中的限流配置
	rateLimitConfig := &cfg.RateLimit

	// 如果配置文件中的限流配置为空，使用默认值
	defaultConfig := getDefaultRateLimitConfig()
	if rateLimitConfig.DefaultQPS == 0 {
		rateLimitConfig.DefaultQPS = defaultConfig.DefaultQPS
	}
	if rateLimitConfig.DefaultBurst == 0 {
		rateLimitConfig.DefaultBurst = defaultConfig.DefaultBurst
	}
	if rateLimitConfig.MethodLimits == nil {
		rateLimitConfig.MethodLimits = make(map[string]MethodLimit)
	}
	for method, limit := range defaultConfig.MethodLimits {
		if _, ok := rateLimitConfig.MethodLimits[method]; !ok {
			rateLimitConfig.MethodLimits[method] = limit
		}
	}

	log.Infof("加载流量控制配置成功，全局QPS: %d, 突发: %d, 接口配置数量: %d",
		rateLimitConfig.DefaultQPS, rateLimitConfig.DefaultBurst, len(rateLimitConfig.MethodLimits))
	return rateLimitConfig
}

// newRateLimitManager 创建流量控制管理器
func newRateLimitManager(config *RateLimitConfig) *RateLimitManager {
	manager := &RateLimitManager{
		config:         config,
		globalLimiter:  rate.NewLimiter(rate.Limit(config.DefaultQPS), config.DefaultBurst),
		methodLimiters: make(map[string]*rate.Limiter),
	}

	// 初始化接口级别限流器
	for method, limit := range config.MethodLimits {
		manager.methodLimiters[method] = rate.NewLimiter(rate.Limit(limit.QPS), limit.Burst)
	}
	return manager
}

// checkRateLimit 检查是否超过流量限制
func (m *RateLimitManager) checkRateLimit(ctx context.Context, method string) bool {
	// 检查接口级别限流
	if methodLimiter, exists := m.methodLimiters[method]; exists {
		if !methodLimiter.Allow() {
			log.WarnContextf(ctx, "接口 [%s] 超过流量限制", method)
			return false
		}
	} else {
		// 使用全局限流器
		if !m.globalLimiter.Allow() {
			log.WarnContextf(ctx, "全局流量超过限制")
			return false
		}
	}
	return true
}

// allowRequest 按接口路径做限流，超过限制时返回 false。
func allowRequest(ctx context.Context, path string) bool {
	return getRateLimitManager().checkRateLimit(ctx, path)
}

// createRateLimitResponse 创建流量限制响应
func createRateLimitResponse() interface{} {
	return &middlewareResp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode_INNER_ERR,
			Msg:  "请求过于频繁，请稍后再试",
		},
	}
}
