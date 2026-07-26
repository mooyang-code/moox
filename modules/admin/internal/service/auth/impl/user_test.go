package impl

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	authconfig "github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

func TestGetUserInfoRequiresContextMetadata(t *testing.T) {
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
	rsp, err := svc.GetUserInfo(ctx, &pb.GetUserInfoReq{})
	if err != nil {
		t.Fatalf("GetUserInfo() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_AUTH {
		t.Fatalf("GetUserInfo ret = %+v, want no auth", rsp.GetRetInfo())
	}
}

func authCtx(userID string, role int32) context.Context {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte(userID))
	trpc.SetMetaData(ctx, model.CtxUserRole, []byte("2"))
	return ctx
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
