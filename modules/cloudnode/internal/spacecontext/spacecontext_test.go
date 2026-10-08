package spacecontext

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	"trpc.group/trpc-go/trpc-go/codec"
)

func withMetadataSpace(spaceID string) context.Context {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{gatewayroute.MetadataSpaceID: []byte(spaceID)})
	return ctx
}

func TestFromContextReadsExplicitSpaceID(t *testing.T) {
	got, ok := FromContext(WithSpaceID(context.Background(), "crypto"))
	if !ok || got != "crypto" {
		t.Fatalf("FromContext = %q %v, want crypto", got, ok)
	}
}

func TestFromContextReadsTRPCMetadata(t *testing.T) {
	got, ok := FromContext(withMetadataSpace("crypto"))
	if !ok || got != "crypto" {
		t.Fatalf("FromContext = %q %v, want crypto", got, ok)
	}
}

func TestFromContextPrefersExplicitSpaceIDOverMetadata(t *testing.T) {
	got, ok := FromContext(WithSpaceID(withMetadataSpace("wrong"), "crypto"))
	if !ok || got != "crypto" {
		t.Fatalf("FromContext = %q %v, want explicit crypto", got, ok)
	}
}

func TestFromContextRejectsBlankMetadata(t *testing.T) {
	if got, ok := FromContext(withMetadataSpace("   ")); ok {
		t.Fatalf("FromContext ok = true, want false with value %q", got)
	}
	if _, err := MustFromContext(context.Background()); err == nil {
		t.Fatal("缺少 space 时应当报错")
	}
}
