package dao

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUserTestDB(t *testing.T, users ...*model.User) *UserDAO {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// 共享内存库在最后一个连接关闭时才销毁；不关的话 -count=N 的下一轮会读到上一轮的数据。
	if sqlDB, err := db.DB(); err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	for _, user := range users {
		require.NoError(t, db.Create(user).Error)
	}
	return NewUserDAO(db)
}

func TestUserDAO_GetUserByUsernameAndID(t *testing.T) {
	d := setupUserTestDB(t, &model.User{UserID: "user-1", Username: "alice", PasswordHash: "hash"})

	got, err := d.GetUserByUsername(context.Background(), "alice")
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.UserID)
	got, err = d.GetUserByID(context.Background(), "user-1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.Username)
}

func TestUserDAO_GetUserByID_NotFound_ShouldError(t *testing.T) {
	d := setupUserTestDB(t)
	_, err := d.GetUserByID(context.Background(), "missing")
	require.Error(t, err)
}

func TestUserDAO_UpdateUserLoginInfo_ValidUser_ShouldUpdate(t *testing.T) {
	d := setupUserTestDB(t, &model.User{UserID: "user-3", Username: "carol", PasswordHash: "h"})
	require.NoError(t, d.UpdateUserLoginInfo(context.Background(), "user-3", "10.0.0.1"))

	got, err := d.GetUserByID(context.Background(), "user-3")
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", got.LastLoginIP)
	assert.NotNil(t, got.LastLoginAt)
}
