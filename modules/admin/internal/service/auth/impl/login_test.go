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
)

func newAuthServiceForLoginTest(t *testing.T) (*AuthServiceImpl, string, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)

	cache, err := dao.NewCacheDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	secretKey := "test-secret-for-login"
	password := "secret123"
	userSalt := admincrypto.GenerateSalt()
	passwordHash := admincrypto.HashPassword(password, userSalt)
	user := &model.User{
		UserID:       "user-login-1",
		Username:     "admin",
		PasswordHash: passwordHash,
		Salt:         userSalt,
		Role:         int32(pb.UserRole_USER_ROLE_ADMIN),
		Status:       int32(pb.UserStatus_USER_STATUS_ACTIVE),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, db.Create(user).Error)

	svc := &AuthServiceImpl{
		cfg: &authconfig.Config{
			JWT: authconfig.JWTConfig{SecretKey: secretKey, AccessExpired: time.Hour},
			Security: authconfig.SecurityConfig{
				SaltExpired:     10 * time.Minute,
				MaxLoginAttempt: 5,
				LockDuration:    time.Minute,
			},
		},
		userDAO: dao.NewUserDAO(db, cache),
	}
	return svc, password, secretKey
}

func encryptLoginPassword(t *testing.T, password, loginSalt string, timestamp int64) string {
	t.Helper()
	key := admincrypto.DeriveEncryptionKey(loginSalt, timestamp)
	cipher, err := admincrypto.AESEncrypt(password, string(key))
	require.NoError(t, err)
	return cipher
}

func TestLogin_InvalidCredentialFormat_ShouldReturnInvalidParam(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	rsp, err := svc.Login(context.Background(), &pb.LoginReq{Username: "", PasswordHash: "short"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestLogin_InvalidSalt_ShouldReturnInvalidParam(t *testing.T) {
	svc, password, _ := newAuthServiceForLoginTest(t)
	loginSalt := admincrypto.GenerateSalt()
	timestamp := time.Now().Unix()
	rsp, err := svc.Login(context.Background(), &pb.LoginReq{
		Username:     "admin",
		PasswordHash: encryptLoginPassword(t, password, loginSalt, timestamp),
		Salt:         loginSalt,
		Timestamp:    timestamp,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestLogin_ValidCredentials_ShouldReturnAccessToken(t *testing.T) {
	svc, password, secretKey := newAuthServiceForLoginTest(t)
	ctx := context.Background()

	saltRsp, err := svc.GetLoginSalt(ctx, &pb.GetLoginSaltReq{Username: "admin"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, saltRsp.GetRetInfo().GetCode())

	loginRsp, err := svc.Login(ctx, &pb.LoginReq{
		Username:     "admin",
		PasswordHash: encryptLoginPassword(t, password, saltRsp.GetSalt(), saltRsp.GetTimestamp()),
		Salt:         saltRsp.GetSalt(),
		Timestamp:    saltRsp.GetTimestamp(),
		ClientIp:     "127.0.0.1",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, loginRsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, loginRsp.GetAccessToken())

	claims, err := admincrypto.ValidateAccessToken(loginRsp.GetAccessToken(), secretKey)
	require.NoError(t, err)
	assert.Equal(t, "user-login-1", claims.UserID)
}

func TestGetLoginSalt_ExistingValidSalt_ShouldReuse(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := context.Background()

	first, err := svc.GetLoginSalt(ctx, &pb.GetLoginSaltReq{Username: "admin"})
	require.NoError(t, err)
	second, err := svc.GetLoginSalt(ctx, &pb.GetLoginSaltReq{Username: "admin"})
	require.NoError(t, err)
	assert.Equal(t, first.GetSalt(), second.GetSalt())
}
