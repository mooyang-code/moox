package spacecontext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestWithSpaceID(t *testing.T) {
	got, ok := FromContext(WithSpaceID(context.Background(), "crypto"))
	require.True(t, ok)
	require.Equal(t, "crypto", got)

	got, ok = FromContext(WithSpaceID(context.Background(), ""))
	require.False(t, ok)
	require.Empty(t, got)

	got, ok = FromContext(context.Background())
	require.False(t, ok)
	require.Empty(t, got)
}

func TestFromContextReadsNativeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata codec.MetaData
		want     string
		ok       bool
	}{
		{name: "canonical", metadata: codec.MetaData{SpaceIDHeader: []byte("crypto")}, want: "crypto", ok: true},
		{name: "lowercase and whitespace", metadata: codec.MetaData{"x-space-id": []byte(" crypto ")}, want: "crypto", ok: true},
		{name: "identical aliases", metadata: codec.MetaData{SpaceIDHeader: []byte("crypto"), "x-space-id": []byte("crypto")}, want: "crypto", ok: true},
		{name: "conflicting aliases", metadata: codec.MetaData{SpaceIDHeader: []byte("crypto"), "x-space-id": []byte("stock")}, want: "", ok: false},
		{name: "blank", metadata: codec.MetaData{SpaceIDHeader: []byte("   ")}, want: "", ok: false},
		{name: "missing", metadata: nil, want: "", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, message := codec.WithNewMessage(context.Background())
			defer codec.PutBackMessage(message)
			message.WithServerMetaData(tc.metadata)
			got, ok := FromContext(ctx)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.ok, ok)

			_, err := spaceServerFilter(ctx, nil, func(ctx context.Context, _ interface{}) (interface{}, error) {
				filtered, filteredOK := FromContext(ctx)
				require.Equal(t, tc.want, filtered)
				require.Equal(t, tc.ok, filteredOK)
				return nil, nil
			})
			require.NoError(t, err)
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
}
