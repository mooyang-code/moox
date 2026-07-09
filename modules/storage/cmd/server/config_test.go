package main

import (
	"os"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
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

func storageConfigWithRoles(roles ...string) storageconfig.StorageConfig {
	return storageconfig.StorageConfig{Roles: roles}
}

func TestStorageSplitViewRolePredicates(t *testing.T) {
	tests := []struct {
		name        string
		roles       []string
		wantQuery   bool
		wantBuilder bool
	}{
		{name: "legacy view", roles: []string{"view"}, wantQuery: true, wantBuilder: true},
		{name: "query only", roles: []string{"view_query"}, wantQuery: true},
		{name: "builder only", roles: []string{"view_builder"}, wantBuilder: true},
		{name: "access only", roles: []string{"access"}},
		{name: "all", roles: []string{"all"}, wantQuery: true, wantBuilder: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := storageConfigWithRoles(tt.roles...)
			if got := shouldRegisterViewQueryRole(cfg); got != tt.wantQuery {
				t.Fatalf("shouldRegisterViewQueryRole = %v, want %v", got, tt.wantQuery)
			}
			if got := shouldStartViewBuilderRole(cfg); got != tt.wantBuilder {
				t.Fatalf("shouldStartViewBuilderRole = %v, want %v", got, tt.wantBuilder)
			}
		})
	}
}
