package middleware

import (
	"context"
	"strings"
	"sync"

	gatewayConfig "github.com/mooyang-code/moox/server/internal/config"
	authConfig "github.com/mooyang-code/moox/server/internal/service/auth/config"
	"github.com/mooyang-code/moox/server/internal/service/auth/util"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/log"
)

// 全局配置缓存
var (
	globalAuthConfig    *authConfig.Config
	globalGatewayConfig *gatewayConfig.Config
	noAuthMethodsMap    map[string]bool
	configMutex         sync.RWMutex
)

// getJWTSecretKey 获取JWT密钥（带缓存）
func getJWTSecretKey(ctx context.Context) string {
	configMutex.RLock()
	if globalAuthConfig != nil {
		secretKey := globalAuthConfig.JWT.SecretKey
		configMutex.RUnlock()
		return secretKey
	}
	configMutex.RUnlock()

	// 双重检查锁定模式
	configMutex.Lock()
	defer configMutex.Unlock()

	if globalAuthConfig != nil {
		return globalAuthConfig.JWT.SecretKey
	}

	// 加载配置
	cfg, err := authConfig.LoadConfig()
	if err != nil {
		log.ErrorContextf(ctx, "加载JWT配置失败: %v", err)
		return ""
	}

	globalAuthConfig = cfg
	return cfg.JWT.SecretKey
}

// isNoAuthMethod 检查是否为不需要鉴权的接口
func isNoAuthMethod(rpcName string) bool {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return noAuthMethodsMap[rpcName]
}

// loadNoAuthMethods 加载不需要鉴权的接口列表
func loadNoAuthMethods() {
	cfg, err := gatewayConfig.LoadConfig()
	if err != nil {
		log.Errorf("加载网关配置失败: %v", err)
		// 使用默认配置
		noAuthMethodsMap = map[string]bool{
			"/trpc.moox.server.AuthAPI/Register":     true,
			"/trpc.moox.server.AuthAPI/GetLoginSalt": true,
			"/trpc.moox.server.AuthAPI/Login":        true,
			"/trpc.moox.gateway.stdhttp/health":      true,
		}
		return
	}

	globalGatewayConfig = cfg
	noAuthMethodsMap = make(map[string]bool)
	for _, method := range cfg.Gateway.NoAuthMethods {
		noAuthMethodsMap[method] = true
	}

	log.Infof("加载不需要鉴权的接口列表: %v", cfg.Gateway.NoAuthMethods)
}

func init() {
	// 注册中间件
	filter.Register("authorize", Authorize(), nil)

	// 初始化认证配置
	cfg, err := authConfig.LoadConfig()
	if err != nil {
		log.Errorf("初始化认证配置失败: %v", err)
	} else {
		configMutex.Lock()
		globalAuthConfig = cfg
		configMutex.Unlock()
		log.Infof("认证配置初始化成功")
	}

	// 加载不需要鉴权的接口列表
	loadNoAuthMethods()
}

// Authorize 从 HTTP header 中获取访问令牌进行鉴权
func Authorize() filter.ServerFilter {
	return func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		ctxMsg := trpc.Message(ctx)
		rpcName := ctxMsg.ServerRPCName()

		// 检查是否需要鉴权
		if isNoAuthMethod(rpcName) {
			log.InfoContextf(ctx, "接口 [%s] 无需鉴权，直接通过", rpcName)
			return next(ctx, req)
		}

		header := thttp.Head(ctx)
		if header == nil {
			log.ErrorContext(ctx, "获取HTTP头失败")
			return createAuthFailResponse(), nil
		}

		// 从请求中获取访问令牌
		accessToken := getAccessTokenFromRequest(ctx, header, req)
		if accessToken == "" {
			log.ErrorContextf(ctx, "接口 [%s] 未找到访问令牌", rpcName)
			return createAuthFailResponse(), nil
		}

		// 验证访问令牌
		claims, valid := validateAccessToken(ctx, accessToken)
		if !valid {
			log.ErrorContextf(ctx, "接口 [%s] 访问令牌验证失败", rpcName)
			return createAuthFailResponse(), nil
		}

		// 将用户信息保存到上下文中（底层接口需要这些信息）
		ctx = context.WithValue(ctx, "user_id", claims.UserID)
		ctx = context.WithValue(ctx, "username", claims.Username)
		ctx = context.WithValue(ctx, "user_role", claims.Role)
		log.InfoContextf(ctx, "接口 [%s] 鉴权通过", rpcName)

		// 继续执行下一个处理器
		rsp, err := next(ctx, req)
		if err != nil {
			log.ErrorContextf(ctx, "接口 [%s] 执行失败: %v", rpcName, err)
		}
		return rsp, nil
	}
}

// getAccessTokenFromRequest 从请求中获取访问令牌
// 优先级：1. 请求体中的access_token字段 2. Authorization头 3. X-Access-Token头
func getAccessTokenFromRequest(ctx context.Context, header *thttp.Header, req interface{}) string {
	// 1. 尝试从请求体中获取access_token字段
	if tokenReq, ok := req.(accessTokenProvider); ok {
		if token := tokenReq.GetAccessToken(); token != "" {
			log.DebugContextf(ctx, "从请求体获取到访问令牌")
			return token
		}
	}

	// 2. 尝试从Authorization头获取 (Bearer token格式)
	if authHeaders, ok := header.Request.Header["Authorization"]; ok && len(authHeaders) > 0 {
		authHeader := authHeaders[0]
		if strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token != "" {
				log.DebugContextf(ctx, "从Authorization头获取到访问令牌")
				return token
			}
		}
	}

	// 3. 尝试从X-Access-Token头获取
	if tokenHeaders, ok := header.Request.Header["X-Access-Token"]; ok && len(tokenHeaders) > 0 {
		if token := tokenHeaders[0]; token != "" {
			log.DebugContextf(ctx, "从X-Access-Token头获取到访问令牌")
			return token
		}
	}

	return ""
}

// validateAccessToken 验证访问令牌并返回用户信息
func validateAccessToken(ctx context.Context, accessToken string) (*util.JWTClaims, bool) {
	// 获取JWT密钥（带缓存）
	secretKey := getJWTSecretKey(ctx)
	if secretKey == "" {
		log.ErrorContext(ctx, "JWT密钥为空")
		return nil, false
	}

	// 验证JWT令牌
	claims, err := util.ParseJWT(accessToken, secretKey)
	if err != nil {
		log.ErrorContextf(ctx, "JWT令牌验证失败: %v", err)
		return nil, false
	}

	return claims, true
}

// createAuthFailResponse 创建鉴权失败响应
func createAuthFailResponse() interface{} {
	return &pb.GetUserInfoRsp{
		Code:    pb.EnumMooxErrorCode_NO_AUTH,
		Message: "访问令牌无效，请退出重新登录(gateway)",
	}
}

// accessTokenProvider 定义获取访问令牌的接口
type accessTokenProvider interface {
	GetAccessToken() string
}
