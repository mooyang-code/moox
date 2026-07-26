package spacecontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSpaceContext_WithSpaceID_ValidID_ShouldStoreValue(t *testing.T) {
	ctx := WithSpaceID(context.Background(), "crypto")
	got, ok := ctx.Value(ctxKey{}).(string)
	assert.True(t, ok)
	assert.Equal(t, "crypto", got)
}

func TestSpaceContext_WithSpaceID_EmptyID_ShouldReturnOriginalContext(t *testing.T) {
	base := context.Background()
	ctx := WithSpaceID(base, "")
	got, ok := ctx.Value(ctxKey{}).(string)
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestSpaceContext_InjectFromHeaders_ValidHeader_ShouldInjectSpaceID(t *testing.T) {
	ctx := InjectFromHeaders(context.Background(), map[string]string{"space_id": "crypto"})
	got, ok := ctx.Value(ctxKey{}).(string)
	assert.True(t, ok)
	assert.Equal(t, "crypto", got)
}

func TestSpaceContext_InjectFromHeaders_NilHeaders_ShouldReturnOriginalContext(t *testing.T) {
	base := context.Background()
	ctx := InjectFromHeaders(base, nil)
	got, ok := ctx.Value(ctxKey{}).(string)
	assert.False(t, ok)
	assert.Empty(t, got)
}
