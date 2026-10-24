package utils

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
)

func TestUtils_GetUserInfoFromCtx_ValidMetadata_ShouldReturnUserInfo(t *testing.T) {
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("user-1"))
	trpc.SetMetaData(ctx, model.CtxUsername, []byte("alice"))

	userID, username, err := GetUserInfoFromCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, "user-1", userID)
	assert.Equal(t, "alice", username)
}

func TestUtils_GetUserInfoFromCtx_MissingUserID_ShouldReturnError(t *testing.T) {
	_, _, err := GetUserInfoFromCtx(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "用户ID未在上下文中找到")
}

func TestUtils_BuildSafeUserInfo_ScriptPayload_ShouldEscapeHTML(t *testing.T) {
	now := time.Now()
	user := &model.User{
		UserID:      "user-1",
		Username:    "<script>alert(1)</script>",
		CreatedAt:   now,
		LastLoginIP: "127.0.0.1",
	}

	info := BuildSafeUserInfo(user)
	require.NotNil(t, info)
	assert.Equal(t, "user-1", info.UserId)
	assert.NotContains(t, info.Username, "<script>")
}

func TestUtils_ValidateStringFormat_ValidValue_ShouldReturnNil(t *testing.T) {
	err := ValidateStringFormat("abc123", "用户名")
	assert.NoError(t, err)
}

func TestUtils_ValidateStringFormat_TooShort_ShouldReturnError(t *testing.T) {
	err := ValidateStringFormat("", "用户名")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "长度必须在1-20个字符之间")
}

func TestUtils_ValidateStringFormat_TooLong_ShouldReturnError(t *testing.T) {
	err := ValidateStringFormat("abcdefghijklmnopqrstuvwxyz", "用户名")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "长度必须在1-20个字符之间")
}

func TestUtils_ValidateStringFormat_InvalidChars_ShouldReturnError(t *testing.T) {
	err := ValidateStringFormat("user_name", "用户名")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "只能包含大小写字母和数字")
}
