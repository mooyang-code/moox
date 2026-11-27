package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	appconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

const testPassword = "abc123"

func setupUserTestDB(t *testing.T, users ...*User) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	for _, user := range users {
		require.NoError(t, db.Create(user).Error)
	}
	return db
}

func TestNewService_Validation(t *testing.T) {
	sessions, err := NewSessionStore(time.Hour)
	require.NoError(t, err)
	_, err = NewService(&Config{}, database.NewManager(), sessions, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "数据库连接未初始化")

	mgr := database.NewManager()
	require.NoError(t, mgr.Initialize(&appconfig.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}))

	svc, err := NewService(&Config{}, mgr, sessions, nil)
	require.NoError(t, err)
	require.NotNil(t, svc)
	_, err = NewService(&Config{}, mgr, nil, nil)
	require.Error(t, err)
}

func TestUserDAO_CRUD(t *testing.T) {
	d := NewUserDAO(setupUserTestDB(t, &User{UserID: "user-1", Username: "alice", PasswordHash: "hash"}))
	got, err := d.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.UserID)
	got, err = d.GetUserByID(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Username)

	_, err = d.GetUserByID(context.Background(), "missing")
	require.Error(t, err)

	require.NoError(t, d.UpdateUserLoginInfo(context.Background(), "user-1", "10.0.0.1"))
	got, err = d.GetUserByID(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", got.LastLoginIP)
	assert.NotNil(t, got.LastLoginAt)
}

func TestSessionStore_Lifecycle(t *testing.T) {
	_, err := NewSessionStore(0)
	require.Error(t, err)

	store, err := NewSessionStore(24 * time.Hour)
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }

	created, err := store.Create("user-1", "admin")
	require.NoError(t, err)
	require.Len(t, created.ID, 64)
	require.Equal(t, now.Add(24*time.Hour), created.ExpiresAt)

	now = now.Add(23 * time.Hour)
	got, ok := store.Get(created.ID)
	require.True(t, ok)
	require.Equal(t, created.ExpiresAt, got.ExpiresAt, "使用不顺延过期时间")

	now = now.Add(time.Hour)
	_, ok = store.Get(created.ID)
	require.False(t, ok, "满 24 小时即失效")

	fresh, err := store.Create("user-1", "admin")
	require.NoError(t, err)
	require.Len(t, store.sessions, 1)
	store.Delete(fresh.ID)
	_, ok = store.Get(fresh.ID)
	require.False(t, ok)
	_, ok = store.Get("")
	require.False(t, ok)

	require.Nil(t, LoginFrom(context.Background()))
	login := &LoginContext{ClientIP: "203.0.113.9"}
	require.Same(t, login, LoginFrom(WithLogin(context.Background(), login)))
}

func TestContextAndSafeUserInfoHelpers(t *testing.T) {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, CtxUserID, []byte("user-1"))
	trpc.SetMetaData(ctx, CtxUsername, []byte("alice"))
	userID, username, err := GetUserInfoFromCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, "user-1", userID)
	assert.Equal(t, "alice", username)

	_, _, err = GetUserInfoFromCtx(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "用户ID未在上下文中找到")

	created := time.Date(2026, 11, 24, 8, 0, 0, 123_000_000, time.UTC)
	lastLogin := created.Add(time.Hour)
	info := BuildSafeUserInfo(&User{UserID: "user-1", Username: "<script>alert(1)</script>", CreatedAt: created, LastLoginAt: &lastLogin, LastLoginIP: "127.0.0.1"})
	assert.NotContains(t, info.GetUsername(), "<script>")
	assert.Equal(t, created.UnixMilli(), info.GetCreatedAt())
	assert.Equal(t, lastLogin.UnixMilli(), info.GetLastLoginAt())
	assert.Zero(t, BuildSafeUserInfo(&User{UserID: "u2", Username: "a", CreatedAt: created}).GetLastLoginAt())

	assert.NoError(t, ValidateStringFormat("abc123", "用户名"))
	assert.Error(t, ValidateStringFormat("", "用户名"))
	assert.Error(t, ValidateStringFormat("abcdefghijklmnopqrstuvwxyz", "用户名"))
	assert.Error(t, ValidateStringFormat("user_name", "用户名"))
}

type recordedLogin struct {
	userID, username, ip, ua string
}

type loginFixture struct {
	svc      *Service
	now      time.Time
	recorded []recordedLogin
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	passwordHash, err := mooxsecurity.HashPassword(testPassword)
	require.NoError(t, err)
	db := setupUserTestDB(t, &User{UserID: "user-login-1", Username: "admin", PasswordHash: passwordHash, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	sessions, err := NewSessionStore(24 * time.Hour)
	require.NoError(t, err)
	f := &loginFixture{now: time.Unix(1_800_000_000, 0)}
	f.svc = &Service{
		cfg:      &Config{Session: SessionConfig{TTL: 24 * time.Hour}, Security: SecurityConfig{MaxLoginAttempt: 5, LockDuration: 15 * time.Minute}},
		userDAO:  NewUserDAO(db),
		sessions: sessions,
		failures: newLoginFailures(),
		record: func(_ context.Context, userID, username, ip, ua string) error {
			f.recorded = append(f.recorded, recordedLogin{userID: userID, username: username, ip: ip, ua: ua})
			return nil
		},
		now: func() time.Time { return f.now },
	}
	return f
}

func (f *loginFixture) login(t *testing.T, username, password, ip string) (*pb.LoginRsp, *LoginContext) {
	t.Helper()
	carrier := &LoginContext{ClientIP: ip, UserAgent: "test-agent"}
	rsp, err := f.svc.Login(WithLogin(context.Background(), carrier), &pb.LoginReq{Username: username, Password: password})
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
	assert.Equal(t, carrier.Issued.ExpiresAt.UnixMilli(), rsp.GetExpiresAt())
	assert.Equal(t, "admin", rsp.GetUserInfo().GetUsername())
	stored, ok := f.svc.sessions.Get(carrier.Issued.ID)
	require.True(t, ok)
	assert.Equal(t, "admin", stored.Username)

	require.Equal(t, []recordedLogin{{userID: "user-login-1", username: "admin", ip: "203.0.113.7", ua: "test-agent"}}, f.recorded)
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

	direct, err := f.svc.Login(context.Background(), &pb.LoginReq{Username: "admin", Password: testPassword})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INNER_ERR, direct.GetRetInfo().GetCode())
}

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
	require.Equal(t, []string{placeholderPasswordHash()}, hashes)
	assert.Regexp(t, `^\$2[aby]\$10\$`, hashes[0])
	assert.False(t, original(testPassword, hashes[0]))

	hashes = nil
	rsp, _ = f.login(t, "admin", "wrong", "203.0.113.8")
	assert.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg())
	require.Len(t, hashes, 1)
	assert.NotEqual(t, placeholderPasswordHash(), hashes[0])
}

func TestLoginLocksAndClearsFailures(t *testing.T) {
	f := newLoginFixture(t)
	for attempt := 1; attempt <= 5; attempt++ {
		rsp, _ := f.login(t, "admin", "wrong", "203.0.113.7")
		require.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg(), "attempt %d", attempt)
		f.now = f.now.Add(time.Minute)
	}
	locked, carrier := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_NO_AUTH, locked.GetRetInfo().GetCode())
	assert.Equal(t, "账户已被锁定，请稍后再试", locked.GetRetInfo().GetMsg())
	assert.Nil(t, carrier.Issued)

	other, _ := f.login(t, "admin", testPassword, "198.51.100.1")
	assert.Equal(t, pb.ErrorCode_SUCCESS, other.GetRetInfo().GetCode())

	f.now = f.now.Add(13 * time.Minute)
	stillLocked, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, "账户已被锁定，请稍后再试", stillLocked.GetRetInfo().GetMsg())
	f.now = f.now.Add(time.Minute)
	unlocked, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_SUCCESS, unlocked.GetRetInfo().GetCode())
}

func TestLoginFailureWindowAndSweep(t *testing.T) {
	f := newLoginFixture(t)
	for range 4 {
		f.login(t, "admin", "wrong", "203.0.113.7")
	}
	f.now = f.now.Add(30 * time.Minute)
	for range 4 {
		rsp, _ := f.login(t, "admin", "wrong", "203.0.113.7")
		require.Equal(t, wrongCredentials, rsp.GetRetInfo().GetMsg())
	}
	rsp, _ := f.login(t, "admin", testPassword, "203.0.113.7")
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	f.login(t, "admin", "wrong", "203.0.113.7")
	f.now = f.now.Add(31 * time.Minute)
	f.login(t, "admin", "wrong", "198.51.100.1")
	assert.Len(t, f.svc.failures.entries, 1)
}

func TestLogoutAndGetUserInfo(t *testing.T) {
	f := newLoginFixture(t)
	current, err := f.svc.sessions.Create("user-login-1", "admin")
	require.NoError(t, err)
	other, err := f.svc.sessions.Create("user-login-1", "admin")
	require.NoError(t, err)

	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, CtxUserID, []byte("user-login-1"))
	trpc.SetMetaData(ctx, CtxSessionID, []byte(current.ID))

	info, err := f.svc.GetUserInfo(ctx, &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, info.GetRetInfo().GetCode())
	assert.Equal(t, "user-login-1", info.GetUserInfo().GetUserId())

	noAuth, err := f.svc.GetUserInfo(context.Background(), &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, noAuth.GetRetInfo().GetCode())

	rsp, err := f.svc.Logout(ctx, &pb.LogoutReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	_, ok := f.svc.sessions.Get(current.ID)
	assert.False(t, ok)
	_, ok = f.svc.sessions.Get(other.ID)
	assert.True(t, ok)

	noSid, err := f.svc.Logout(trpc.BackgroundContext(), &pb.LogoutReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, noSid.GetRetInfo().GetCode())
}
