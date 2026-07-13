package main

import (
	"testing"

	"trpc.group/trpc-go/trpc-go"
)

func TestPluginConfigInitializes(t *testing.T) {
	t.Setenv("MOOX_OTEL_ENDPOINT", "")
	cfg, err := trpc.LoadConfig("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatalf("load tRPC config: %v", err)
	}
	server := trpc.NewServerWithConfig(cfg)
	if server == nil {
		t.Fatal("expected initialized tRPC server")
	}
}
