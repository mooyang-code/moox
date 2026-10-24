package impl

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/activitylog"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/log"
)

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

// recordLoginLog 把成功登录写进全局活动日志（不属于任何空间）；写入失败只记日志，不影响登录。
func (s *AuthServiceImpl) recordLoginLog(ctx context.Context, user *model.User, req *pb.LoginReq) {
	if s.loginLog == nil {
		return
	}
	detail, err := json.Marshal(map[string]string{
		"username":   user.Username,
		"ip":         req.ClientIp,
		"user_agent": req.UserAgent,
	})
	if err != nil {
		return
	}
	entry := activitylog.Entry{
		Source:   model.LoginLogSource,
		Category: model.LoginLogCategory,
		Level:    "info",
		Title:    "用户 " + user.Username + " 登录",
		Detail:   string(detail),
		RefID:    user.UserID,
	}
	if _, err := s.loginLog.Append(ctx, []activitylog.Entry{entry}, time.Now()); err != nil {
		log.ErrorContextf(ctx, "[Auth] 写入登录活动日志失败: %v", err)
	}
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
