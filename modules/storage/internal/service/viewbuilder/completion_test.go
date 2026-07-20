//go:build legacy_storage

package builder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveCompletionWaitsForEveryItemAndReturnsFirstError(t *testing.T) {
	completion := newDeriveCompletion(2)
	firstErr := errors.New("duckdb unavailable")
	completion.complete(firstErr)

	result := make(chan error, 1)
	go func() { result <- completion.wait(context.Background()) }()

	select {
	case <-result:
		t.Fatal("wait returned before every item completed")
	case <-time.After(20 * time.Millisecond):
	}

	completion.complete(errors.New("later error"))
	require.ErrorIs(t, <-result, firstErr)
}

func TestDeriveCompletionWithNoItemsCompletesImmediately(t *testing.T) {
	completion := newDeriveCompletion(0)
	require.NoError(t, completion.wait(context.Background()))
	completion.complete(errors.New("late completion must be ignored"))
}

func TestDeriveCompletionHonorsCancellationAndLateCompletion(t *testing.T) {
	completion := newDeriveCompletion(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, completion.wait(ctx), context.Canceled)
	completion.complete(nil)
	require.NoError(t, completion.wait(context.Background()))
}
