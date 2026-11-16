package impl

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
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
		userDAO: dao.NewUserDAO(db),
	}
}

func TestGetUserInfo_NoAuth_ShouldReturnNoAuth(t *testing.T) {
	svc := newAuthServiceForRegisterTest(t)
	rsp, err := svc.GetUserInfo(context.Background(), &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NO_AUTH, rsp.GetRetInfo().GetCode())
}

func TestGetUserInfo_LoggedInUser_ShouldSucceed(t *testing.T) {
	user := &model.User{UserID: "user-info-1", Username: "alice", PasswordHash: "unused"}
	svc := newAuthServiceForUserTest(t, user)
	ctx := authCtx(user.UserID)

	rsp, err := svc.GetUserInfo(ctx, &pb.GetUserInfoReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, user.UserID, rsp.GetUserInfo().GetUserId())
}
