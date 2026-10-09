package spacecontext

import (
	"context"
	"testing"

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

func TestNativeSpaceMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata codec.MetaData
		want     string
		ok       bool
	}{
		{"canonical", codec.MetaData{"X-Space-Id": []byte("crypto")}, "crypto", true},
		{"case and whitespace", codec.MetaData{"x-space-id": []byte(" crypto ")}, "crypto", true},
		{"identical aliases", codec.MetaData{"X-Space-Id": []byte("crypto"), "x-space-id": []byte("crypto")}, "crypto", true},
		{"conflicting aliases", codec.MetaData{"X-Space-Id": []byte("crypto"), "x-space-id": []byte("another")}, "", false},
		{"empty", codec.MetaData{"X-Space-Id": []byte(" ")}, "", false},
		{"missing", nil, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, message := codec.WithNewMessage(context.Background())
			defer codec.PutBackMessage(message)
			message.WithServerMetaData(test.metadata)
			_, err := spaceServerFilter(ctx, nil, func(ctx context.Context, _ interface{}) (interface{}, error) {
				got, ok := FromContext(ctx)
				assert.Equal(t, test.want, got)
				assert.Equal(t, test.ok, ok)
				return nil, nil
			})
			assert.NoError(t, err)
		})
	}
}
