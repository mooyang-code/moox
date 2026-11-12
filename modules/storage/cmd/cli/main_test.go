package main

import "testing"

func TestResolveSeedPathRequiresExplicitFlagOrEnvironment(t *testing.T) {
	t.Setenv("STORAGE_SEED_FILE", "")
	if got := resolveSeedPath("", "config/storage.yaml"); got != "" {
		t.Fatalf("implicit seed path = %q, want empty", got)
	}
	if got := resolveSeedPath("bundle/metadata.yaml", "config/storage.yaml"); got != "bundle/metadata.yaml" {
		t.Fatalf("explicit seed path = %q", got)
	}
	t.Setenv("STORAGE_SEED_FILE", "bundle/from-env.yaml")
	if got := resolveSeedPath("", "config/storage.yaml"); got != "bundle/from-env.yaml" {
		t.Fatalf("environment seed path = %q", got)
	}
}
