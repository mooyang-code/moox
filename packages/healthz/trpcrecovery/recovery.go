// Package trpcrecovery registers MooX's sanitized tRPC panic boundary.
package trpcrecovery

import (
	"context"

	"trpc.group/trpc-go/trpc-filter/recovery"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/metrics"
)

func init() {
	filter.Register("recovery", recovery.ServerFilter(recovery.WithRecoveryHandler(handlePanic)), nil)
}

func handlePanic(ctx context.Context, _ interface{}) error {
	log.ErrorContextf(ctx, "tRPC handler panic recovered")
	metrics.IncrCounter("trpc.PanicNum", 1)
	return errs.NewFrameError(errs.RetServerSystemErr, "internal server error")
}
