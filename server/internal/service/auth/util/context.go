package util

import (
	"context"
	"fmt"
	"strconv"

	"github.com/mooyang-code/moox/server/internal/service/auth/model"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

// GetUserInfoFromHeader 从HTTP header中获取用户信息
func GetUserInfoFromHeader(ctx context.Context) (userID string, username string, role int32, err error) {
	header := thttp.Head(ctx)
	if header == nil {
		return "", "", 0, fmt.Errorf("获取HTTP头失败")
	}

	// 获取用户ID
	userID = header.Request.Header.Get(model.HeaderUserID)
	if userID == "" {
		return "", "", 0, fmt.Errorf("用户ID未在header中找到")
	}

	// 获取用户名
	username = header.Request.Header.Get(model.HeaderUsername)

	// 获取用户角色
	roleStr := header.Request.Header.Get(model.HeaderUserRole)
	if roleStr != "" {
		if roleInt, parseErr := strconv.ParseInt(roleStr, 10, 32); parseErr == nil {
			role = int32(roleInt)
		}
	}
	return userID, username, role, nil
}
