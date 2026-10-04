package spacecontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSpaceContext_WithSpaceID_ValidID_ShouldStoreValue(t *testing.T) {
	ctx := WithSpaceID(context.Background(), "crypto")
	got, ok := FromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, "crypto", got)
}

func TestSpaceContext_WithSpaceID_EmptyID_ShouldReturnOriginalContext(t *testing.T) {
	ctx := WithSpaceID(context.Background(), "")
	got, ok := FromContext(ctx)
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestSpaceContext_FromContext_MissingValue_ShouldReturnFalse(t *testing.T) {
	got, ok := FromContext(context.Background())
	assert.False(t, ok)
	assert.Empty(t, got)
}
