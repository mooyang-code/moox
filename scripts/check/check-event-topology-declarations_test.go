package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindTopologyDeclarationsIncludesGroupedDeclarations(t *testing.T) {
	root := t.TempDir()
	source := `package sample

const (
	DatasetStream = "MOOX_STORAGE"
)

var (
	MetricTopic = "moox.metrics"
)
`
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := findTopologyDeclarations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2: %v", len(findings), findings)
	}
}

func TestFindTopologyDeclarationsIgnoresTestsAndUnrelatedNames(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("package sample\nconst StreamLimit = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample_test.go"), []byte("package sample\nconst TestStream = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := findTopologyDeclarations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("unexpected findings: %v", findings)
	}
}
