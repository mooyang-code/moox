package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
)

// ProviderRouter resolves one explicit SourceKey and delegates all row
// normalization/storage semantics to KlinePipeline. It deliberately does not
// silently combine fields from multiple sources.
type ProviderRouter struct {
	Registry *sources.ProviderRegistry
	Writer   KlineRowWriter
}

func (router *ProviderRouter) FetchAndWrite(ctx context.Context, request PipelineRequest) (PipelineResult, error) {
	if router == nil || router.Registry == nil || router.Writer == nil {
		return PipelineResult{}, fmt.Errorf("provider router is not initialized")
	}
	if request.SourceKey.ProviderID == "" || request.SourceKey.SourceID == "" {
		return PipelineResult{}, fmt.Errorf("source_key is required")
	}
	registration, ok := router.Registry.Lookup(request.SourceKey)
	if !ok {
		return PipelineResult{}, fmt.Errorf("source %s is not registered", request.SourceKey)
	}
	if registration.Klines == nil {
		return PipelineResult{}, fmt.Errorf("source %s does not implement kline", request.SourceKey)
	}
	return (&KlinePipeline{Fetcher: registration.Klines, Writer: router.Writer}).FetchAndWrite(ctx, request)
}

// FetchWithFallback tries complete sources in manifest order. A source either
// writes a complete row set or the next source is tried; no field-level merge
// is performed across providers.
func (router *ProviderRouter) FetchWithFallback(ctx context.Context, request PipelineRequest, candidates []marketdata.SourceKey) (PipelineResult, error) {
	if len(candidates) == 0 {
		return PipelineResult{}, fmt.Errorf("source candidates are required")
	}
	var lastErr error
	for _, candidate := range candidates {
		request.SourceKey = candidate
		result, err := router.FetchAndWrite(ctx, request)
		if err == nil && result.RowsWritten > 0 {
			return result, nil
		}
		if err != nil {
			lastErr = fmt.Errorf("source %s: %w", candidate, err)
			if !fallbackError(err) {
				return PipelineResult{}, lastErr
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all source candidates returned no rows")
	}
	return PipelineResult{}, lastErr
}

func fallbackError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, sentinel := range []error{marketdata.ErrTimeout, marketdata.ErrUnavailable, marketdata.ErrMalformed, marketdata.ErrRateLimited, marketdata.ErrOutOfRange} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	// Source adapters may not yet wrap their wire errors with a shared
	// sentinel. Keep fallback conservative for clearly local request errors.
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"required", "invalid", "unsupported", "does not support", "cannot be negative"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	return true
}
