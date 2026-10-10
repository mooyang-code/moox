package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDoesNotProvisionOrCreateState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.yaml")
	if err := os.WriteFile(path, []byte("tls:\n  storage_root: ./state\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"validate", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatal("validate provisioned Caddy state")
	}
	out.Reset()
	if err := run(context.Background(), []string{"check-state", "--config", path}, &out); err == nil || out.Len() != 0 {
		t.Fatal("check-state accepted a missing internal CA or emitted success output")
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatal("check-state created persistent state")
	}
	out.Reset()
	if err := run(context.Background(), []string{"stop-budget", "--config", path}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "45\n" {
		t.Fatalf("unexpected script stop budget %q", out.String())
	}
	for _, args := range [][]string{{}, {"reload"}, {"serve", "extra"}, {"validate", "--config", path, "extra"}, {"serve", "--import-existing"}, {"serve", "--for-import"}, {"initialize-state", "--for-import"}} {
		if err := run(context.Background(), args, &out); err == nil {
			t.Fatalf("accepted unsupported command %v", args)
		}
	}
	if strings.Contains(out.String(), "private") {
		t.Fatal("sensitive state included in command output")
	}
}

func TestConfigurationCannotRetainCAInitializationPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.yaml")
	for _, value := range []string{"true", "false"} {
		if err := os.WriteFile(path, []byte("tls:\n  initialize_ca: "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := run(context.Background(), []string{"validate", "--config", path}, &out); err == nil || out.Len() != 0 {
			t.Fatal("service configuration accepted a reusable CA generation permission")
		}
	}
}
