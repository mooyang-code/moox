package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPathFromArgs(t *testing.T) {
	if got := configPathFromArgs([]string{"moox-storage", "-conf=./config/trpc_go.yaml"}); got != "./config/trpc_go.yaml" {
		t.Fatalf("configPathFromArgs with equals = %q", got)
	}
	if got := configPathFromArgs([]string{"moox-storage", "-conf", "./config/trpc_go.yaml"}); got != "./config/trpc_go.yaml" {
		t.Fatalf("configPathFromArgs with split flag = %q", got)
	}
}

func TestLoadStorageRootFromConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "trpc_go.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  root: ./var/storage\n"), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	root := loadStorageRoot(configPath)
	if root != "./var/storage" {
		t.Fatalf("loadStorageRoot = %q", root)
	}
}
