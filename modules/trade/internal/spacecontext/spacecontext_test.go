package spacecontext

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/assert"
	"trpc.group/trpc-go/trpc-go/codec"
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

func withMetadataSpace(spaceID string) context.Context {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{gatewayroute.MetadataSpaceID: []byte(spaceID)})
	return ctx
}

func TestSpaceContext_FromContext_ReadsTRPCMetadata(t *testing.T) {
	got, ok := FromContext(withMetadataSpace("crypto"))
	assert.True(t, ok)
	assert.Equal(t, "crypto", got)
}

func TestSpaceContext_FromContext_PrefersExplicitSpaceID(t *testing.T) {
	got, ok := FromContext(WithSpaceID(withMetadataSpace("wrong"), "crypto"))
	assert.True(t, ok)
	assert.Equal(t, "crypto", got)
}

func TestSpaceContext_FromContext_RejectsBlankMetadata(t *testing.T) {
	_, ok := FromContext(withMetadataSpace("   "))
	assert.False(t, ok)
}
