package impl

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	admincrypto "github.com/mooyang-code/moox/modules/admin/internal/common/crypto"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

func newAuthServiceForPasswordTest(t *testing.T) (*AuthServiceImpl, *model.User) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)

	cache, err := dao.NewCacheDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	password := "old-pass"
	userSalt := admincrypto.GenerateSalt()
	user := &model.User{
		UserID:       "user-pwd-1",
		Username:     "alice",
		PasswordHash: admincrypto.HashPassword(password, userSalt),
		Salt:         userSalt,
		Role:         int32(pb.UserRole_USER_ROLE_ADMIN),
		Status:       int32(pb.UserStatus_USER_STATUS_ACTIVE),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, db.Create(user).Error)

	svc := &AuthServiceImpl{
		cfg: &authconfig.Config{
			Security: authconfig.SecurityConfig{SaltExpired: 10 * time.Minute},
		},
		userDAO: dao.NewUserDAO(db, cache),
	}
	return svc, user
}

func authCtx(userID string, role int32) context.Context {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte(userID))
	trpc.SetMetaData(ctx, model.CtxUserRole, []byte("1"))
	return ctx
}

func TestGetChangePasswordSalt_NoUserInContext_ShouldReturnNoAuth(t *testing.T) {
	svc, _ := newAuthServiceForPasswordTest(t)
	rsp, err := svc.GetChangePasswordSalt(context.Background(), &pb.GetChangePasswordSaltReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, rsp.GetRetInfo().GetCode())
}

func TestGetChangePasswordSalt_ValidUser_ShouldReturnSalt(t *testing.T) {
	svc, user := newAuthServiceForPasswordTest(t)
	ctx := authCtx(user.UserID, user.Role)

	rsp, err := svc.GetChangePasswordSalt(ctx, &pb.GetChangePasswordSaltReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetSalt())
}

func TestChangePassword_InvalidSalt_ShouldReturnInvalidParam(t *testing.T) {
	svc, user := newAuthServiceForPasswordTest(t)
	ctx := authCtx(user.UserID, user.Role)

	rsp, err := svc.ChangePassword(ctx, &pb.ChangePasswordReq{
		Salt:            "bad-salt",
		Timestamp:       time.Now().Unix(),
		OldPasswordHash: "abc",
		NewPasswordHash: "def",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
