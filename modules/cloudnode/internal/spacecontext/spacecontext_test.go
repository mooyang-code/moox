package spacecontext

import (
	"context"
	"net/http"
	"testing"

	thttp "trpc.group/trpc-go/trpc-go/http"
)

func TestFromContextReadsExplicitSpaceID(t *testing.T) {
	got, ok := FromContext(WithSpaceID(context.Background(), "crypto"))
	if !ok {
		t.Fatal("FromContext ok = false, want true")
	}
	if got != "crypto" {
		t.Fatalf("FromContext = %q, want crypto", got)
	}
}

func TestFromContextReadsHTTPHeaderWhenFilterDidNotInject(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/trpc.moox.cloudnode.CloudNodeMgr/GetNodeList", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(SpaceIDHeader, "crypto")
	ctx := thttp.WithHeader(context.Background(), &thttp.Header{Request: req})

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext ok = false, want true")
	}
	if got != "crypto" {
		t.Fatalf("FromContext = %q, want crypto", got)
	}
}

func TestFromContextPrefersExplicitSpaceIDOverHTTPHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/trpc.moox.cloudnode.CloudNodeMgr/GetNodeList", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(SpaceIDHeader, "wrong")
	ctx := thttp.WithHeader(WithSpaceID(context.Background(), "crypto"), &thttp.Header{Request: req})

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext ok = false, want true")
	}
	if got != "crypto" {
		t.Fatalf("FromContext = %q, want explicit crypto", got)
	}
}

func TestFromContextRejectsBlankHTTPHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/trpc.moox.cloudnode.CloudNodeMgr/GetNodeList", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(SpaceIDHeader, "   ")
	ctx := thttp.WithHeader(context.Background(), &thttp.Header{Request: req})

	got, ok := FromContext(ctx)
	if ok {
		t.Fatalf("FromContext ok = true, want false with value %q", got)
	}
}
