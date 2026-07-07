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
	"gorm.io/gorm"
)

func TestGetUserInfoAcceptsRequestAccessTokenWhenContextMetadataMissing(t *testing.T) {
	ctx := context.Background()
	secretKey := "test-secret-for-get-user-info"
	user := &model.User{
		UserID:             "user-1",
		Username:           "admin",
		Nickname:           "Admin",
		Email:              "admin@example.local",
		Role:               int32(pb.UserRole_USER_ROLE_ADMIN),
		Status:             int32(pb.UserStatus_USER_STATUS_ACTIVE),
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
		LastPasswordChange: time.Now(),
	}
	svc := newAuthServiceForUserTest(t, secretKey, user)
	accessToken, err := admincrypto.GenerateAccessToken(user.UserID, user.Username, user.Role, secretKey, time.Hour)
	if err != nil {
		t.Fatalf("GenerateAccessToken() error = %v", err)
	}

	rsp, err := svc.GetUserInfo(ctx, &pb.GetUserInfoReq{AccessToken: accessToken})
	if err != nil {
		t.Fatalf("GetUserInfo() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("GetUserInfo ret = %+v, want success", rsp.GetRetInfo())
	}
	if rsp.GetUserInfo().GetUserId() != user.UserID {
		t.Fatalf("user_id = %q, want %q", rsp.GetUserInfo().GetUserId(), user.UserID)
	}
}

func newAuthServiceForUserTest(t *testing.T, secretKey string, users ...*model.User) *AuthServiceImpl {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(schema.AdminSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	for _, user := range users {
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	return &AuthServiceImpl{
		cfg:     &authconfig.Config{JWT: authconfig.JWTConfig{SecretKey: secretKey}},
		userDAO: dao.NewUserDAO(db, nil),
	}
}
