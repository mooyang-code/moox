package impl

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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

func TestRegister_EmptyUsername_ShouldReturnInvalidParam(t *testing.T) {
	svc := newAuthServiceForRegisterTest(t)
	rsp, err := svc.Register(context.Background(), &pb.RegisterReq{Password: "pwd"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestRegister_EmptyPassword_ShouldReturnInvalidParam(t *testing.T) {
	svc := newAuthServiceForRegisterTest(t)
	rsp, err := svc.Register(context.Background(), &pb.RegisterReq{Username: "new-user"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestRegister_ValidUser_ShouldCreateUser(t *testing.T) {
	svc := newAuthServiceForRegisterTest(t)
	rsp, err := svc.Register(context.Background(), &pb.RegisterReq{
		Username: "new-user",
		Password: "secret123",
		Email:    "new@example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetUserId())
	assert.Equal(t, "new-user", rsp.GetUserInfo().GetUsername())
}

func TestRegister_DuplicateUsername_ShouldReturnInvalidParam(t *testing.T) {
	svc := newAuthServiceForRegisterTest(t)
	_, err := svc.Register(context.Background(), &pb.RegisterReq{Username: "dup", Password: "pwd1"})
	require.NoError(t, err)
	rsp, err := svc.Register(context.Background(), &pb.RegisterReq{Username: "dup", Password: "pwd2"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestUpdateUserInfo_ValidUser_ShouldUpdateNickname(t *testing.T) {
	svc, user := newAuthServiceForPasswordTest(t)
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
	svc, user := newAuthServiceForPasswordTest(t)
	ctx := authCtx(user.UserID, user.Role)

	rsp, err := svc.GetUserInfo(ctx, &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, user.UserID, rsp.GetUserInfo().GetUserId())
}
