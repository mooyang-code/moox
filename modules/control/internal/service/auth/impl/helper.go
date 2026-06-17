package impl

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/control/proto/gen"
	"trpc.group/trpc-go/trpc-go/http"
)

// validateLoginSalt 验证登录盐值
func (s *AuthServiceImpl) validateLoginSalt(ctx context.Context, username, salt string, timestamp int64) bool {
	loginSalt, err := s.userDAO.GetLoginSalt(ctx, username)
	if err != nil {
		return false
	}

	return loginSalt.Salt == salt && loginSalt.Timestamp == timestamp && time.Now().Before(loginSalt.ExpiresAt)
}

// validateChangePasswordSalt 验证修改密码盐值
func (s *AuthServiceImpl) validateChangePasswordSalt(ctx context.Context, userID, salt string, timestamp int64) bool {
	changePwdSalt, err := s.userDAO.GetChangePasswordSalt(ctx, userID)
	if err != nil {
		return false
	}

	return changePwdSalt.Salt == salt && changePwdSalt.Timestamp == timestamp && time.Now().Before(changePwdSalt.ExpiresAt)
}

// isUserLocked 检查用户是否被锁定
func (s *AuthServiceImpl) isUserLocked(ctx context.Context, username, ip string) bool {
	attempt, err := s.userDAO.GetLoginAttempt(ctx, username, ip)
	if err != nil {
		return false
	}

	return attempt.Attempts >= s.cfg.Security.MaxLoginAttempt && time.Now().Before(attempt.ExpiresAt)
}

// recordLoginAttempt 记录登录尝试
func (s *AuthServiceImpl) recordLoginAttempt(ctx context.Context, username, ip string, success bool) {
	if success {
		// 登录成功，清除尝试记录
		s.userDAO.DeleteLoginAttempt(ctx, username, ip)
		return
	}

	// 登录失败，记录尝试次数
	attempt, err := s.userDAO.GetLoginAttempt(ctx, username, ip)
	if err != nil {
		attempt = &model.LoginAttempt{
			Username: username,
			IP:       ip,
			Attempts: 1,
		}
	} else {
		attempt.Attempts++
	}

	if attempt.Attempts >= s.cfg.Security.MaxLoginAttempt {
		attempt.LockedAt = time.Now()
		attempt.ExpiresAt = time.Now().Add(s.cfg.Security.LockDuration)
	}

	s.userDAO.SetLoginAttempt(ctx, username, ip, *attempt)
}

// recordLoginHistory 记录登录历史
func (s *AuthServiceImpl) recordLoginHistory(ctx context.Context, user *model.User, req *pb.LoginReq, result, reason string) {
	history := &model.LoginHistory{
		UserID:        user.UserID,
		Username:      user.Username,
		LoginType:     "password",
		ClientIP:      req.ClientIp,
		UserAgent:     req.UserAgent,
		DeviceID:      req.DeviceId,
		LoginResult:   result,
		FailureReason: reason,
	}

	s.userDAO.CreateLoginHistory(ctx, history)
}

// recordUserAction 记录用户操作日志
func (s *AuthServiceImpl) recordUserAction(ctx context.Context, userID, action, resource, details, clientIP, userAgent, result string) {
	userAction := &model.UserAction{
		UserID:    userID,
		Action:    action,
		Resource:  resource,
		Details:   details,
		ClientIP:  clientIP,
		UserAgent: userAgent,
		Result:    result,
	}

	s.userDAO.CreateUserAction(ctx, userAction)
}

// extractClientIPFromContext 从上下文中提取真实客户端IP
// 优先级：X-Client-Ip（网关传递）> X-Forwarded-For > X-Real-IP > fallback（请求体中的IP）
func (s *AuthServiceImpl) extractClientIPFromContext(ctx context.Context, fallbackIP string) string {
	// 尝试从TRPC HTTP头中获取
	head := http.Head(ctx)
	if head != nil {
		// 优先使用网关传递的X-Client-Ip（网关已经处理了代理链）
		if clientIP := head.Request.Header.Get("X-Client-Ip"); clientIP != "" {
			return strings.TrimSpace(clientIP)
		}

		// 其次尝试X-Forwarded-For（取第一个IP）
		if forwardedFor := head.Request.Header.Get("X-Forwarded-For"); forwardedFor != "" {
			// X-Forwarded-For 格式: client, proxy1, proxy2
			ips := strings.Split(forwardedFor, ",")
			if len(ips) > 0 {
				return strings.TrimSpace(ips[0])
			}
		}

		// 再尝试X-Real-IP
		if realIP := head.Request.Header.Get("X-Real-IP"); realIP != "" {
			return strings.TrimSpace(realIP)
		}
	}

	// 如果都获取不到，使用fallback（请求体中的IP，可能是客户端直连时填写的）
	if fallbackIP != "" {
		return fallbackIP
	}
	return "unknown"
}
