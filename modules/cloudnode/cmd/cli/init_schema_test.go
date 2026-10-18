package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsInitCommand_ShouldDetectInitSubcommand(t *testing.T) {
	assert.True(t, isInitCommand([]string{"moox-cloudnode", "init"}))
	assert.False(t, isInitCommand([]string{"moox-cloudnode", "serve"}))
}

func TestPrintInitError_ShouldWriteJSON(t *testing.T) {
	var stderr bytes.Buffer
	printInitError(&stderr, assert.AnError)
	assert.Contains(t, stderr.String(), "init_failed")
}

func TestRunInitCommandAppliesCloudNodeSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}
