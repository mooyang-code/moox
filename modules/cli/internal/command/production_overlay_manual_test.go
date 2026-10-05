package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/nats-io/nats.go"
	cls "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cls/v20201016"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	scf "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf/v20180416"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestManualProductionCollectorPageOverlay(t *testing.T) {
	if os.Getenv("MOOX_RUN_PRODUCTION_COLLECTOR_OVERLAY") != "1" {
		t.Skip("manual production operation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	host := snapshot.Manifest.ControlHost
	control, err := dialSetupHost(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	preflight, err := control.Run(ctx, []string{"sh", "-lc", `set -eu
root="$1"
test -x "$root/start.sh"
test -x "$root/stop.sh"
test -r "$root/config/components.env"
grep -Fq 'MOOX_INSTALLED_WITH_' "$root/config/components.env"
for name in gateway-control.env gateway-service.env gateway-moox-cli.env; do test -s "$root/secrets/$name"; done
printf 'overlay_preflight=ok\n'
printf 'installed_components:\n'
grep '^MOOX_INSTALLED_WITH_' "$root/config/components.env"
printf 'selected_processes:\n'
ps -eo comm= | grep -E '^moox-collector(-subject)?$' || true
printf 'free_bytes:\n'
df -B1 --output=avail "$root" | tail -1
`, "moox-page-overlay-preflight", paths.ControlRoot}, nil)
	if err != nil {
		t.Fatalf("production overlay preflight failed: %v", err)
	}
	t.Log(preflight.Stdout)
	controlURL, controlKey, serviceKey, caBundle, err := controlGatewayMaterial(ctx, control, host.Address, false, paths.ControlRoot, setupdeploy.TLSMode(host.TLSMode))
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.MkdirTemp("", "moox-page-overlay-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	controlKeyPath := filepath.Join(tmp, "gateway-control.key")
	serviceKeyPath := filepath.Join(tmp, "gateway-service.key")
	caPath := filepath.Join(tmp, "gateway-ca.pem")
	for path, value := range map[string][]byte{
		controlKeyPath: []byte(controlKey),
		serviceKeyPath: []byte(serviceKey),
		caPath:         caBundle,
	} {
		if err := os.WriteFile(path, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{
		"--target", host.Username + "@" + host.Address,
		"--dir", paths.ControlRoot,
		"--goos", "linux", "--goarch", "amd64", "--skip-build",
		"--component-overlay", "--no-admin", "--no-web-host", "--no-gateway",
		"--no-storage", "--no-storage-access", "--no-archive", "--no-eventbus", "--no-cloudnode",
		"--no-factor-mgr", "--no-strategy", "--no-trade", "--no-monitor", "--no-hostagent",
		"--node-id", "control", "--gateway-control-url", controlURL,
		"--gateway-ca-bundle", caPath, "--gateway-control-key-file", controlKeyPath, "--gateway-service-key-file", serviceKeyPath,
		"--public-host", host.Address, "--tls-mode", "internal", "--browser-https-port", "9527",
		"--service-https-port", "11001", "--local-ca", "skip", "--target-ca", "skip",
	}
	commandArgs := append([]string{filepath.Join(root, "scripts/deploy/deploy-moox.sh")}, args...)
	cmd := exec.Command("bash", commandArgs...)
	cmd.Dir = root
	pathEnv := "/tmp/moox-sshpass-wrapper:" + os.Getenv("PATH")
	cmd.Env = append(os.Environ(), "PATH="+pathEnv, "SSHPASS="+host.Password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("component overlay failed: %v\n%s", err, string(out))
	}
	t.Log(strings.TrimSpace(fmt.Sprintf("%s", out)))
}

func TestManualProductionStorageAccessOverlay(t *testing.T) {
	if os.Getenv("MOOX_RUN_PRODUCTION_STORAGE_ACCESS_OVERLAY") != "1" {
		t.Skip("manual production component deployment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	storageHost := snapshot.Manifest.StorageHost
	storage, err := dialSetupHost(ctx, storageHost)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	preflight, err := storage.Run(ctx, []string{"sh", "-lc", `set -eu
	root="$1"
	test -x "$root/start.sh"
	test -x "$root/stop.sh"
	test -x "$root/bin/moox-storage-access"
	test -r "$root/config/components.env"
	grep -Fxq 'MOOX_INSTALLED_WITH_STORAGE=1' "$root/config/components.env"
	grep -Fxq 'MOOX_INSTALLED_WITH_STORAGE_ACCESS=1' "$root/config/components.env"
	for name in gateway-control.env gateway-service.env gateway-moox-cli.env storage-access.env; do test -s "$root/secrets/$name"; done
	status=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 2 http://127.0.0.1:11014/readyz || true)
	printf 'storage_access_overlay_preflight=ok health=%s\n' "${status:-unreachable}"
	ps -eo comm= | grep -E '^moox-storage-access$' || true
	df -h "$root"
	`, "moox-storage-access-preflight", paths.StorageRoot}, nil)
	if err != nil {
		t.Fatalf("production Storage AccessProxy preflight failed: %v\n%s\n%s", err, preflight.Stdout, preflight.Stderr)
	}
	t.Log(preflight.Stdout)
	upstreamEnv, err := storage.Run(ctx, []string{"sh", "-lc", `set -eu
	root="$1"
	awk -F= '/^MOOX_STORAGE_ACCESS_UPSTREAM_TARGET(_NODE)?=/{print}' "$root/secrets/storage-access.env"
	`, "moox-storage-access-upstream", paths.StorageRoot}, nil)
	if err != nil {
		t.Fatalf("read Storage Access upstream configuration failed: %v", err)
	}
	upstreamTarget := ""
	upstreamNode := ""
	for _, line := range strings.Split(strings.TrimSpace(upstreamEnv.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, "\"'")
		switch key {
		case "MOOX_STORAGE_ACCESS_UPSTREAM_TARGET":
			upstreamTarget = value
		case "MOOX_STORAGE_ACCESS_UPSTREAM_TARGET_NODE":
			upstreamNode = value
		}
	}
	if upstreamTarget == "" {
		t.Fatal("Storage Access upstream target is not configured on the production host")
	}
	t.Logf("Storage Access upstream target: %s (node %s)", upstreamTarget, upstreamNode)
	controlHost := snapshot.Manifest.ControlHost
	gatewayControl, err := dialSetupHost(ctx, controlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer gatewayControl.Close()
	controlURL, controlKey, serviceKey, caBundle, err := controlGatewayMaterial(ctx, gatewayControl, controlHost.Address, false, paths.ControlRoot, setupdeploy.TLSMode(controlHost.TLSMode))
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.MkdirTemp("", "moox-storage-access-overlay-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	controlKeyPath := filepath.Join(tmp, "gateway-control.key")
	serviceKeyPath := filepath.Join(tmp, "gateway-service.key")
	caPath := filepath.Join(tmp, "gateway-ca.pem")
	for path, value := range map[string][]byte{
		controlKeyPath: []byte(controlKey),
		serviceKeyPath: []byte(serviceKey),
		caPath:         caBundle,
	} {
		if err := os.WriteFile(path, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{
		"--target", storageHost.Username + "@" + storageHost.Address,
		"--dir", paths.StorageRoot,
		"--goos", "linux", "--goarch", "amd64", "--skip-build",
		"--component-overlay", "--no-admin", "--no-storage", "--with-storage-access", "--no-web-host", "--no-gateway",
		"--no-archive", "--no-eventbus", "--no-cloudnode", "--no-collector", "--no-factor-mgr", "--no-strategy",
		"--no-trade", "--no-monitor", "--no-hostagent",
		"--node-id", "storage", "--gateway-control-url", controlURL,
		"--gateway-ca-bundle", caPath, "--gateway-control-key-file", controlKeyPath, "--gateway-service-key-file", serviceKeyPath,
		"--public-host", storageHost.Address, "--tls-mode", "internal", "--browser-https-port", "9527",
		"--service-https-port", "11001", "--local-ca", "skip", "--target-ca", "skip",
	}
	commandArgs := append([]string{filepath.Join(root, "scripts/deploy/deploy-moox.sh")}, args...)
	cmd := exec.Command("bash", commandArgs...)
	cmd.Dir = root
	pathEnv := "/tmp/moox-sshpass-wrapper:" + os.Getenv("PATH")
	cmd.Env = append(os.Environ(), "PATH="+pathEnv, "SSHPASS="+storageHost.Password,
		"MOOX_STORAGE_ACCESS_UPSTREAM_TARGET="+upstreamTarget,
		"MOOX_STORAGE_ACCESS_UPSTREAM_TARGET_NODE="+upstreamNode)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Storage AccessProxy overlay failed: %v\n%s", err, string(out))
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestManualProductionStorageAccessTargetBinaryOverlay(t *testing.T) {
	if os.Getenv("MOOX_RUN_PRODUCTION_STORAGE_ACCESS_TARGET_OVERLAY") != "1" {
		t.Skip("manual production binary deployment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	var host *setupconfig.Host
	for index := range snapshot.Manifest.OtherHosts {
		if snapshot.Manifest.OtherHosts[index].Name == "compute-1" {
			host = &snapshot.Manifest.OtherHosts[index]
			break
		}
	}
	if host == nil {
		t.Fatal("production Storage Access target host compute-1 is not configured")
	}
	binaryPath := filepath.Join(root, "bin/moox-storage-access")
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	newHash := fmt.Sprintf("%x", sha256.Sum256(binary))
	remote, err := dialSetupHost(ctx, *host)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	const remoteRoot = "/home/ubuntu/moox/storage-access"
	preflight, err := remote.Run(ctx, []string{"sh", "-lc", `set -eu
	root="$1"
	test -x "$root/start.sh"
	test -x "$root/stop.sh"
	test -s "$root/secrets/access.env"
	grep -q '^MOOX_STORAGE_ACCESS_INBOUND_SECRET=' "$root/secrets/access.env"
	grep -q '^MOOX_STORAGE_ACCESS_UPSTREAM_SECRET=' "$root/secrets/access.env"
	pid=$(cat "$root/run/storage-access.pid")
	kill -0 "$pid"
	ss -ltnp | grep -E '0\.0\.0\.0:12004|\[::\]:12004'
	printf 'preflight=ok pid=%s binary_sha256=%s\n' "$pid" "$(sha256sum "$root/bin/moox-storage-access" | awk '{print $1}')"
	awk -F= '/^MOOX_STORAGE_ACCESS_(TARGET_NODE|UPSTREAM_TARGET|UPSTREAM_TARGET_NODE|ALLOWED_CALLERS)=/{print}' "$root/secrets/access.env"
	`, "moox-storage-access-binary-preflight", remoteRoot}, nil)
	if err != nil {
		t.Fatalf("production AccessProxy binary preflight failed: %v\n%s\n%s", err, preflight.Stdout, preflight.Stderr)
	}
	t.Log(preflight.Stdout)
	tmpPath := fmt.Sprintf("%s/bin/.moox-storage-access.%d", remoteRoot, time.Now().UnixNano())
	if err := remote.Upload(ctx, bytes.NewReader(binary), int64(len(binary)), tmpPath, 0700); err != nil {
		t.Fatalf("upload Storage Access binary: %v", err)
	}
	deploy, err := remote.Run(ctx, []string{"bash", "-lc", `set -euo pipefail
	root="$1"
	candidate="$2"
	expected="$3"
	actual=$(sha256sum "$candidate" | awk '{print $1}')
	[[ "$actual" == "$expected" ]]
	backup="$root/bin/moox-storage-access.pre-period-rpc"
	if [[ -e "$backup" ]]; then backup="$backup.$(date +%s)"; fi
	mv "$root/bin/moox-storage-access" "$backup"
	mv "$candidate" "$root/bin/moox-storage-access"
	chmod 0755 "$root/bin/moox-storage-access"
	if ! { "$root/stop.sh" && "$root/start.sh"; }; then
	  "$root/stop.sh" || true
	  mv "$root/bin/moox-storage-access" "$root/bin/moox-storage-access.failed"
	  mv "$backup" "$root/bin/moox-storage-access"
	  "$root/start.sh" || true
	  exit 1
	fi
	for _ in $(seq 1 30); do
	  pid=$(cat "$root/run/storage-access.pid" 2>/dev/null || true)
	  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null && ss -ltnp | grep -qE '0\.0\.0\.0:12004|\[::\]:12004'; then
	    running_hash=$(sha256sum "/proc/$pid/exe" | awk '{print $1}')
	    if [[ "$running_hash" == "$expected" ]]; then
	      printf 'deployment=ok pid=%s binary_sha256=%s backup=%s\n' "$pid" "$running_hash" "$backup"
	      break
	    fi
	  fi
	  sleep 1
	done
	if [[ "${running_hash:-}" != "$expected" ]]; then
	  "$root/stop.sh" || true
	  mv "$root/bin/moox-storage-access" "$root/bin/moox-storage-access.failed"
	  mv "$backup" "$root/bin/moox-storage-access"
	  "$root/start.sh" || true
	  echo 'new Storage Access process did not become ready; previous binary restored' >&2
	  exit 1
	fi
	`, "moox-storage-access-binary-deploy", remoteRoot, tmpPath, newHash}, nil)
	if err != nil {
		t.Fatalf("deploy Storage Access binary on SCF target host: %v\n%s\n%s", err, deploy.Stdout, deploy.Stderr)
	}
	t.Log(deploy.Stdout)
}

func TestManualProductionOverlayProgress(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_OVERLAY_PROGRESS") != "1" {
		t.Skip("manual production diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	result, err := control.Run(ctx, []string{"sh", "-lc", `set -eu
root="$1"
printf 'overlay_processes:\n'
ps -eo pid=,etime=,comm=,args= | grep -E 'deploy-moox|moox-collector|python3' | grep -v grep || true
python_pid=$(ps -eo pid=,args= | awk '/python3 - \/data\/moox\/prod\/data\/collector\/moox_collector\.db/ {print $1; exit}')
if test -n "$python_pid"; then ps -o pid=,etime=,%cpu=,rss=,wchan= -p "$python_pid" || true; fi
for name in collector collector-subject; do
  pid=$(cat "$root/run/$name.pid" 2>/dev/null || true)
  if test -n "$pid" && test -d "/proc/$pid"; then
    ps -o pid=,ppid=,etime=,args= -p "$pid" || true
    printf 'cgroup_%s=' "$name"
    tr '\n' ' ' <"/proc/$pid/cgroup" 2>/dev/null || true
    printf '\n'
  fi
done
if command -v systemctl >/dev/null 2>&1; then
  printf 'collector_units:\n'
  systemctl --user list-units --all --no-legend 2>/dev/null | grep -E 'moox-(collector|storage-access)' || true
fi
printf 'collector_cron_entries:\n'
for account in ubuntu root; do
  crontab -l -u "$account" 2>/dev/null | grep -Ei 'moox|collector|start\.sh' | sed -E 's/([[:alnum:]_]*(SECRET|PASSWORD|TOKEN|KEY)[[:alnum:]_]*=)[^[:space:]]+/\1[redacted]/g' || true
done
for file in /etc/crontab /etc/cron.d/*; do
  test -f "$file" || continue
  grep -Ei 'moox|collector|start\.sh' "$file" 2>/dev/null | sed -E 's/([[:alnum:]_]*(SECRET|PASSWORD|TOKEN|KEY)[[:alnum:]_]*=)[^[:space:]]+/\1[redacted]/g' || true
done
for proc in /proc/[0-9]*; do
  executable=$(readlink -f "$proc/exe" 2>/dev/null || true)
  case "$executable" in
    "$root"/bin/moox-collector|"$root"/bin/moox-collector-subject|"$root"/bin/moox-web-host)
      printf 'pid=%s executable=%s\n' "${proc#/proc/}" "$executable"
      ;;
  esac
done
printf 'collector_logs:\n'
for file in "$root/logs/collector.log" "$root/logs/collector-subject.log"; do
  if test -r "$file"; then printf '%s\n' "$file"; tail -n 25 "$file"; fi
done
printf 'service_health:\n'
"$root/healthcheck.sh" collector collector-subject 2>&1 || true
for item in collector:11412 web-host:19527; do
  name=${item%%:*}
  port=${item##*:}
  status=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 2 "http://127.0.0.1:$port/healthz" || true)
  printf '%s=%s\n' "$name" "${status:-unreachable}"
done
printf 'disk:\n'
df -h "$root" /
ls -lh "$root/data/collector/moox_collector.db"* 2>/dev/null || true
`, "moox-overlay-progress", paths.ControlRoot}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(result.Stdout)
}

func TestManualStopProductionCollectorRuntimePurge(t *testing.T) {
	if os.Getenv("MOOX_STOP_PRODUCTION_COLLECTOR_RUNTIME_PURGE") != "1" {
		t.Skip("manual production recovery action")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	result, err := control.Run(ctx, []string{"bash", "-lc", `set -eu
	db="$1/data/collector/moox_collector.db"
	pid=$(ps -eo pid=,args= | awk -v db="$db" '$0 ~ /python3 -c import sqlite3/ && index($0, db) { print $1; exit }')
	if [[ -z "$pid" ]]; then
	  echo 'no active Collector runtime purge found'
	  exit 0
	fi
	echo "stopping Collector runtime purge pid=$pid"
	kill -TERM "$pid"
	for _ in $(seq 1 30); do
	  kill -0 "$pid" 2>/dev/null || { echo 'purge stopped'; exit 0; }
	  sleep 1
	done
	kill -KILL "$pid" 2>/dev/null || true
	echo 'purge stopped after forced termination'
	`, "moox-stop-runtime-purge", paths.ControlRoot}, nil)
	if err != nil {
		t.Fatalf("stop active runtime purge: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	t.Log(result.Stdout)
}

func TestManualProductionCollectorDataCounts(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_COLLECTOR_COUNTS") != "1" {
		t.Skip("manual production diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	api, cleanup, err := newCollectorTimerInventoryClient(ctx, collectorTimerInventoryOptions{
		ControlURL: "https://" + snapshot.Manifest.ControlHost.Address + ":11001",
		File:       filepath.Join(root, "moox.toml"),
		SpaceID:    "crypto",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	startedAt := time.Now()
	tasks, err := api.ListTasks(ctx, "crypto", "kline", nil)
	if err != nil {
		t.Fatalf("read Collector task API: %v", err)
	}
	t.Logf("task_api_elapsed=%s tasks=%d", time.Since(startedAt), len(tasks))
	for _, task := range tasks {
		t.Logf("api_task space=%s id=%s name=%q enabled=%t result_status=%s last_data_time=%s view_id=%s", task.SpaceID, task.TaskID, task.TaskName, task.Enabled, task.Result.Status, task.Result.LastDataTime, task.Result.ViewID)
	}
	nodes, err := api.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		CloudAccountID: "tencent-scf",
		Region:         "ap-hongkong",
		NodeType:       "scf-event",
		BizType:        "market_fetcher",
		PageSize:       500,
	})
	if err != nil {
		t.Fatalf("read crypto SCF node catalog: %v", err)
	}
	nodeCounts := make(map[string]int)
	for _, node := range nodes {
		if node.Namespace != "moox-crypto" && node.Namespace != "moox-crypto-ns2" {
			continue
		}
		key := strings.Join([]string{node.Namespace, node.Region, node.TriggerType, node.PackageID}, "|")
		nodeCounts[key]++
	}
	t.Logf("crypto_invoke_catalog_nodes=%d groups=%v", len(nodes), nodeCounts)
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	result, err := control.Run(ctx, []string{"sh", "-lc", `set -eu
db="$1/data/collector/moox_collector.db"
test -r "$db"
python3 - "$db" <<'PY'
import sqlite3
import sys

db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
db.execute("PRAGMA query_only=ON")
for task in db.execute("SELECT c_space_id,c_task_id,c_task_name,c_enabled FROM t_collector_tasks WHERE c_space_id=?", ("crypto",)):
    print("task space_id={} task_id={} task_name={!r} enabled={}".format(*task))
print("runtime_table_counts=")
tables = ["t_collector_runs", "t_collector_task_instances", "t_collector_instance_write_targets",
          "t_collector_fetch_batches", "t_collector_fetch_batch_items", "t_collector_fetch_retry_items",
          "t_collector_timer_period_batches", "t_collector_task_period_series", "t_collector_period_storage_states",
          "t_period_readiness"]
for table in tables:
    print("{}={}".format(table, db.execute("SELECT count(*) FROM " + table + " WHERE c_space_id=?", ("crypto",)).fetchone()[0]))
print("t_period_readiness_items={}".format(db.execute("SELECT count(*) FROM t_period_readiness_items items JOIN t_period_readiness readiness ON readiness.c_id=items.c_readiness_id WHERE readiness.c_space_id=?", ("crypto",)).fetchone()[0]))
print("retry_pending={}".format(db.execute("SELECT count(*) FROM t_collector_fetch_retry_items WHERE c_space_id=? AND c_status='pending'", ("crypto",)).fetchone()[0]))
print("retry_dispatched={}".format(db.execute("SELECT count(*) FROM t_collector_fetch_retry_items WHERE c_space_id=? AND c_status='dispatched'", ("crypto",)).fetchone()[0]))
print("batch_active={}".format(db.execute("SELECT count(*) FROM t_collector_fetch_batches WHERE c_space_id=? AND c_status IN ('planned','dispatched')", ("crypto",)).fetchone()[0]))
print("latest_runs=")
for row in db.execute("SELECT c_run_id,c_status,c_error_summary,c_ctime FROM t_collector_runs WHERE c_space_id=? ORDER BY c_id DESC LIMIT 5", ("crypto",)):
    print("run_id={} status={} error={!r} ctime={}".format(*row))
print("latest_batch_routes=")
for row in db.execute("SELECT c_batch_id,c_status,c_request_json FROM t_collector_fetch_batches WHERE c_space_id=? ORDER BY c_id DESC LIMIT 10", ("crypto",)):
    try:
        request = json.loads(row[2] or "{}")
    except Exception:
        request = {}
    print("batch_id={} status={} storage_target={!r} region={!r} function={!r}".format(row[0], row[1], request.get("storage_rpc_gateway_target"), request.get("region"), request.get("function_name")))
print("sqlite page_count={} freelist_count={} journal_mode={}".format(db.execute("PRAGMA page_count").fetchone()[0], db.execute("PRAGMA freelist_count").fetchone()[0], db.execute("PRAGMA journal_mode").fetchone()[0]))
PY
printf 'completion_diagnostics:\n'
for log_file in "$1"/logs/collector/*.log "$1"/logs/collector-subject/*.log; do
  test -f "$log_file" || continue
  printf '%s\n' "log_file=$log_file"
  tail -n 2000 "$log_file" | grep -Ei 'market fetch completion handling failed|retry maintenance failed|scheduler failed|SCF market fetch|completion EventBus|completion consumer|deadline exceeded|timeout' | tail -30 || true
done
`, "moox-collector-data-counts", paths.ControlRoot}, nil)
	if err != nil {
		t.Fatalf("read Collector production diagnostics: %v\nstdout:\n%s\nstderr:\n%s", err, result.Stdout, result.Stderr)
	}
	t.Log(result.Stdout)
}

func TestManualProductionTaskResultKlineReadback(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_TASK_KLINE") != "1" {
		t.Skip("manual production read-only data verification")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, err := defaultSetupExportSkillConfig(ctx, snapshot, "crypto")
	if err != nil {
		t.Fatalf("load read-only Storage access credentials: %v", err)
	}
	defer func() {
		cfg.Gateway.Secret = ""
		cfg.Storage.AppKey = ""
	}()
	storageHost, err := dialSetupHost(ctx, snapshot.Manifest.StorageHost)
	if err != nil {
		t.Fatalf("connect Storage host for internal read-only auth: %v", err)
	}
	defer storageHost.Close()
	storageRaw, err := readRemoteSkillSecret(ctx, storageHost, filepath.Join(snapshot.Manifest.Paths.Resolved().StorageRoot, "secrets/storage-internal-auth.env"))
	if err != nil {
		t.Fatalf("read Storage Primary auth for read-only verification: %v", err)
	}
	primarySecret, err := collectorStoragePrimaryAuthSecret(storageRaw)
	for index := range storageRaw {
		storageRaw[index] = 0
	}
	if err != nil {
		t.Fatalf("parse Storage Primary auth for read-only verification: %v", err)
	}
	const primaryReadAppID = "moox-cli-data-export"
	primaryAuth := &storagepb.AuthInfo{AppId: primaryReadAppID, AppKey: security.HMACSHA256Hex(primarySecret, []byte(primaryReadAppID))}
	primarySecret = ""
	defer func() { primaryAuth.AppKey = "" }()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	result, err := control.Run(ctx, []string{"sh", "-lc", `set -eu
python3 - "$1/data/collector/moox_collector.db" <<'PY'
import json
import sqlite3
import sys

db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
db.execute("PRAGMA query_only=ON")
row = db.execute("""
    SELECT target.c_dataset_id, instance.c_subject_id, instance.c_frequency,
           instance.c_series_tag, target.c_status, instance.c_target_data_time
    FROM t_collector_instance_write_targets AS target
    JOIN t_collector_task_instances AS instance
      ON instance.c_space_id = target.c_space_id AND instance.c_instance_id = target.c_instance_id
    WHERE target.c_space_id = ? AND target.c_task_id = ?
    ORDER BY CASE WHEN target.c_status = 'succeeded' THEN 0 ELSE 1 END,
             instance.c_id DESC
    LIMIT 1
""", ("crypto", "dasftksvjhj2jom4vhd0")).fetchone()
if row is None:
    raise SystemExit("task has no Collector write target to use for Storage readback")
print(json.dumps({"dataset_id": row[0], "subject_id": row[1], "frequency": row[2],
                  "series_tag": row[3], "target_status": row[4], "target_data_time": row[5]},
                 separators=(",", ":")))
PY
`, "moox-task-kline-readback-target", paths.ControlRoot}, nil)
	if err != nil {
		t.Fatalf("select a task Kline target for Storage readback: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	var target struct {
		DatasetID      string `json:"dataset_id"`
		SubjectID      string `json:"subject_id"`
		Frequency      string `json:"frequency"`
		SeriesTag      string `json:"series_tag"`
		TargetStatus   string `json:"target_status"`
		TargetDataTime string `json:"target_data_time"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &target); err != nil {
		t.Fatalf("decode task Kline target: %v\n%s", err, result.Stdout)
	}
	if target.DatasetID == "" || target.SubjectID == "" || target.Frequency == "" {
		t.Fatalf("task Kline target is incomplete: %+v", target)
	}
	t.Logf("task_kline_target dataset=%s subject=%s frequency=%s target_status=%s target_data_time=%s", target.DatasetID, target.SubjectID, target.Frequency, target.TargetStatus, target.TargetDataTime)
	reader := defaultDataKlineDeps().newReader(cfg)
	rsp, err := reader.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo:  primaryAuth,
		SpaceId:   "crypto",
		DatasetId: target.DatasetID,
		Selectors: []*storagepb.TimeSeriesSelector{{SpaceId: "crypto", DatasetId: target.DatasetID, SubjectId: target.SubjectID, Freq: target.Frequency, SeriesTag: &target.SeriesTag}},
		Order:     storagepb.SortOrder_SORT_ORDER_DESC,
		Page:      &storagepb.Page{Page: 1, Size: 10},
	})
	if err != nil {
		t.Fatalf("read task result Dataset %s from Primary: %v", target.DatasetID, redactDiagnosticSecrets(err.Error()))
	}
	if rsp == nil {
		t.Fatal("Primary returned an empty Kline response")
	}
	if err := checkStorageRetInfo("PrimaryStore", "ReadTimeSeriesRows", rsp); err != nil {
		t.Fatal(err)
	}
	if len(rsp.GetRows()) == 0 {
		t.Fatalf("task result Dataset %s has no Kline rows for subject %s", target.DatasetID, target.SubjectID)
	}
	for _, row := range rsp.GetRows() {
		key := row.GetKey()
		if key == nil {
			t.Fatal("Primary returned a row without a time-series key")
		}
		t.Logf("task_kline_row dataset=%s subject=%s frequency=%s data_time=%s field_count=%d", key.GetDatasetId(), key.GetSubjectId(), key.GetFreq(), key.GetDataTime(), len(row.GetFields()))
	}
	viewID := "view_dasftksvjhj2jom4vhd0_kline_1m"
	caPEM, err := os.ReadFile(setupdeploy.CAPath(snapshot.Manifest.ControlHost.Address))
	if err != nil {
		t.Fatalf("read Control CA for user-facing View query: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("Control CA contains no certificates")
	}
	userHTTP := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	controlURL := "https://" + snapshot.Manifest.ControlHost.Address + ":9527"
	postProto := func(path string, request, response proto.Message) error {
		body, marshalErr := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
		if marshalErr != nil {
			return marshalErr
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, controlURL+path, bytes.NewReader(body))
		if requestErr != nil {
			return requestErr
		}
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := userHTTP.Do(req)
		if doErr != nil {
			return doErr
		}
		defer resp.Body.Close()
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("user-facing endpoint returned HTTP %s", resp.Status)
		}
		return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(raw, response)
	}
	saltResponse := &adminpb.GetLoginSaltRsp{}
	if err := postProto("/api/admin/auth/GetLoginSalt", &adminpb.GetLoginSaltReq{Username: snapshot.Manifest.Admin.Username}, saltResponse); err != nil {
		t.Fatalf("get admin login salt: %v", redactDiagnosticSecrets(err.Error()))
	}
	if saltResponse.GetRetInfo() == nil || saltResponse.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || saltResponse.GetSalt() == "" || saltResponse.GetTimestamp() <= 0 {
		t.Fatal("admin login salt request was rejected")
	}
	encryptedPassword, err := security.Encrypt(snapshot.Manifest.Admin.Password, saltResponse.GetSalt()+fmt.Sprint(saltResponse.GetTimestamp()))
	if err != nil {
		t.Fatal("encrypt admin password for read-only View verification")
	}
	loginResponse := &adminpb.LoginRsp{}
	if err := postProto("/api/admin/auth/Login", &adminpb.LoginReq{
		Username: snapshot.Manifest.Admin.Username, PasswordHash: encryptedPassword,
		Salt: saltResponse.GetSalt(), Timestamp: saltResponse.GetTimestamp(),
		DeviceId: "moox-cli-task-kline-readback", UserAgent: "moox-cli/production-readback", ClientIp: "127.0.0.1",
	}, loginResponse); err != nil {
		t.Fatalf("admin login for read-only View verification: %v", redactDiagnosticSecrets(err.Error()))
	}
	if loginResponse.GetRetInfo() == nil || loginResponse.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || loginResponse.GetAccessToken() == "" || loginResponse.GetRequestSigningKey() == "" {
		t.Fatal("admin login for read-only View verification was rejected")
	}
	signedPost := func(path string, body []byte) ([]byte, error) {
		timestamp := time.Now().Unix()
		nonce, nonceErr := requestauth.NewNonce()
		if nonceErr != nil {
			return nil, nonceErr
		}
		signature, signErr := requestauth.Sign(loginResponse.GetRequestSigningKey(), requestauth.Material{
			Method: http.MethodPost, Path: path, Body: body,
			Headers: map[string]string{requestauth.HeaderSpaceID: "crypto"}, Timestamp: timestamp, Nonce: nonce,
		})
		if signErr != nil {
			return nil, signErr
		}
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, controlURL+path, bytes.NewReader(body))
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", loginResponse.GetAccessToken())
		req.Header.Set("X-Access-Token", loginResponse.GetAccessToken())
		req.Header.Set("X-Space-Id", "crypto")
		req.Header.Set("X-Moox-Timestamp", fmt.Sprint(timestamp))
		req.Header.Set("X-Moox-Nonce", nonce)
		req.Header.Set("X-Moox-Signature", signature)
		response, doErr := userHTTP.Do(req)
		if doErr != nil {
			return nil, doErr
		}
		defer response.Body.Close()
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("user-facing endpoint returned HTTP %s: %s", response.Status, redactDiagnosticSecrets(string(raw)))
		}
		return raw, nil
	}
	taskListBody, err := json.Marshal(map[string]any{"space_id": "crypto", "page": map[string]any{"page": 1, "size": 1000}})
	if err != nil {
		t.Fatal(err)
	}
	taskListRaw, err := signedPost("/api/admin/collectmgr/GetTaskList", taskListBody)
	if err != nil {
		t.Fatalf("query user-visible Collector task list: %v", redactDiagnosticSecrets(err.Error()))
	}
	taskList := &collectorpb.GetTaskListRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(taskListRaw, taskList); err != nil {
		t.Fatalf("decode user-visible Collector task list: %v", err)
	}
	if taskList.GetRetInfo() == nil || taskList.GetRetInfo().GetCode() != collectorpb.ErrorCode_SUCCESS {
		t.Fatalf("user-visible Collector task list rejected: %v", taskList.GetRetInfo())
	}
	var visibleTask *collectorpb.CollectionTask
	for _, task := range taskList.GetTasks() {
		if task.GetTaskId() == "dasftksvjhj2jom4vhd0" {
			visibleTask = task
			break
		}
	}
	if visibleTask == nil {
		t.Fatal("user-visible Collector task list does not contain the requested task")
	}
	if visibleTask.GetResult() == nil {
		t.Fatal("user-visible Collector task has no result summary")
	}
	t.Logf("task_results_api task=%s status=%s view=%s last_data_time=%s", visibleTask.GetTaskId(), visibleTask.GetResult().GetStatus(), visibleTask.GetResult().GetViewId(), visibleTask.GetResult().GetLastDataTime())
	viewResponse := &storagepb.QueryTimeSeriesRowsRsp{}
	viewRequest := map[string]any{
		"space_id": "crypto", "view_id": viewID,
		"selectors": []map[string]any{{
			"space_id": "crypto", "dataset_id": target.DatasetID, "subject_id": target.SubjectID,
			"freq": target.Frequency, "series_tag": target.SeriesTag,
		}},
		"sorts": []map[string]any{{"field_name": "data_time", "desc": true}},
		"page":  map[string]any{"page": 1, "size": 10}, "limit": 10, "total_mode": 0,
	}
	viewBody, err := json.Marshal(viewRequest)
	if err != nil {
		t.Fatal(err)
	}
	viewPath := "/api/admin/storage/QueryTimeSeriesRows"
	viewRaw, err := signedPost(viewPath, viewBody)
	if err != nil {
		t.Fatalf("query task result View: %v", redactDiagnosticSecrets(err.Error()))
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(viewRaw, viewResponse); err != nil {
		t.Fatalf("decode task result View response: %v", err)
	}
	if viewResponse == nil {
		t.Fatal("DataView returned an empty task-result response")
	}
	if err := checkStorageRetInfo("DataView", "QueryTimeSeriesRows", viewResponse); err != nil {
		t.Fatal(err)
	}
	if len(viewResponse.GetRows()) == 0 {
		t.Fatalf("task result View %s has no rows although Primary has Klines", viewID)
	}
	for _, row := range viewResponse.GetRows() {
		key := row.GetKey()
		if key == nil {
			t.Fatal("DataView returned a row without a time-series key")
		}
		t.Logf("task_view_row view=%s subject=%s frequency=%s data_time=%s field_count=%d", viewID, key.GetSubjectId(), key.GetFreq(), key.GetDataTime(), len(row.GetFields()))
	}
}

func TestManualProductionCollectorRuntimePurge(t *testing.T) {
	if os.Getenv("MOOX_RUN_PRODUCTION_COLLECTOR_RUNTIME_PURGE") != "1" {
		t.Skip("manual production Collector runtime cleanup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	dbPath := filepath.Join(paths.ControlRoot, "data/collector/moox_collector.db")
	schemaSQL, err := os.ReadFile(filepath.Join(root, "modules/collector/schema/collector.sql"))
	if err != nil {
		t.Fatal(err)
	}
	const rebuildScript = `import base64
import os
import sqlite3
import stat
import sys

path, schema_b64 = sys.argv[1:3]
fresh_path = path + ".new"
if os.path.exists(fresh_path):
    raise SystemExit("refusing to overwrite an existing Collector database rebuild file")
preserved = ["t_collector_tasks", "t_collector_task_tags", "t_collector_task_series"]
runtime = [
    "t_period_readiness_items", "t_period_readiness", "t_collector_fetch_batch_items",
    "t_collector_fetch_retry_items", "t_collector_instance_write_targets",
    "t_collector_fetch_batches", "t_collector_timer_period_batches",
    "t_collector_task_period_series", "t_collector_period_storage_states",
    "t_collector_task_instances", "t_collector_runs",
]

checkpoint = sqlite3.connect(path, timeout=60)
checkpoint.execute("PRAGMA busy_timeout=60000")
busy, log_frames, checkpointed_frames = checkpoint.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
if busy:
    raise SystemExit("old Collector database WAL checkpoint is busy")
checkpoint.close()

old = sqlite3.connect("file:" + path + "?mode=ro", uri=True, timeout=60)
old.execute("PRAGMA query_only=ON")
new = sqlite3.connect(fresh_path, timeout=60)
new.execute("PRAGMA foreign_keys=ON")
try:
    new.executescript(base64.b64decode(schema_b64).decode("utf-8"))
    copied = {}
    for table in preserved:
        columns = [row[1] for row in old.execute("PRAGMA table_info(" + table + ")")]
        if not columns:
            raise SystemExit("missing preserved task configuration table: " + table)
        column_sql = ",".join('"' + column + '"' for column in columns)
        order_by = {
            "t_collector_tasks": "c_id",
            "t_collector_task_tags": "c_space_id,c_task_id,c_tag_id",
            "t_collector_task_series": "c_id",
        }[table]
        rows = old.execute("SELECT " + column_sql + " FROM " + table + " ORDER BY " + order_by).fetchall()
        placeholders = ",".join("?" for _ in columns)
        new.executemany("INSERT INTO " + table + " (" + column_sql + ") VALUES (" + placeholders + ")", rows)
        copied[table] = len(rows)
        if rows != new.execute("SELECT " + column_sql + " FROM " + table + " ORDER BY " + order_by).fetchall():
            raise SystemExit("configuration rows differ after copy: " + table)

    old_tables = {row[0] for row in old.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    if "sqlite_sequence" in old_tables:
        sequences = old.execute("SELECT name,seq FROM sqlite_sequence WHERE name IN (?,?,?)", preserved).fetchall()
        for name, sequence in sequences:
            new.execute("DELETE FROM sqlite_sequence WHERE name=?", (name,))
            new.execute("INSERT INTO sqlite_sequence(name,seq) VALUES (?,?)", (name, sequence))
    new.commit()
    remaining = {table: new.execute("SELECT count(*) FROM " + table).fetchone()[0] for table in runtime}
    if any(remaining.values()):
        raise SystemExit("fresh Collector database contains runtime rows: " + repr(remaining))
    violations = list(new.execute("PRAGMA foreign_key_check"))
    if violations:
        raise SystemExit("fresh Collector database foreign-key violations: " + repr(violations[:5]))
    integrity = new.execute("PRAGMA integrity_check").fetchone()[0]
    if integrity != "ok":
        raise SystemExit("fresh Collector database integrity check failed: " + integrity)
    new.close()
    old.close()
    os.chmod(fresh_path, stat.S_IMODE(os.stat(path).st_mode))
    print("preserved_config_rows=" + repr(copied))
    print("runtime_rows_after=" + repr(remaining))
    print("fresh_database_integrity=" + integrity)
except BaseException:
    new.close()
    old.close()
    try:
        os.unlink(fresh_path)
    except FileNotFoundError:
        pass
    raise
`
	maintenanceScript := `set -euo pipefail
root="$1"
db="$2"
schema_b64="$3"
components="$root/config/components.env"
backup="$components.codex-runtime-maintenance"
db_backup="$db.pre-runtime-rebuild"
[[ -s "$components" && ! -e "$backup" ]]
[[ ! -e "$db_backup" && ! -e "$db.new" && ! -e "$db_backup-wal" && ! -e "$db_backup-shm" ]]
cp -p "$components" "$backup"
restart_required=0
restore() {
  status=$?
  trap - EXIT
  set +e
  if [[ -e "$db_backup" ]]; then
    MOOX_WITH_COLLECTOR=1 "$root/stop.sh" collector >/dev/null 2>&1
    mv -f "$db" "$db.failed-runtime-rebuild" 2>/dev/null || true
    rm -f "$db-wal" "$db-shm"
    mv -f "$db_backup" "$db"
    mv -f "$db_backup-wal" "$db-wal" 2>/dev/null || true
    mv -f "$db_backup-shm" "$db-shm" 2>/dev/null || true
  fi
  if [[ -e "$backup" ]]; then
    cp -p "$backup" "$components"
    rm -f "$backup"
  fi
  if [[ "$restart_required" == 1 ]]; then
    "$root/start.sh" collector
  fi
  exit "$status"
}
trap restore EXIT
python3 - "$components" <<'PY'
import os
import pathlib
import stat
import sys

path = pathlib.Path(sys.argv[1])
raw = path.read_text()
old = "MOOX_INSTALLED_WITH_COLLECTOR=1"
if raw.count(old) != 1:
    raise SystemExit("expected exactly one installed Collector component flag")
temp = path.with_name(path.name + ".maintenance.tmp")
temp.write_text(raw.replace(old, "MOOX_INSTALLED_WITH_COLLECTOR=0"))
os.chmod(temp, stat.S_IMODE(path.stat().st_mode))
os.replace(temp, path)
PY
restart_required=1
MOOX_WITH_COLLECTOR=1 "$root/stop.sh" collector
sleep 65
for _ in $(seq 1 30); do
  alive=0
  for proc in /proc/[0-9]*; do
    exe=$(readlink -f "$proc/exe" 2>/dev/null || true)
    case "$exe" in "$root/bin/moox-collector"|"$root/bin/moox-collector-subject") alive=1;; esac
  done
  [[ "$alive" == 0 ]] && break
  sleep 1
done
[[ "$alive" == 0 ]] || { echo 'Collector processes restarted during maintenance; refusing database cleanup' >&2; exit 1; }
printf 'collector_stopped=ok healthcheck_component_disabled=ok\n'
python3 - "$db" "$schema_b64" <<'PY'
` + rebuildScript + `
PY
mv "$db" "$db_backup"
if [[ -e "$db-wal" ]]; then mv "$db-wal" "$db_backup-wal"; fi
if [[ -e "$db-shm" ]]; then mv "$db-shm" "$db_backup-shm"; fi
mv "$db.new" "$db"
MOOX_WITH_COLLECTOR=1 "$root/start.sh" collector
healthy=0
for _ in $(seq 1 60); do
  if MOOX_WITH_COLLECTOR=1 "$root/healthcheck.sh" collector collector-subject >/dev/null 2>&1; then healthy=1; break; fi
  sleep 2
done
[[ "$healthy" == 1 ]] || { echo 'rebuilt Collector database did not become healthy' >&2; exit 1; }
restart_required=0
cp -p "$backup" "$components"
rm -f "$backup"
rm -f "$db_backup" "$db_backup-wal" "$db_backup-shm"
trap - EXIT
printf 'collector_runtime_rebuild=complete task_definitions_preserved=1 collector_restarted=1\n'
`
	schemaB64 := base64.StdEncoding.EncodeToString(schemaSQL)
	result, err := control.Run(ctx, []string{"bash", "-lc", maintenanceScript, "moox-collector-runtime-maintenance", paths.ControlRoot, dbPath, schemaB64}, nil)
	if err != nil {
		t.Fatalf("clear Collector runtime state while preserving task definitions: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	t.Log(result.Stdout)
}

func TestManualProductionSCFLogs(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_SCF_LOGS") != "1" {
		t.Skip("manual production diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: "ap-hongkong",
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := cls.NewClient(common.NewCredential(snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey), "ap-guangzhou", profile.NewClientProfile())
	if err != nil {
		t.Fatal(err)
	}
	scfProfile := profile.NewClientProfile()
	scfProfile.HttpProfile.Endpoint = "scf.tencentcloudapi.com"
	scfProfile.HttpProfile.ReqTimeout = 30
	scfClient, err := scf.NewClient(common.NewCredential(snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey), "ap-hongkong", scfProfile)
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"moox-crypto", "moox-crypto-ns2"} {
		functions, err := network.ListSCFFunctions(ctx, namespace, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(functions) == 0 {
			t.Fatalf("no active SCF functions found in %s", namespace)
		}
		function, err := network.GetSCFFunction(ctx, namespace, functions[0].FunctionName)
		if err != nil {
			t.Fatal(err)
		}
		functionRequest := scf.NewGetFunctionRequest()
		functionRequest.Namespace = common.StringPtr(namespace)
		functionRequest.FunctionName = common.StringPtr(function.FunctionName)
		functionResponse, err := scfClient.GetFunctionWithContext(ctx, functionRequest)
		if err != nil {
			t.Fatalf("read SCF log route for %s: %v", function.FunctionName, err)
		}
		if functionResponse == nil || functionResponse.Response == nil {
			t.Fatalf("SCF GetFunction returned no configuration for %s", function.FunctionName)
		}
		topicID := strings.TrimSpace(function.Environment["MOOX_CLS_TOPIC_ID"])
		if topicID == "" {
			t.Fatalf("SCF function %s has no MOOX_CLS_TOPIC_ID", function.FunctionName)
		}
		logsetID := strings.TrimSpace(function.Environment["MOOX_CLS_LOGSET_ID"])
		actualLogsetID := valueOrEmpty(functionResponse.Response.ClsLogsetId)
		actualTopicID := valueOrEmpty(functionResponse.Response.ClsTopicId)
		inlineCA := strings.TrimSpace(function.Environment["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"])
		t.Logf("scf_config namespace=%s function=%s package=%s mode=%s storage_target=%s eventbus_url=%s eventbus_credential_file=%s eventbus_ca_file=%s inline_ca_bytes=%d env_endpoint=%s env_logset=%s env_topic=%s actual_logset=%s actual_topic=%s", namespace, function.FunctionName,
			function.Environment["MOOX_CODE_PACKAGE_ID"], function.Environment["MOOX_MARKET_FETCH_MODE"], function.Environment["MOOX_STORAGE_RPC_GATEWAY_TARGET"], function.Environment["MOOX_EVENTBUS_NATS_URL"],
			function.Environment["MOOX_EVENTBUS_CREDENTIAL_FILE"], function.Environment["MOOX_EVENTBUS_NATS_TLS_CA_FILE"], len(inlineCA),
			function.Environment["MOOX_CLS_ENDPOINT"], logsetID, topicID, actualLogsetID, actualTopicID)
		if logsetID != "" {
			topicsRequest := cls.NewDescribeTopicsRequest()
			topicsRequest.Filters = []*cls.Filter{{Key: common.StringPtr("logsetId"), Values: []*string{common.StringPtr(logsetID)}}}
			topicsRequest.Limit = common.Int64Ptr(100)
			topicsResponse, topicsErr := client.DescribeTopicsWithContext(ctx, topicsRequest)
			if topicsErr != nil {
				t.Logf("cls_topic_inventory_error namespace=%s err=%v", namespace, topicsErr)
			} else if topicsResponse != nil && topicsResponse.Response != nil {
				for _, topic := range topicsResponse.Response.Topics {
					if topic != nil {
						t.Logf("cls_topic namespace=%s id=%s name=%s", namespace, valueOrEmpty(topic.TopicId), valueOrEmpty(topic.TopicName))
					}
				}
			}
		}
		request := cls.NewSearchLogRequest()
		request.From = common.Int64Ptr(time.Now().Add(-30 * time.Minute).UnixMilli())
		request.To = common.Int64Ptr(time.Now().UnixMilli())
		request.QueryString = common.StringPtr("*")
		if actualTopicID != "" {
			request.TopicId = common.StringPtr(actualTopicID)
		} else {
			request.TopicId = common.StringPtr(topicID)
		}
		request.Sort = common.StringPtr("desc")
		request.Limit = common.Int64Ptr(1000)
		response, err := client.SearchLogWithContext(ctx, request)
		if err != nil {
			t.Logf("scf_log_search_error namespace=%s err=%v", namespace, err)
			continue
		}
		if response == nil || response.Response == nil {
			t.Fatalf("CLS returned an empty response for %s", namespace)
		}
		t.Logf("scf_log_probe function=%s namespace=%s region=%s logs=%d", function.FunctionName, function.Namespace, function.Region, len(response.Response.Results))
		for index, item := range response.Response.Results {
			if index >= 40 || item == nil || item.LogJson == nil {
				break
			}
			line := strings.TrimSpace(*item.LogJson)
			lower := strings.ToLower(line)
			if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "storage") || strings.Contains(lower, "completion") || strings.Contains(lower, "market fetch") {
				t.Logf("scf_log time_ms=%d %s", valueOrZero(item.Time), redactDiagnosticSecrets(line, snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey))
			}
		}
	}
}

func TestManualProductionSCFCanary(t *testing.T) {
	if os.Getenv("MOOX_RUN_PRODUCTION_SCF_CANARY") != "1" {
		t.Skip("manual production write canary")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, cleanup, err := newCollectorTimerInventoryClient(ctx, collectorTimerInventoryOptions{
		ControlURL: "https://" + snapshot.Manifest.ControlHost.Address + ":11001",
		File:       filepath.Join(root, "moox.toml"),
		SpaceID:    "crypto",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		CloudAccountID: "tencent-scf", Region: "ap-hongkong", NodeType: "scf-event", BizType: "market_fetcher", PageSize: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("no crypto SCF node is available for the canary")
	}
	var selected adminclient.CloudNode
	for _, node := range nodes {
		if node.Namespace == "moox-crypto" && strings.EqualFold(node.TriggerType, "invoke") && collectorProbeNodeEligible(node) {
			selected = node
			break
		}
	}
	if selected.NodeID == "" {
		t.Fatal("no enabled invoke node is available for the canary")
	}
	function, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: "ap-hongkong",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := function.GetSCFFunction(ctx, selected.Namespace, selected.FunctionName)
	if err != nil {
		t.Fatal(err)
	}
	storageTarget := strings.TrimSpace(info.Environment["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
	if storageTarget == "" {
		t.Fatal("selected SCF function has no Storage target")
	}
	canaryID := fmt.Sprintf("diagnostic-canary-%d", time.Now().UnixNano())
	opts := collectorPublishOptions{collectorPackageOptions: collectorPackageOptions{SpaceID: "crypto"}, SpaceID: "crypto", Region: "ap-hongkong", StorageRPCGatewayTarget: storageTarget}
	response, invokeErr := client.InvokeFunction(ctx, selected.NodeID, collectorSCFCanaryEvent(opts, selected.NodeID, canaryID))
	if invokeErr != nil {
		t.Fatalf("scf_canary function=%s namespace=%s storage_target=%s error=%v", selected.FunctionName, selected.Namespace, storageTarget, redactDiagnosticSecrets(invokeErr.Error(), snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey))
	}
	raw, _ := json.Marshal(response)
	t.Logf("scf_canary function=%s namespace=%s storage_target=%s result=%s", selected.FunctionName, selected.Namespace, storageTarget, redactDiagnosticSecrets(string(raw), snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey))
}

func TestManualProductionScheduledBatchReplay(t *testing.T) {
	if os.Getenv("MOOX_RUN_PRODUCTION_SCHEDULED_BATCH_REPLAY") != "1" {
		t.Skip("manual production write and completion diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	api, cleanup, err := newCollectorTimerInventoryClient(ctx, collectorTimerInventoryOptions{
		ControlURL: "https://" + snapshot.Manifest.ControlHost.Address + ":11001",
		File:       filepath.Join(root, "moox.toml"),
		SpaceID:    "crypto",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	paths := snapshot.Manifest.Paths.Resolved()
	result, err := control.Run(ctx, []string{"sh", "-lc", `set -eu
python3 - "$1/data/collector/moox_collector.db" <<'PY'
import json
import sqlite3
import sys

db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
db.execute("PRAGMA query_only=ON")
row = db.execute("""
    SELECT c_request_json, c_node_id, c_function_name, c_batch_id
    FROM t_collector_fetch_batches
    WHERE c_space_id = ? AND c_status = 'dispatched' AND c_frequency = '1m'
    ORDER BY c_id DESC LIMIT 1
""", ("crypto",)).fetchone()
if row is None:
    raise SystemExit("no dispatched crypto spot Kline batch found")
request = json.loads(row[0])
print(json.dumps({"request": request, "node_id": row[1], "function_name": row[2], "batch_id": row[3]}, separators=(",", ":")))
PY
`, "moox-scheduled-batch-replay", paths.ControlRoot}, nil)
	if err != nil {
		t.Fatalf("read one dispatched production batch: %v\nstdout:\n%s\nstderr:\n%s", err, result.Stdout, result.Stderr)
	}
	var stored struct {
		Request      map[string]any `json:"request"`
		NodeID       string         `json:"node_id"`
		FunctionName string         `json:"function_name"`
		BatchID      string         `json:"batch_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &stored); err != nil {
		t.Fatalf("decode dispatched batch readback: %v\n%s", err, result.Stdout)
	}
	if stored.BatchID == "" || stored.NodeID == "" || len(stored.Request) == 0 {
		t.Fatalf("dispatched batch is incomplete: batch=%s node=%s request_fields=%d", stored.BatchID, stored.NodeID, len(stored.Request))
	}
	nodes, err := api.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		CloudAccountID: "tencent-scf", Region: "ap-hongkong", NodeType: "scf-event", BizType: "market_fetcher", PageSize: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	var node *adminclient.CloudNode
	for index := range nodes {
		if nodes[index].NodeID == stored.NodeID && !nodes[index].IsDeleted {
			node = &nodes[index]
			break
		}
	}
	if node == nil {
		t.Fatalf("dispatched batch node %s is not active in CloudNode catalog", stored.NodeID)
	}
	network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: "ap-hongkong",
	})
	if err != nil {
		t.Fatal(err)
	}
	function, err := network.GetSCFFunction(ctx, node.Namespace, stored.FunctionName)
	if err != nil {
		t.Fatal(err)
	}
	storageTarget := strings.TrimSpace(function.Environment["MOOX_STORAGE_RPC_GATEWAY_TARGET"])
	if storageTarget == "" {
		t.Fatal("dispatched batch SCF has no Storage target")
	}
	event := map[string]any{
		"action": "market_fetch", "source": "collector_scheduler", "request_id": stored.BatchID,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "storage_rpc_gateway_target": storageTarget, "data": stored.Request,
	}
	response, err := api.InvokeFunction(ctx, stored.NodeID, event)
	if err != nil {
		t.Fatalf("synchronous replay of dispatched batch %s failed: %v", stored.BatchID, redactDiagnosticSecrets(err.Error(), snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey))
	}
	raw, _ := json.Marshal(response)
	t.Logf("scheduled_batch_replay batch=%s node=%s function=%s storage_target=%s result=%s", stored.BatchID, stored.NodeID, stored.FunctionName, storageTarget, redactDiagnosticSecrets(string(raw), snapshot.Manifest.TencentCloud.SecretID, snapshot.Manifest.TencentCloud.SecretKey))
}

func TestManualProductionCompletionConsumer(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_COMPLETION_CONSUMER") != "1" {
		t.Skip("manual production diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	credential, _, err := collectorEventBusCredentialMaterial(collectorPublishOptions{EventBusCredentialFile: "~/.config/moox/eventbus/market-fetch-publisher.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: "ap-hongkong",
	})
	if err != nil {
		t.Fatal(err)
	}
	functions, err := network.ListSCFFunctions(ctx, "moox-crypto", nil)
	if err != nil || len(functions) == 0 {
		t.Fatalf("list SCF functions for EventBus CA: count=%d err=%v", len(functions), err)
	}
	scfFunction, err := network.GetSCFFunction(ctx, "moox-crypto", functions[0].FunctionName)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(scfFunction.Environment["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"]))
	if err != nil || len(caPEM) == 0 {
		t.Fatalf("decode SCF EventBus CA: bytes=%d err=%v", len(caPEM), err)
	}
	credential.Username = scfFunction.Environment["MOOX_EVENTBUS_NATS_USERNAME"]
	credential.Password = scfFunction.Environment["MOOX_EVENTBUS_NATS_PASSWORD"]
	credential, err = preflightCollectorSCFEventBusCredential(credential, caPEM, snapshot.Manifest.EventBus)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("invalid EventBus CA")
	}
	connection, err := nats.Connect(strings.Join(credential.URLs, ","), nats.UserInfo(credential.Username, credential.Password),
		nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}), nats.TLSHandshakeFirst(), nats.Timeout(5*time.Second), nats.Name("moox-completion-consumer-diagnostic"))
	if err != nil {
		t.Fatalf("connect to EventBus: %v", err)
	}
	defer connection.Close()
	js, err := connection.JetStream()
	if err != nil {
		t.Fatalf("create JetStream context: %v", err)
	}
	consumerName := "collector-market-fetch-completion-v1-crypto"
	info, err := js.ConsumerInfo(events.MarketFetchBatchCompleted.Stream(), consumerName, nats.Context(ctx))
	if err != nil {
		t.Fatalf("read market-fetch completion consumer: %v", err)
	}
	lastDelivered := ""
	if info.Delivered.Last != nil {
		lastDelivered = info.Delivered.Last.UTC().Format(time.RFC3339Nano)
	}
	lastAck := ""
	if info.AckFloor.Last != nil {
		lastAck = info.AckFloor.Last.UTC().Format(time.RFC3339Nano)
	}
	t.Logf("completion_consumer stream=%s name=%s pending=%d ack_pending=%d redelivered=%d delivered_stream=%d ack_floor_stream=%d last_delivered=%s last_ack=%s filter=%s",
		info.Stream, info.Name, info.NumPending, info.NumAckPending, info.NumRedelivered, info.Delivered.Stream, info.AckFloor.Stream, lastDelivered, lastAck, info.Config.FilterSubject)
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func redactDiagnosticSecrets(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	return value
}

func TestManualProductionStorageServices(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_STORAGE_STATUS") != "1" {
		t.Skip("manual production diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	paths := snapshot.Manifest.Paths.Resolved()
	script := "set -eu\n" +
		"root=\"$1\"\n" +
		"printf 'moox_processes:\\n'\n" +
		"ps -eo comm= | awk '/^moox-/' | sort | uniq -c\n" +
		"printf 'service_status:\\n'\n" +
		"\"$root/healthcheck.sh\" storage-primary storage-view storage-node storage-access\n"
	hosts := []struct {
		role string
		host setupconfig.Host
	}{
		{role: "storage", host: snapshot.Manifest.StorageHost},
		{role: "view", host: snapshot.Manifest.ViewHost},
	}
	seen := make(map[string]struct{})
	for _, item := range hosts {
		if item.host.Address == "" {
			t.Logf("%s host is not configured", item.role)
			continue
		}
		if _, exists := seen[item.host.Address]; exists {
			continue
		}
		seen[item.host.Address] = struct{}{}
		remote, err := dialSetupHost(ctx, item.host)
		if err != nil {
			t.Fatalf("connect %s host: %v", item.role, err)
		}
		result, err := remote.Run(ctx, []string{"sh", "-lc", script, "moox-storage-status", paths.StorageRoot}, nil)
		_ = remote.Close()
		if err != nil {
			t.Logf("%s remote output: %s\n%s", item.role, result.Stdout, result.Stderr)
			t.Fatalf("read %s status: %v", item.role, err)
		}
		t.Logf("%s host status:\n%s", item.role, result.Stdout)
	}
}

func TestManualProductionAccessTargetHost(t *testing.T) {
	if os.Getenv("MOOX_READ_PRODUCTION_ACCESS_TARGET_HOST") != "1" {
		t.Skip("manual production diagnostic")
	}
	root, err := filepath.Abs("../../../../")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, host := range snapshot.Manifest.OtherHosts {
		if host.Name != "compute-1" {
			continue
		}
		remote, err := dialSetupHost(ctx, host)
		if err != nil {
			t.Fatalf("connect %s host: %v", host.Name, err)
		}
		result, err := remote.Run(ctx, []string{"sh", "-lc", `set -u
	root="$1"
	printf 'host=%s\n' "$2"
	if test -x "$root/start.sh"; then
	  printf 'lifecycle=present\n'
	else
	  printf 'lifecycle=missing root=%s\n' "$root"
	fi
	printf 'storage_access_env:\n'
	awk -F= '/^MOOX_STORAGE_ACCESS_UPSTREAM_TARGET(_NODE)?=/{print}' "$root/secrets/storage-access.env" 2>/dev/null || true
	printf 'access_env_keys:\n'
	awk -F= '{print $1}' "$root/secrets/access.env" 2>/dev/null | sort || true
	stat -c 'access_env_mode=%a' "$root/secrets/access.env" 2>/dev/null || true
	printf 'candidate_env_files:\n'
	find /home/ubuntu/moox -maxdepth 5 -type f -path '*/secrets/storage-access.env' -print 2>/dev/null || true
	printf 'gateway_material_files:\n'
	for file in gateway-control.env gateway-service.env gateway-moox-cli.env storage-access.env; do
	  test -s "$root/secrets/$file" && printf '%s\n' "$file" || true
	done
	printf 'processes:\n'
	ps -eo pid=,comm=,args= | grep -E 'moox-(storage-access|gateway)' | grep -v grep || true
	pid=$(pgrep -x moox-storage-ac | head -1 || true)
	if test -n "$pid"; then
	  ps -o pid=,ppid=,args= -p "$pid" || true
	  printf 'storage_access_cwd=%s\n' "$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)"
	  printf 'storage_access_env:\n'
	  tr '\0' '\n' <"/proc/$pid/environ" | grep -E '^MOOX_STORAGE_ACCESS_(UPSTREAM_TARGET(_NODE)?|TARGET_NODE|ALLOWED_CALLERS)=' || true
	fi
	printf 'root_files:\n'
	find "$root" -maxdepth 2 -type f -printf '%P\n' 2>/dev/null | sort | head -80
	for script in start.sh stop.sh; do
	  if test -r "$root/$script"; then
	    printf '%s_contents:\n' "$script"
	    sed -n '1,100p' "$root/$script"
	  fi
	done
	printf 'listeners:\n'
	ss -ltnp 2>/dev/null | grep -E ':(11003|12004)\b' || true
	status=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 2 http://127.0.0.1:11014/readyz || true)
	printf 'storage_access_readyz=%s\n' "${status:-unreachable}"
	`, "moox-access-target-host", "/home/ubuntu/moox/storage-access", host.Name}, nil)
		_ = remote.Close()
		if err != nil {
			t.Fatalf("inspect %s host: %v\n%s\n%s", host.Name, err, result.Stdout, result.Stderr)
		}
		t.Logf("%s host inventory:\n%s", host.Name, result.Stdout)
		return
	}
	t.Fatal("compute-1 host is not configured")
}
