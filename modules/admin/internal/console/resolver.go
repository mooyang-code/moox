package console

import "context"

// SpaceAuthorizer 是 Admin 侧的空间授权策略。请求头里的空间不可信，实现必须核对
// 空间存在且可用。系统只有一个超级用户，不区分成员与角色。
type SpaceAuthorizer interface {
	AuthorizeSpaceRequest(ctx context.Context, spaceID string) error
}
