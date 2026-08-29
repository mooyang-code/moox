package marketdata

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		err  error
		kind ErrorKind
	}{
		{err: fmt.Errorf("provider: %w", ErrTimeout), kind: ErrorKindTimeout},
		{err: ErrRateLimited, kind: ErrorKindRateLimited},
		{err: ErrHTTPStatus, kind: ErrorKindHTTPStatus},
		{err: ErrProtocol, kind: ErrorKindProtocol},
		{err: ErrNoClosedBar, kind: ErrorKindNoClosedBar},
		{err: ErrUnsupportedSymbol, kind: ErrorKindUnsupportedSymbol},
		{err: ErrUnsupportedFrequency, kind: ErrorKindUnsupportedFrequency},
		{err: ErrInvalidRequest, kind: ErrorKindInvalidRequest},
		{err: context.Canceled, kind: ErrorKindContextCanceled},
		{err: context.DeadlineExceeded, kind: ErrorKindDeadlineExceeded},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.kind, ClassifyError(tt.err))
	}
}

func TestCanFallbackOnlyForFetchFailuresWithLiveContext(t *testing.T) {
	ctx := context.Background()
	for _, err := range []error{ErrTimeout, ErrRateLimited, ErrHTTPStatus, ErrProtocol, ErrNoClosedBar} {
		assert.True(t, CanFallback(ctx, err), "error=%v", err)
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, ErrInvalidRequest, ErrUnsupportedSymbol, ErrUnsupportedFrequency} {
		assert.False(t, CanFallback(ctx, err), "error=%v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.False(t, CanFallback(canceled, ErrTimeout))
}
