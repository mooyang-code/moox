// Package jobcontext carries immutable Collector JobItem identity through execution.
package jobcontext

import (
	"context"
	"strings"
)

type jobItemIDKey struct{}

func WithJobItemID(ctx context.Context, jobItemID string) context.Context {
	return context.WithValue(ctx, jobItemIDKey{}, strings.TrimSpace(jobItemID))
}

func JobItemID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	jobItemID, _ := ctx.Value(jobItemIDKey{}).(string)
	return jobItemID
}
