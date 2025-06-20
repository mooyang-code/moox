package middleware

import (
	"context"
	"sync"

	gatewayConfig "github.com/mooyang-code/moox/server/internal/config"
	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/log"

	"golang.org/x/time/rate"
)

// RateLimitManager 流量控制管理器
type RateLimitManager struct {
	config *gatewayConfig.RateLimitConfig

	// 全局限流器
	globalLimiter *rate.Limiter

	// 接口级别限流器
	methodLimiters map[string]*rate.Limiter
}

// 全局流量控制管理器
var (
	rateLimitManager *RateLimitManager
	rateLimitOnce    sync.Once
)

func init() {
	// 注册流量控制中间件
	filter.Register("ratelimit", RateLimit(), nil)
	log.Infof("流量控制中间件注册成功")
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
func getDefaultRateLimitConfig() *gatewayConfig.RateLimitConfig {
	return &gatewayConfig.RateLimitConfig{
		DefaultQPS:   10,
		DefaultBurst: 20,
		MethodLimits: map[string]gatewayConfig.MethodLimit{
			"/gateway/auth/Login":        {QPS: 1, Burst: 2},  // 登录接口限制更严格
			"/gateway/auth/Register":     {QPS: 2, Burst: 4},  // 注册接口限制更严格
			"/gateway/auth/GetLoginSalt": {QPS: 2, Burst: 4},  // 获取盐值接口
			"/gateway/auth/GetUserInfo":  {QPS: 5, Burst: 10}, // 获取用户信息
		},
	}
}

// loadRateLimitConfig 加载流量控制配置
func loadRateLimitConfig() *gatewayConfig.RateLimitConfig {
	cfg, err := gatewayConfig.LoadConfig()
	if err != nil {
		log.Errorf("加载网关配置失败: %v", err)
		// 使用默认配置
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
		rateLimitConfig.MethodLimits = defaultConfig.MethodLimits
	}

	log.Infof("加载流量控制配置成功，全局QPS: %d, 突发: %d, 接口配置数量: %d",
		rateLimitConfig.DefaultQPS, rateLimitConfig.DefaultBurst, len(rateLimitConfig.MethodLimits))
	return rateLimitConfig
}

// newRateLimitManager 创建流量控制管理器
func newRateLimitManager(config *gatewayConfig.RateLimitConfig) *RateLimitManager {
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

// RateLimit 流量控制中间件
func RateLimit() filter.ServerFilter {
	return func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		ctxMsg := trpc.Message(ctx)
		rpcName := ctxMsg.ServerRPCName()

		// 获取TraceID（用于日志追踪）
		traceIDBytes := trpc.GetMetaData(ctx, model.CtxTraceID)
		traceID := string(traceIDBytes)

		// 检查流量限制
		manager := getRateLimitManager()
		if !manager.checkRateLimit(ctx, rpcName) {
			log.WarnContextf(ctx, "接口 [%s] 流量被限制，TraceID: %s", rpcName, traceID)
			return createRateLimitResponse(), nil
		}

		log.DebugContextf(ctx, "接口 [%s] 流量检查通过，TraceID: %s", rpcName, traceID)
		// 继续执行下一个处理器
		return next(ctx, req)
	}
}

// createRateLimitResponse 创建流量限制响应
func createRateLimitResponse() interface{} {
	return &pb.GetUserInfoRsp{
		Code:    pb.EnumMooxErrorCode_INNER_ERR,
		Message: "请求过于频繁，请稍后再试",
	}
}
