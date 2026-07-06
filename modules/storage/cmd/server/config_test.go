package main

import (
	"os"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestStorageTRPCConfigServiceNamesAreUnique(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatalf("read trpc_go.yaml: %v", err)
	}

	var cfg struct {
		Server struct {
			Service []struct {
				Name string `yaml:"name"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse trpc_go.yaml: %v", err)
	}

	seen := make(map[string]bool)
	for _, service := range cfg.Server.Service {
		if service.Name == "" {
			t.Fatal("server service name must not be empty")
		}
		if seen[service.Name] {
			t.Fatalf("duplicate server service name %q shadows an earlier listener", service.Name)
		}
		seen[service.Name] = true
	}
}
