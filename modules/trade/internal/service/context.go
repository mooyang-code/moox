package service

import "context"

// operatorKey 在 context 中携带操作人标识（通常为登录 user_id），用于审计。
type operatorCtxKey struct{}

// WithOperator 把操作人写入 context。
func WithOperator(ctx context.Context, operator string) context.Context {
	if operator == "" {
		return ctx
	}
	return context.WithValue(ctx, operatorCtxKey{}, operator)
}

// operatorFromContext 取操作人；无则返回空串。
func operatorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(operatorCtxKey{}).(string); ok {
		return v
	}
	return ""
}
