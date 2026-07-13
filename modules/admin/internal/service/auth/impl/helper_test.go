package impl

import (
	"context"
	"github.com/glebarez/sqlite"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

func newAuthServiceForHelperTest(t *testing.T) *AuthServiceImpl {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	cache, err := dao.NewCacheDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })
	return &AuthServiceImpl{
		cfg: &authconfig.Config{
			Security: authconfig.SecurityConfig{
				MaxLoginAttempt: 2,
				LockDuration:    time.Minute,
				SaltExpired:     5 * time.Minute,
			},
		},
		userDAO: dao.NewUserDAO(db, cache),
	}
}

func TestExtractClientIPFromContext_XClientIP_ShouldPreferHeader(t *testing.T) {
	svc := newAuthServiceForHelperTest(t)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Client-Ip", "10.1.2.3")
	ctx := thttp.WithHeader(context.Background(), &thttp.Header{Request: req})
	assert.Equal(t, "10.1.2.3", svc.extractClientIPFromContext(ctx, "fallback"))
}

func TestExtractClientIPFromContext_Fallback_ShouldUseRequestIP(t *testing.T) {
	svc := newAuthServiceForHelperTest(t)
	assert.Equal(t, "198.18.0.1", svc.extractClientIPFromContext(context.Background(), "198.18.0.1"))
}

func TestIsUserLocked_ExceedAttempts_ShouldReturnTrue(t *testing.T) {
	svc := newAuthServiceForHelperTest(t)
	ctx := context.Background()
	svc.recordLoginAttempt(ctx, "alice", "1.1.1.1", false)
	svc.recordLoginAttempt(ctx, "alice", "1.1.1.1", false)
	assert.True(t, svc.isUserLocked(ctx, "alice", "1.1.1.1"))
}

func TestValidateLoginSalt_ValidSalt_ShouldReturnTrue(t *testing.T) {
	svc := newAuthServiceForHelperTest(t)
	ctx := context.Background()
	salt := model.LoginSalt{
		Username:  "bob",
		Salt:      "salt-1",
		Timestamp: 123,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, svc.userDAO.SetLoginSalt(ctx, "bob", salt))
	assert.True(t, svc.validateLoginSalt(ctx, "bob", "salt-1", 123))
}

func TestRecordLoginHistory_ShouldNotPanic(t *testing.T) {
	svc := newAuthServiceForHelperTest(t)
	user := &model.User{UserID: "u1", Username: "bob"}
	require.NotPanics(t, func() {
		svc.recordLoginHistory(context.Background(), user, &pb.LoginReq{ClientIp: "1.1.1.1"}, model.LoginResultSuccess, "")
	})
}

func TestLogUserAction_ShouldNotPanic(t *testing.T) {
	svc := newAuthServiceForHelperTest(t)
	require.NotPanics(t, func() {
		svc.logUserAction(context.Background(), "u1", "login", "", "ok", "1.1.1.1", "ua", "success")
	})
}

func newAuthServiceForRegisterTest(t *testing.T) *AuthServiceImpl {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	return &AuthServiceImpl{
		cfg:     &authconfig.Config{},
		userDAO: dao.NewUserDAO(db, nil),
	}
}

func TestUpdateUserInfo_ValidUser_ShouldUpdateNickname(t *testing.T) {
	user := &model.User{UserID: "user-profile-1", Username: "alice", PasswordHash: "unused", Role: int32(pb.UserRole_USER_ROLE_ADMIN), Status: int32(pb.UserStatus_USER_STATUS_ACTIVE)}
	svc := newAuthServiceForUserTest(t, "secret", user)
	ctx := authCtx(user.UserID, user.Role)

	rsp, err := svc.UpdateUserInfo(ctx, &pb.UpdateUserInfoReq{Nick: "new-nick"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "new-nick", rsp.GetUserInfo().GetNickname())
}

func TestGetUserInfo_NoAuth_ShouldReturnNoAuth(t *testing.T) {
	svc := newAuthServiceForRegisterTest(t)
	rsp, err := svc.GetUserInfo(context.Background(), &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, rsp.GetRetInfo().GetCode())
}

func TestGetUserInfo_AdminQueryOtherUser_ShouldSucceed(t *testing.T) {
	user := &model.User{UserID: "user-info-1", Username: "alice", PasswordHash: "unused", Role: int32(pb.UserRole_USER_ROLE_ADMIN), Status: int32(pb.UserStatus_USER_STATUS_ACTIVE)}
	svc := newAuthServiceForUserTest(t, "secret", user)
	ctx := authCtx(user.UserID, user.Role)

	rsp, err := svc.GetUserInfo(ctx, &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, user.UserID, rsp.GetUserInfo().GetUserId())
}
