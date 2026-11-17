package impl

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/activitylog"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 短密码：原来按密文长度要求至少 32 个字符，改明文后不能再拦短密码。
const testPassword = "abc123"

type loginFixture struct {
	svc *AuthServiceImpl
	now time.Time
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 共享内存库在最后一个连接关闭时才销毁；不关的话 -count=N 的下一轮会读到上一轮的数据。
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	passwordHash, err := mooxsecurity.HashPassword(testPassword)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.User{UserID: "user-login-1", Username: "admin", PasswordHash: passwordHash, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error)
	sessions, err := session.NewStore(24 * time.Hour)
	require.NoError(t, err)
	f := &loginFixture{now: time.Unix(1_800_000_000, 0)}
	f.svc = &AuthServiceImpl{
		// 与 console.yaml 的默认值一致：5 次锁 15 分钟。
		cfg:      &authconfig.Config{Session: authconfig.SessionConfig{TTL: 24 * time.Hour}, Security: authconfig.SecurityConfig{MaxLoginAttempt: 5, LockDuration: 15 * time.Minute}},
		userDAO:  dao.NewUserDAO(db),
		sessions: sessions,
		failures: newLoginFailures(),
		loginLog: activitylog.NewDAO(db),
		now:      func() time.Time { return f.now },
	}
	return f
}

func (f *loginFixture) login(t *testing.T, username, password, ip string) (*pb.LoginRsp, *session.Login) {
	t.Helper()
	carrier := &session.Login{ClientIP: ip, UserAgent: "test-agent"}
	rsp, err := f.svc.Login(session.WithLogin(context.Background(), carrier), &pb.LoginReq{Username: username, Password: password})
	require.NoError(t, err)
	return rsp, carrier
}

func TestLoginWithPlainPasswordIssuesSessionOutsideResponse(t *testing.T) {
	f := newLoginFixture(t)
	rsp, carrier := f.login(t, "admin", testPassword, "203.0.113.7")
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.NotNil(t, carrier.Issued)
	assert.Len(t, carrier.Issued.ID, 64)
	assert.Equal(t, "user-login-1", carrier.Issued.UserID)
	assert.Equal(t, carrier.Issued.ExpiresAt.Unix(), rsp.GetExpiresAt())
	assert.Equal(t, "admin", rsp.GetUserInfo().GetUsername())
	stored, ok := f.svc.sessions.Get(carrier.Issued.ID)
	require.True(t, ok)
	assert.Equal(t, "admin", stored.Username)

	// 成功登录写入一条全局活动日志，IP 取控制台给的来源地址。
	items, total, err := f.svc.loginLog.List(context.Background(), activitylog.Filter{Global: true, Category: model.LoginLogCategory})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, "user-login-1", items[0].RefID)
	assert.Contains(t, items[0].Detail, "203.0.113.7")
	assert.NotContains(t, items[0].Detail, testPassword)
	user, err := f.svc.userDAO.GetUserByID(context.Background(), "user-login-1")
	require.NoError(t, err)
	assert.Equal(t, "203.0.113.7", user.LastLoginIP)
}

func TestLoginRejectsInvalidFormatAndCallsOutsideConsole(t *testing.T) {
	f := newLoginFixture(t)
	rsp, carrier := f.login(t, "", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	assert.Nil(t, carrier.Issued)
	rsp, _ = f.login(t, "admin", "", "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	// 不经控制台（没有登录上下文）时拿不到 Cookie，直接拒绝。
	direct, err := f.svc.Login(context.Background(), &pb.LoginReq{Username: "admin", Password: testPassword})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, direct.GetRetInfo().GetCode())
}

// 用户不存在时也对固定的占位哈希做一次 bcrypt 校验，和密码错误耗时一致，不能借响应时间判断用户名是否存在。
func TestLoginUnknownUserStillRunsOneBcryptCheck(t *testing.T) {
	f := newLoginFixture(t)
	original := verifyPassword
	t.Cleanup(func() { verifyPassword = original })
	var hashes []string
	verifyPassword = func(password, hash string) bool {
		hashes = append(hashes, hash)
		return original(password, hash)
	}
	rsp, _ := f.login(t, "nobody", testPassword, "203.0.113.7")
	assert.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg())
	require.Equal(t, []string{placeholderPasswordHash()}, hashes, "查不到用户也做了一次校验")
	assert.Regexp(t, `^\$2[aby]\$10\$`, hashes[0], "占位哈希与真实密码同一种 bcrypt、同一代价")
	assert.False(t, original(testPassword, hashes[0]), "占位哈希不对应任何能登录的密码")

	hashes = nil
	rsp, _ = f.login(t, "admin", "wrong", "203.0.113.8")
	assert.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg())
	require.Len(t, hashes, 1, "密码错误同样只做一次校验")
	assert.NotEqual(t, placeholderPasswordHash(), hashes[0])
}

func TestLoginUnknownUserAndWrongPasswordGiveSameMessage(t *testing.T) {
	f := newLoginFixture(t)
	unknown, _ := f.login(t, "nobody", testPassword, "203.0.113.7")
	wrong, carrier := f.login(t, "admin", "wrong", "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_NO_AUTH, unknown.GetRetInfo().GetCode())
	assert.Equal(t, unknown.GetRetInfo().GetMsg(), wrong.GetRetInfo().GetMsg())
	assert.Nil(t, carrier.Issued)
}

// 锁定规则与改动前一致：按“用户名 + IP”计数，第 5 次失败仍提示密码错误，第 6 次起提示锁定；
// 锁定期间密码正确也拒绝、不计数、不延长；锁定 15 分钟后计数归零。
func TestLoginLocksAfterFiveFailuresForFifteenMinutes(t *testing.T) {
	f := newLoginFixture(t)
	for attempt := 1; attempt <= 5; attempt++ {
		rsp, _ := f.login(t, "admin", "wrong", "203.0.113.7")
		require.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg(), "第 %d 次", attempt)
		f.now = f.now.Add(time.Minute)
	}
	locked, carrier := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_NO_AUTH, locked.GetRetInfo().GetCode())
	assert.Equal(t, "账户已被锁定，请稍后再试", locked.GetRetInfo().GetMsg())
	assert.Nil(t, carrier.Issued)

	// 另一个 IP 不受影响。
	other, _ := f.login(t, "admin", testPassword, "198.51.100.1")
	assert.Equal(t, pb.ErrorCode_SUCCESS, other.GetRetInfo().GetCode())

	// 锁定从第 5 次失败算起 15 分钟；期间的尝试不延长锁定。
	f.now = f.now.Add(13 * time.Minute)
	stillLocked, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, "账户已被锁定，请稍后再试", stillLocked.GetRetInfo().GetMsg())
	f.now = f.now.Add(time.Minute)
	unlocked, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_SUCCESS, unlocked.GetRetInfo().GetCode())
}

func TestLoginUnknownUserCountsTowardsLock(t *testing.T) {
	f := newLoginFixture(t)
	for range 5 {
		f.login(t, "ghost", "wrong", "203.0.113.7")
	}
	rsp, _ := f.login(t, "ghost", "wrong", "203.0.113.7")
	assert.Equal(t, "账户已被锁定，请稍后再试", rsp.GetRetInfo().GetMsg())
}

func TestLoginFailureCountResetsThirtyMinutesAfterLastFailure(t *testing.T) {
	f := newLoginFixture(t)
	for range 4 {
		f.login(t, "admin", "wrong", "203.0.113.7")
	}
	// 最后一次失败满 30 分钟，计数清零：再错 4 次也不锁。
	f.now = f.now.Add(30 * time.Minute)
	for range 4 {
		rsp, _ := f.login(t, "admin", "wrong", "203.0.113.7")
		require.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg())
	}
	rsp, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestLoginSuccessClearsFailureCount(t *testing.T) {
	f := newLoginFixture(t)
	for range 4 {
		f.login(t, "admin", "wrong", "203.0.113.7")
	}
	ok, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	require.Equal(t, pb.ErrorCode_SUCCESS, ok.GetRetInfo().GetCode())
	for range 4 {
		rsp, _ := f.login(t, "admin", "wrong", "203.0.113.7")
		require.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg())
	}
	rsp, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestLoginFailuresSweepExpiredEntries(t *testing.T) {
	f := newLoginFixture(t)
	f.login(t, "admin", "wrong", "203.0.113.7")
	f.now = f.now.Add(31 * time.Minute)
	f.login(t, "admin", "wrong", "198.51.100.1")
	assert.Len(t, f.svc.failures.entries, 1)
}
