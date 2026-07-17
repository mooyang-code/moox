package impl

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newAuthServiceForLoginTest(t *testing.T) (*AuthServiceImpl, string, string) {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)

	cache, err := dao.NewCacheDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cache.Close() })

	secretKey := "test-secret-for-login"
	password := "secret123"
	passwordHash, err := mooxsecurity.HashPassword(password)
	require.NoError(t, err)
	user := &model.User{
		UserID:       "user-login-1",
		Username:     "admin",
		PasswordHash: passwordHash,
		Role:         int32(pb.UserRole_USER_ROLE_ADMIN),
		Status:       int32(pb.UserStatus_USER_STATUS_ACTIVE),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, db.Create(user).Error)

	svc := &AuthServiceImpl{
		cfg: &authconfig.Config{
			JWT: authconfig.JWTConfig{SecretKey: secretKey, AccessExpired: 24 * time.Hour},
			Security: authconfig.SecurityConfig{
				SaltExpired:     10 * time.Minute,
				MaxLoginAttempt: 5,
				LockDuration:    time.Minute,
				SessionTTL:      24 * time.Hour,
				RawTicketTTL:    time.Minute,
			},
		},
		userDAO: dao.NewUserDAO(db, cache),
	}
	return svc, password, secretKey
}

func encryptLoginPassword(t *testing.T, password, loginSalt string, timestamp int64) string {
	t.Helper()
	cipher, err := mooxsecurity.Encrypt(password, loginSalt+strconv.FormatInt(timestamp, 10))
	require.NoError(t, err)
	return cipher
}

func newTestSalt(t *testing.T) string {
	t.Helper()
	salt, err := mooxsecurity.NewSalt()
	require.NoError(t, err)
	return salt
}

func TestLogin_InvalidCredentialFormat_ShouldReturnInvalidParam(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	rsp, err := svc.Login(context.Background(), &pb.LoginReq{Username: "", PasswordHash: "short"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestLogin_InvalidSalt_ShouldReturnInvalidParam(t *testing.T) {
	svc, password, _ := newAuthServiceForLoginTest(t)
	loginSalt := newTestSalt(t)
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
	assert.Len(t, loginRsp.GetSessionId(), 32)
	assert.Len(t, loginRsp.GetRequestSigningKey(), 64)
	assert.WithinDuration(t, time.Now().Add(24*time.Hour), time.Unix(loginRsp.GetExpiresAt(), 0), 2*time.Second)

	claims, err := mooxsecurity.ParseToken(loginRsp.GetAccessToken(), secretKey)
	require.NoError(t, err)
	assert.Equal(t, "user-login-1", claims["user_id"])
	assert.Equal(t, loginRsp.GetSessionId(), claims["sid"])
	assert.Equal(t, float64(loginRsp.GetExpiresAt()), claims["exp"])
	session, err := svc.userDAO.GetSigningSession(ctx, loginRsp.GetSessionId())
	require.NoError(t, err)
	assert.NotEqual(t, loginRsp.GetRequestSigningKey(), session.EncryptedSecret)
}

func TestLoginSaltCannotBeReusedAfterFailedPassword(t *testing.T) {
	svc, _, _ := newAuthServiceForLoginTest(t)
	ctx := context.Background()
	salt, err := svc.GetLoginSalt(ctx, &pb.GetLoginSaltReq{Username: "admin"})
	require.NoError(t, err)
	req := &pb.LoginReq{Username: "admin", Salt: salt.GetSalt(), Timestamp: salt.GetTimestamp()}
	req.PasswordHash = encryptLoginPassword(t, "wrong-password", req.Salt, req.Timestamp)
	first, err := svc.Login(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, first.GetRetInfo().GetCode())
	second, err := svc.Login(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, second.GetRetInfo().GetCode())
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
