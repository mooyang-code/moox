package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/mooyang-code/moox/cli/internal/config"
)

// AuthOperator 认证服务操作类
type AuthOperator struct {
	httpClient *http.Client
	baseURL    string
	cfg        *config.Config
}

// RegisterRequest 注册请求结构
type RegisterRequest struct {
	AppInfo  AppInfo `json:"app_info"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	Nickname string  `json:"nickname,omitempty"`
	Email    string  `json:"email,omitempty"`
}

// AppInfo 应用信息
type AppInfo struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// UserInfo 用户信息
type UserInfo struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Avatar      string `json:"avatar"`
	Status      int32  `json:"status"`
	Role        int32  `json:"role"`
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt int64  `json:"last_login_at"`
	LastLoginIP string `json:"last_login_ip"`
}

// RegisterResponse 注册响应结构
type RegisterResponse struct {
	Code     int32     `json:"code"`
	Message  string    `json:"message"`
	UserID   string    `json:"user_id"`
	UserInfo *UserInfo `json:"user_info"`
}

// NewAuthOperator 创建认证服务操作实例
func NewAuthOperator(cfg *config.Config) *AuthOperator {
	return &AuthOperator{
		httpClient: &http.Client{},
		baseURL:    "http://" + cfg.Moox.AuthTarget,
		cfg:        cfg,
	}
}

// RegisterUser 注册用户
func (a *AuthOperator) RegisterUser(ctx context.Context, username, password, nickname, email string) (*RegisterResponse, error) {
	// 构建注册请求
	req := &RegisterRequest{
		AppInfo: AppInfo{
			AppID:  "moox-cli",     // CLI工具的应用ID
			AppKey: "moox-cli-key", // CLI工具的应用密钥
		},
		Username: username,
		Password: password,
		Nickname: nickname,
		Email:    email,
	}

	// 序列化请求
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %v", err)
	}

	// 创建HTTP请求
	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/trpc.moox.server.AuthAPI/Register", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("发送HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	// 解析响应
	var registerResp RegisterResponse
	if err := json.Unmarshal(respBody, &registerResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &registerResp, nil
}
