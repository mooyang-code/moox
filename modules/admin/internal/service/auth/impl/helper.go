package impl

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/activitylog"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"trpc.group/trpc-go/trpc-go/log"
)

// failureWindow 是未锁定时失败计数的保留时间：最后一次失败满 30 分钟后计数清零。
const failureWindow = 30 * time.Minute

// loginFailures 是登录失败计数，按“用户名 + 来源 IP”记，只在内存里（Admin 重启即清零）。
// 规则：连续失败到 max_login_attempt 次（用户不存在也算）就锁定 lock_duration；锁定期间直接拒绝，
// 不计数、不延长；锁定结束后记录消失；登录成功清掉记录。
type loginFailures struct {
	mu      sync.Mutex
	entries map[string]loginFailure
}

type loginFailure struct {
	count int
	// expiresAt 是这条记录失效的时间：未锁定时是最后一次失败后 failureWindow，锁定后是锁定结束。
	expiresAt time.Time
}

func newLoginFailures() *loginFailures {
	return &loginFailures{entries: make(map[string]loginFailure)}
}

func failureKey(username, ip string) string { return username + "\x00" + ip }

func (f *loginFailures) locked(username, ip string, maxAttempts int, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[failureKey(username, ip)]
	return ok && now.Before(entry.expiresAt) && entry.count >= maxAttempts
}

func (f *loginFailures) fail(username, ip string, security config.SecurityConfig, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 写入时顺手清掉过期记录，换着用户名试也撑不大这张表
	for key, entry := range f.entries {
		if !now.Before(entry.expiresAt) {
			delete(f.entries, key)
		}
	}
	key := failureKey(username, ip)
	entry := f.entries[key]
	entry.count++
	entry.expiresAt = now.Add(failureWindow)
	if entry.count >= security.MaxLoginAttempt {
		entry.expiresAt = now.Add(security.LockDuration)
	}
	f.entries[key] = entry
}

func (f *loginFailures) clear(username, ip string) {
	f.mu.Lock()
	delete(f.entries, failureKey(username, ip))
	f.mu.Unlock()
}

// recordLoginLog 把成功登录写进全局活动日志（不属于任何空间）；写入失败只记日志，不影响登录。
func (s *AuthServiceImpl) recordLoginLog(ctx context.Context, user *model.User, login *session.Login) {
	if s.loginLog == nil {
		return
	}
	detail, err := json.Marshal(map[string]string{
		"username":   user.Username,
		"ip":         login.ClientIP,
		"user_agent": login.UserAgent,
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
