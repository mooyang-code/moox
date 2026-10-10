package console

import "context"

// SpaceAuthorizer 是 Admin 侧的空间授权策略。请求头里的空间不可信，实现必须核对
// 用户是否有权在该空间执行请求；mutates 为 true 的写操作要求空间 owner/admin。
type SpaceAuthorizer interface {
	AuthorizeSpaceRequest(ctx context.Context, userID, spaceID string, mutates bool, globalRole int32) error
}
