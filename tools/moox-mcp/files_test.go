package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveStaysInRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ws.resolve("a")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if _, err := ws.resolve("../etc/passwd"); err == nil {
		t.Fatal("expected escape to be rejected")
	}
	if _, err := ws.resolve("/etc/passwd"); err == nil {
		t.Fatal("expected absolute escape to be rejected")
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	ws, err := newWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.resolve("out/secret"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
