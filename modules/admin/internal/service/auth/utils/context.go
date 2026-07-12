package utils

import (
	"context"
	"fmt"
	"strconv"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"trpc.group/trpc-go/trpc-go"
)

// GetUserInfoFromCtx 从 trpc 上下文元数据中获取用户信息。
func GetUserInfoFromCtx(ctx context.Context) (userID string, username string, role int32, err error) {
	userID = string(trpc.GetMetaData(ctx, model.CtxUserID))
	if userID == "" {
		return "", "", 0, fmt.Errorf("用户ID未在上下文中找到")
	}
	username = string(trpc.GetMetaData(ctx, model.CtxUsername))
	roleBytes := trpc.GetMetaData(ctx, model.CtxUserRole)
	if roleStr := string(roleBytes); roleStr != "" {
		if roleInt, parseErr := strconv.ParseInt(roleStr, 10, 32); parseErr == nil {
			role = int32(roleInt)
		}
	}
	return userID, username, role, nil
}
