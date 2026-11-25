package impl

import (
	"context"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	authutils "github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"

	"trpc.group/trpc-go/trpc-go/log"
)

// 用户不存在与密码错误给同一句话，不让人借提示判断用户名是否存在。
const wrongCredentials = "用户名或密码错误"

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

// Login 用户登录：密码是明文（HTTPS 保护），bcrypt 校验通过后新建会话。会话 ID 交给控制台写进 Cookie，不进响应体。
func (s *AuthServiceImpl) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	login := session.LoginFrom(ctx)
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
	// 会话建好后才写全局活动日志，登录失败不留“登录”记录
	s.recordLoginLog(ctx, user, login)

	return &pb.LoginRsp{
		RetInfo:   &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "登录成功"},
		UserInfo:  authutils.BuildSafeUserInfo(user),
		ExpiresAt: created.ExpiresAt.UnixMilli(),
	}, nil
}

func loginRejected(code pb.ErrorCode, msg string) *pb.LoginRsp {
	return &pb.LoginRsp{RetInfo: &pb.RetInfo{Code: code, Msg: msg}}
}

// validateCredentialFormat 校验用户名格式与密码非空；密码长度不限（bcrypt 只看前 72 字节，由设定密码时把关）。
func validateCredentialFormat(username, password string) error {
	if err := authutils.ValidateStringFormat(username, "用户名"); err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("密码不能为空")
	}
	return nil
}
