package main

import (
	"testing"

	"trpc.group/trpc-go/trpc-go"
)

func TestPluginConfigsInitialize(t *testing.T) {
	t.Setenv("MOOX_OTEL_ENDPOINT", "")
	configs := []string{
		"../../config/trpc_go.yaml",
		"../../config/trpc_go.access.yaml",
		"../../config/trpc_go.view.yaml",
		"../../config/trpc_go.view_builder.yaml",
		"../../config/trpc_go.view_index.yaml",
		"../../config/trpc_go.view_query.yaml",
	}
	for _, path := range configs {
		t.Run(path, func(t *testing.T) {
			cfg, err := trpc.LoadConfig(path)
			if err != nil {
				t.Fatalf("load tRPC config: %v", err)
			}
			if server := trpc.NewServerWithConfig(cfg); server == nil {
				t.Fatal("expected initialized tRPC server")
			}
		})
	}
}
