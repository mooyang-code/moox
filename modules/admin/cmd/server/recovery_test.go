package main

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-go/filter"
)

func TestRecoveryFilterConvertsPanicAndKeepsServing(t *testing.T) {
	recovery := filter.GetServer("recovery")
	if recovery == nil {
		t.Fatal("recovery filter is not registered")
	}

	_, err := recovery(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		panic("test panic")
	})
	if err == nil {
		t.Fatal("panic must be converted to a server error")
	}
	if err.Error() == "test panic" {
		t.Fatalf("panic details leaked in error: %v", err)
	}

	want := "still-serving"
	got, err := recovery(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		return want, nil
	})
	if err != nil {
		t.Fatalf("normal request returned error: %v", err)
	}
	if got != want {
		t.Fatalf("response = %v, want %q", got, want)
	}
}
