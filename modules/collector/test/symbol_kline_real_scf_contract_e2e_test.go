package test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSymbolKlineRealSCFRunnerContract(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	runCommand(t, root, "node", "--test", "examples/e2e/collector-symbol-kline.test.mjs")
	runCommand(t, root, "bash", "examples/e2e/test-run-real-symbol-kline-scf.sh")
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func runCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, output)
	}
}
