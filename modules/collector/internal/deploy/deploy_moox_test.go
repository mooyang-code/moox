package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeployEnablesCollectorScheduler(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "deploy-moox.sh"))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	mustMkdir(t,
		filepath.Join(root, "scripts"),
		filepath.Join(root, "bin"),
		filepath.Join(root, "modules", "admin", "config"),
		filepath.Join(root, "modules", "collector", "config"),
		filepath.Join(root, "modules", "collector", "configs"),
		filepath.Join(root, "examples"),
	)
	mustWriteFile(t, filepath.Join(root, "scripts", "deploy-moox.sh"), script, 0o755)
	for _, name := range []string{
		"moox-admin",
		"moox-admin-cli",
		"moox-cli",
		"moox-collector",
		"moox-collector-cli",
		"moox-collector-scf",
	} {
		mustWriteFile(t, filepath.Join(root, "bin", name), []byte("#!/usr/bin/env sh\nexit 0\n"), 0o755)
	}
	mustWriteFile(t, filepath.Join(root, "modules", "admin", "config", "app.yaml"), []byte("database:\n  path: ./data/admin.db\n"), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "admin", "config", "gateway.yaml"), []byte("badger:\n  data_dir: \"./data/badger\"\n"), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "collector", "config", "app.yaml"), []byte("database:\n  path: ./data/moox_collector.db\n"), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "collector", "config", "trpc_go.yaml"), []byte(`server:
  service:
    - name: trpc.moox.collector.schedule.timer
      network: "0 */1 * * * *?scheduler=collectorSchedule&disable=1&params="
      protocol: timer
`), 0o644)

	deployDir := filepath.Join(root, "deploy")
	stageDir := filepath.Join(root, "stage")
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "deploy-moox.sh"),
		"--target", "localhost",
		"--dir", deployDir,
		"--stage", stageDir,
		"--skip-build",
		"--no-start",
		"--no-storage",
		"--no-cloudnode",
		"--no-factor",
		"--no-web-host",
	)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy-moox.sh failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(deployDir, "collector", "config", "trpc_go.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "scheduler=collectorSchedule&disable=0&params=space_id=crypto") {
		t.Fatalf("collector scheduler was not enabled with the production space in deployed config:\n%s", text)
	}
	if strings.Contains(text, "scheduler=collectorSchedule&disable=1&params=") {
		t.Fatalf("collector scheduler still disabled in deployed config:\n%s", text)
	}
	if strings.Contains(text, "scheduler=collectorSchedule&disable=0&params=\"") ||
		strings.Contains(text, "scheduler=collectorSchedule&disable=0&params=\n") {
		t.Fatalf("collector scheduler params are empty in deployed config:\n%s", text)
	}
}

func TestDeployStartScriptDefaultsFactorNATSURL(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "deploy-moox.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	want := `"MOOX_FACTOR_NATS_URL=${MOOX_FACTOR_NATS_URL:-nats://127.0.0.1:4222}"`
	if !strings.Contains(text, want) {
		t.Fatalf("deploy start script does not default factor NATS URL; want to find %s", want)
	}
	if strings.Contains(text, `"MOOX_FACTOR_NATS_URL=${MOOX_FACTOR_NATS_URL:-}"`) {
		t.Fatalf("deploy start script still defaults factor NATS URL to empty")
	}
}

func TestDeployRuntimeScriptsStopStaleServiceProcesses(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "deploy-moox.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, want := range []string{
		`pattern="${ROOT}/bin/moox-${name}([[:space:]]|$)"`,
		`pkill -f -- "${pattern}"`,
		`${name}: stopped stale process without pid file`,
		`${name}: stopped stale process with empty pid file`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("deploy runtime scripts do not stop stale service processes; missing %q", want)
		}
	}
	if strings.Contains(text, `pattern="${ROOT}/bin/moox-${name}"`) {
		t.Fatalf("deploy runtime scripts use prefix pkill pattern that can match helper binaries")
	}
}

func TestDeployEnablesStorageFailedViewRetryScheduler(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "deploy-moox.sh"))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	mustMkdir(t,
		filepath.Join(root, "scripts"),
		filepath.Join(root, "bin"),
		filepath.Join(root, "modules", "admin", "config"),
		filepath.Join(root, "modules", "storage", "config"),
		filepath.Join(root, "modules", "storage", "schema"),
		filepath.Join(root, "examples"),
	)
	mustWriteFile(t, filepath.Join(root, "scripts", "deploy-moox.sh"), script, 0o755)
	for _, name := range []string{
		"moox-admin",
		"moox-admin-cli",
		"moox-cli",
		"moox-storage",
		"moox-storage-cli",
	} {
		mustWriteFile(t, filepath.Join(root, "bin", name), []byte("#!/usr/bin/env sh\nexit 0\n"), 0o755)
	}
	mustWriteFile(t, filepath.Join(root, "modules", "admin", "config", "app.yaml"), []byte("database:\n  path: ./data/admin.db\n"), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "admin", "config", "gateway.yaml"), []byte("badger:\n  data_dir: \"./data/badger\"\n"), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "storage", "schema", "metadata.sql"), []byte("-- test schema\n"), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "storage", "config", "storage.yaml"), []byte(`storage:
  root: ./var/storage
  metadata:
    path: ./var/storage/metadata/storage_metadata.db
  eventbus:
    type: memory
    embedded:
      enabled: false
`), 0o644)
	mustWriteFile(t, filepath.Join(root, "modules", "storage", "config", "trpc_go.yaml"), []byte(`server:
  service:
    - name: trpc.moox.storage.view.retry_failed.timer
      network: "0 */10 * * * *?disable=1&scheduler=viewBuilderSchedule&params=op=retry_failed"
      protocol: timer
`), 0o644)

	deployDir := filepath.Join(root, "deploy")
	stageDir := filepath.Join(root, "stage")
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "deploy-moox.sh"),
		"--target", "localhost",
		"--dir", deployDir,
		"--stage", stageDir,
		"--skip-build",
		"--no-start",
		"--no-cloudnode",
		"--no-collector",
		"--no-factor",
		"--no-web-host",
	)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy-moox.sh failed: %v\n%s", err, out)
	}

	got, err := os.ReadFile(filepath.Join(deployDir, "storage", "config", "trpc_go.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "disable=0&scheduler=viewBuilderSchedule&params=op=retry_failed") {
		t.Fatalf("storage retry_failed scheduler was not enabled in deployed config:\n%s", text)
	}
	if strings.Contains(text, "disable=1&scheduler=viewBuilderSchedule&params=op=retry_failed") {
		t.Fatalf("storage retry_failed scheduler still disabled in deployed config:\n%s", text)
	}

	gotStorage, err := os.ReadFile(filepath.Join(deployDir, "storage", "config", "storage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	storageText := string(gotStorage)
	if !strings.Contains(storageText, "type: nats") || !strings.Contains(storageText, "enabled: true") {
		t.Fatalf("storage eventbus was not patched to embedded NATS in deployed config:\n%s", storageText)
	}
}

func TestDeployStartScriptWaitsForFactorNATS(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "deploy-moox.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, want := range []string{
		"factor_nats_endpoint()",
		"wait_factor_nats()",
		"wait_factor_nats\n  ensure_factor_python",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("deploy start script does not wait for factor NATS; missing %q", want)
		}
	}
}

func mustMkdir(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func mustWriteFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatal(err)
	}
}
