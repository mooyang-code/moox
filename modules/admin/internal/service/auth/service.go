package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// 用户不存在与密码错误给同一句话，不让人借提示判断用户名是否存在。
const wrongCredentials = "用户名或密码错误"

// failureWindow 是未锁定时失败计数的保留时间：最后一次失败满 30 分钟后计数清零。
const failureWindow = 30 * time.Minute

// placeholderPasswordHash 是用户不存在时拿来做一次 bcrypt 校验的固定哈希（与真实密码同一种哈希、同一代价），
// 让“用户不存在”和“密码错误”耗时一致，不能借响应时间判断用户名是否存在。服务初始化时先算好。
var placeholderPasswordHash = sync.OnceValue(func() string {
	hash, err := mooxsecurity.HashPassword("moox-login-timing-placeholder")
	if err != nil {
		panic(fmt.Sprintf("生成登录占位哈希失败: %v", err))
	}
	return hash
})

// verifyPassword 在测试里替换，用来确认用户不存在时也做了一次校验。
var verifyPassword = mooxsecurity.VerifyPassword

// LoginRecorder 在登录成功后把登录审计写入全局活动日志；为空时不记录。
type LoginRecorder func(ctx context.Context, userID, username, clientIP, userAgent string) error

// Service 认证服务实现（实现 pb.AuthService）。
type Service struct {
	pb.UnimplementedAuth
	cfg      *Config
	userDAO  *UserDAO
	sessions *SessionStore
	failures *loginFailures
	record   LoginRecorder
	now      func() time.Time
}

// NewService 新建认证服务；sessions 是与控制台共用的浏览器会话表，record 可为空。
func NewService(cfg *Config, dbManager *database.Manager, sessions *SessionStore, record LoginRecorder) (*Service, error) {
	if dbManager == nil || dbManager.GetDB() == nil {
		return nil, fmt.Errorf("数据库连接未初始化")
	}
	if cfg == nil || sessions == nil {
		return nil, fmt.Errorf("认证服务需要配置与会话表")
	}
	placeholderPasswordHash()
	return &Service{
		cfg:      cfg,
		userDAO:  NewUserDAO(dbManager.GetDB()),
		sessions: sessions,
		failures: newLoginFailures(),
		record:   record,
		now:      time.Now,
	}, nil
}

// Login 用户登录：密码是明文（HTTPS 保护），bcrypt 校验通过后新建会话。会话 ID 交给控制台写进 Cookie，不进响应体。
func (s *Service) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	login := LoginFrom(ctx)
	if login == nil {
		return loginRejected(pb.ErrorCode_INNER_ERR, "登录只能经管理台网页入口"), nil
	}
	log.InfoContextf(ctx, "[Auth] Login username=%s ip=%s", req.GetUsername(), login.ClientIP)

	if err := validateCredentialFormat(req.GetUsername(), req.GetPassword()); err != nil {
		return loginRejected(pb.ErrorCode_INVALID_PARAM, err.Error()), nil
	}
	now := s.now()
	if s.failures.locked(req.GetUsername(), login.ClientIP, s.cfg.Security.MaxLoginAttempt, now) {
		return loginRejected(pb.ErrorCode_NO_AUTH, "账户已被锁定，请稍后再试"), nil
	}

	user, err := s.userDAO.GetUserByUsername(ctx, req.GetUsername())
	hash := placeholderPasswordHash()
	if err == nil {
		hash = user.PasswordHash
	}
	if !verifyPassword(req.GetPassword(), hash) || err != nil {
		s.failures.fail(req.GetUsername(), login.ClientIP, s.cfg.Security, now)
		return loginRejected(pb.ErrorCode_NO_AUTH, wrongCredentials), nil
	}
	s.failures.clear(req.GetUsername(), login.ClientIP)

	created, err := s.sessions.Create(user.UserID, user.Username)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 新建会话失败: %v", err)
		return loginRejected(pb.ErrorCode_INNER_ERR, "登录失败"), nil
	}
	login.Issued = &created
	if err := s.userDAO.UpdateUserLoginInfo(ctx, user.UserID, login.ClientIP); err != nil {
		log.WarnContextf(ctx, "[Auth] 更新登录信息失败: %v", err)
	}
	if s.record != nil {
		if err := s.record(ctx, user.UserID, user.Username, login.ClientIP, login.UserAgent); err != nil {
			log.ErrorContextf(ctx, "[Auth] 写入登录活动日志失败: %v", err)
		}
	}

	return &pb.LoginRsp{
		RetInfo:   &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "登录成功"},
		UserInfo:  BuildSafeUserInfo(user),
		ExpiresAt: created.ExpiresAt.UnixMilli(),
	}, nil
}

// Logout 删除当前会话；会话 ID 由控制台按 Cookie 查到后写进 metadata，请求体里没有。Cookie 由控制台清除。
func (s *Service) Logout(ctx context.Context, _ *pb.LogoutReq) (*pb.LogoutRsp, error) {
	sid := string(trpc.GetMetaData(ctx, CtxSessionID))
	if sid == "" {
		return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "用户身份验证失败"}}, nil
	}
	s.sessions.Delete(sid)
	return &pb.LogoutRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "退出成功"}}, nil
}

// GetUserInfo 获取当前登录用户的信息。
func (s *Service) GetUserInfo(ctx context.Context, _ *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	log.InfoContext(ctx, "[Auth] # GetUserInfo")

	currentUserID, _, err := GetUserInfoFromCtx(ctx)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NO_AUTH,
				Msg:  "用户身份验证失败." + err.Error(),
			},
		}, nil
	}

	user, err := s.userDAO.GetUserByID(ctx, currentUserID)
	if err != nil {
		return &pb.GetUserInfoRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.ErrorCode_NOT_FOUND,
				Msg:  "用户不存在",
			},
		}, nil
	}

	return &pb.GetUserInfoRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode_SUCCESS,
			Msg:  "获取用户信息成功",
		},
		UserInfo: BuildSafeUserInfo(user),
	}, nil
}

func loginRejected(code pb.ErrorCode, msg string) *pb.LoginRsp {
	return &pb.LoginRsp{RetInfo: &pb.RetInfo{Code: code, Msg: msg}}
}

func validateCredentialFormat(username, password string) error {
	if err := ValidateStringFormat(username, "用户名"); err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("密码不能为空")
	}
	return nil
}

// loginFailures 是登录失败计数，按“用户名 + 来源 IP”记，只在内存里（Admin 重启即清零）。
// 规则：连续失败到 max_login_attempt 次（用户不存在也算）就锁定 lock_duration；锁定期间直接拒绝，
// 不计数、不延长；锁定结束后记录消失；登录成功清掉记录。
type loginFailures struct {
	mu      sync.Mutex
	entries map[string]loginFailure
}

type loginFailure struct {
	count     int
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

func (f *loginFailures) fail(username, ip string, security SecurityConfig, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
