package spacecontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestFromContextReadsNativeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string][]byte
		want     string
	}{
		{name: "canonical", metadata: map[string][]byte{SpaceIDHeader: []byte("crypto")}, want: "crypto"},
		{name: "lowercase and whitespace", metadata: map[string][]byte{"x-space-id": []byte(" crypto ")}, want: "crypto"},
		{name: "missing"},
		{name: "blank", metadata: map[string][]byte{SpaceIDHeader: []byte("   ")}},
		{name: "conflicting keys", metadata: map[string][]byte{SpaceIDHeader: []byte("crypto"), "x-space-id": []byte("stock")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, message := codec.WithNewMessage(context.Background())
			defer codec.PutBackMessage(message)
			message.WithServerMetaData(tc.metadata)
			got, ok := FromContext(ctx)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.want != "", ok)
		})
	}
}

func TestFromContextPrefersExplicitSpaceID(t *testing.T) {
	ctx, message := codec.WithNewMessage(context.Background())
	defer codec.PutBackMessage(message)
	message.WithServerMetaData(map[string][]byte{SpaceIDHeader: []byte("wrong")})
	got, ok := FromContext(WithSpaceID(ctx, " crypto "))
	require.True(t, ok)
	require.Equal(t, "crypto", got)
	_, err := MustFromContext(context.Background())
	require.Error(t, err)
}
