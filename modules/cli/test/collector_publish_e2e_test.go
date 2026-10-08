package test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorPublishCommandsRejectPositionalArguments(t *testing.T) {
	binary := buildMooxCLI(t)
	cases := [][]string{
		{"collector", "function", "publish", "status", "node-batch-fake", "--job-id", "node-batch-real"},
		{"collector", "function", "publish", "submit", "collector.zip"},
	}
	for _, args := range cases {
		command := exec.Command(binary, args...)
		output, err := command.CombinedOutput()
		require.Error(t, err, "command unexpectedly accepted positional args: %v\n%s", args, output)
		assert.Contains(t, string(output), "unknown command")
	}
}

func buildMooxCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "moox-cli")
	command := exec.Command("go", "build", "-o", binary, "../cmd/moox-cli")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build moox-cli: %s", output)
	return binary
}
