package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/cli/internal/config"
	pb "github.com/mooyang-code/moox/server/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

// AuthOperator 认证服务操作类
type AuthOperator struct {
	cfg *config.Config
}

// NewAuthOperator 创建认证服务操作实例
func NewAuthOperator(cfg *config.Config) *AuthOperator {
	return &AuthOperator{
		cfg: cfg,
	}
}

// RegisterUser 注册用户
func (a *AuthOperator) RegisterUser(ctx context.Context, username, password, nickname, email string) (*pb.RegisterRsp, error) {
	if a.cfg == nil {
		return nil, fmt.Errorf("认证服务配置不存在")
	}
	target := a.cfg.MooX.AuthTarget
	if target == "" {
		return nil, fmt.Errorf("认证服务地址未配置")
	}

	// 创建 tRPC 客户端代理
	authClient := pb.NewAuthAPIClientProxy(client.WithTarget("ip://" + target))

	// 构建注册请求
	req := &pb.RegisterReq{
		AppInfo: &pb.AppInfo{
			AppId:  "moox-cli",     // CLI工具的应用ID
			AppKey: "moox-cli-key", // CLI工具的应用密钥
		},
		Username: username,
		Password: password,
		Nickname: nickname,
		Email:    email,
	}

	// 设置请求超时
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// 发送 tRPC 请求
	rsp, err := authClient.Register(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("发送tRPC请求失败: %v", err)
	}

	return rsp, nil
}
