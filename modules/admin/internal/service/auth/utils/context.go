package utils

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"trpc.group/trpc-go/trpc-go"
)

// GetUserInfoFromCtx 从 trpc 上下文元数据中获取已登录用户。系统只有一个超级用户，没有角色。
func GetUserInfoFromCtx(ctx context.Context) (userID string, username string, err error) {
	userID = string(trpc.GetMetaData(ctx, model.CtxUserID))
	if userID == "" {
		return "", "", fmt.Errorf("用户ID未在上下文中找到")
	}
	username = string(trpc.GetMetaData(ctx, model.CtxUsername))
	return userID, username, nil
}
