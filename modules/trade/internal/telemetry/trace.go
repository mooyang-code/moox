package telemetry

import (
	"context"

	trpc "trpc.group/trpc-go/trpc-go"
)

type traceKey struct{}
type Trace struct{ TraceID, RequestID string }

func WithTrace(ctx context.Context, trace Trace) context.Context {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	return context.WithValue(ctx, traceKey{}, trace)
}
func TraceFromContext(ctx context.Context) Trace {
	if ctx == nil {
		return Trace{}
	}
	trace, _ := ctx.Value(traceKey{}).(Trace)
	return trace
}
