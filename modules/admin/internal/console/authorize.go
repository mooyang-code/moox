package console

import (
	"context"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"

	"trpc.group/trpc-go/trpc-go/log"
)

// middlewareResp 是控制台鉴权、限流失败时的响应体，保持 {ret_info} 格式与业务 RPC 一致。
type middlewareResp struct {
	RetInfo *pb.RetInfo `json:"ret_info"`
}

// ShouldSkipAuth 检查是否应该跳过鉴权
func ShouldSkipAuth(rpcName string) bool {
	cfg := GetConfig()
	if cfg == nil {
		return false
	}

	for _, method := range cfg.Gateway.NoAuthMethods {
		if method == rpcName {
			return true
		}
	}
	return false
}

// getJWTSecretKey 获取JWT密钥（从网关配置）
func getJWTSecretKey() string {
	cfg := GetConfig()
	if cfg == nil {
		log.Error("网关配置未初始化")
		return ""
	}
	return cfg.JWT.SecretKey
}

// validateAccessToken 验证访问令牌并返回用户信息
type accessClaims struct {
	UserID    string
	Username  string
	Role      int32
	SessionID string
	ExpiresAt time.Time
}

func validateAccessToken(ctx context.Context, accessToken string) (*accessClaims, bool) {
	// 获取JWT密钥（带缓存）
	secretKey := getJWTSecretKey()
	if secretKey == "" {
		log.ErrorContext(ctx, "JWT密钥为空")
		return nil, false
	}

	// 验证API访问令牌
	claims, err := mooxsecurity.ParseToken(accessToken, secretKey)
	if err != nil {
		log.ErrorContextf(ctx, "JWT令牌验证失败: %v", err)
		return nil, false
	}
	if claims["iss"] != "moox-admin" || claims["token_type"] != "access" {
		return nil, false
	}
	userID, ok := claims["user_id"].(string)
	if !ok || userID == "" {
		return nil, false
	}
	username, _ := claims["username"].(string)
	role, _ := claims["role"].(float64)
	sessionID, _ := claims["sid"].(string)
	expiresUnix, ok := claims["exp"].(float64)
	if sessionID == "" || !ok {
		return nil, false
	}
	expiresAt := time.Unix(int64(expiresUnix), 0)
	if !time.Now().Before(expiresAt) {
		return nil, false
	}
	return &accessClaims{UserID: userID, Username: username, Role: int32(role), SessionID: sessionID, ExpiresAt: expiresAt}, true
}
