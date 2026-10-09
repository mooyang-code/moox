package console

import "context"

// TradeSpaceAuthorizer is the Admin-side policy gate for the browser Trade
// console.  The request header is untrusted; implementations must verify the
// user is allowed to act in the requested Space and method.
type TradeSpaceAuthorizer interface {
	AuthorizeTradeRequest(ctx context.Context, userID, spaceID, method string, globalRole int32) error
}
