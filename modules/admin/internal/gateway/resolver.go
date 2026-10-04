package gateway

import (
	"context"
	"fmt"
)

// AdminServiceDetailProvider resolves browser control-plane targets for the
// Admin process's configured node.
type AdminServiceDetailProvider interface {
	ResolveAdminServiceDetail(ctx context.Context, adminNodeID, serviceID string) (ServiceDetail, bool)
}

// TradeSpaceAuthorizer is the Admin-side policy gate for the browser Trade
// console.  The request header is untrusted; implementations must verify the
// user is allowed to act in the requested Space and method.
type TradeSpaceAuthorizer interface {
	AuthorizeTradeRequest(ctx context.Context, userID, spaceID, method string, globalRole int32) error
}

func resolveAdminServiceDetail(ctx context.Context, provider AdminServiceDetailProvider, adminNodeID, serviceID string) (ServiceDetail, error) {
	if provider != nil {
		if detail, ok := provider.ResolveAdminServiceDetail(ctx, adminNodeID, serviceID); ok {
			return detail, nil
		}
	}
	return ServiceDetail{}, fmt.Errorf("服务 '%s' 未在节点 '%s' 找到 active 部署记录", serviceID, adminNodeID)
}
