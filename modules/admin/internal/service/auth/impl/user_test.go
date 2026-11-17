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
	user := &model.User{
		UserID:    "user-1",
		Username:  "admin",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	svc := newAuthServiceForUserTest(t, user)
	rsp, err := svc.GetUserInfo(context.Background(), &pb.GetUserInfoReq{})
	if err != nil {
		t.Fatalf("GetUserInfo() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_AUTH {
		t.Fatalf("GetUserInfo ret = %+v, want no auth", rsp.GetRetInfo())
	}
}

func authCtx(userID string) context.Context {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte(userID))
	return ctx
}

func newAuthServiceForUserTest(t *testing.T, users ...*model.User) *AuthServiceImpl {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// 共享内存库在最后一个连接关闭时才销毁；不关的话 -count=N 的下一轮会读到上一轮的数据。
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
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
		cfg:     &authconfig.Config{},
		userDAO: dao.NewUserDAO(db),
	}
}
