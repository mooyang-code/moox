package command

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "testing"
    "time"

    setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
    "github.com/stretchr/testify/require"
)

func TestLiveRefreshStorageGatewayDeployment(t *testing.T) {
    if os.Getenv("MOOX_LIVE_GATEWAY_FIX") != "1" { t.Skip("opt-in") }
    root, err := filepath.Abs("../../../..")
    require.NoError(t, err)
    snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
    require.NoError(t, err)
    defer clearSetupSecrets(snapshot)
    ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()
    control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
    require.NoError(t, err)
    defer control.Close()
    eventbusURL := fmt.Sprintf("tls://%s:%d", snapshot.Manifest.EventBus.Host, snapshot.Manifest.EventBus.Port)
    result, err := control.Run(ctx, []string{"bash", "-lc", `set -euo pipefail
root=/data/moox/prod
"$root/bin/moox-admin-cli" service-deployments import \
  --db-path "$root/data/admin.db" --file "$root/config/setup/service-deployments.yaml" \
  --node-id storage --public-host "$1" --eventbus-nats-url "$2" \
  --only-services storage-primary,storage-view
MOOX_WITH_GATEWAY=1 "$root/stop.sh" gateway >/dev/null 2>&1 || true
MOOX_WITH_GATEWAY=1 "$root/start.sh" gateway >/dev/null
for i in $(seq 1 20); do
  if curl -fsS http://127.0.0.1:11012/readyz >/dev/null; then break; fi
  sleep 1
done
curl -fsS http://127.0.0.1:11012/readyz
python3 - <<'PY'
import sqlite3, json
con=sqlite3.connect('/data/moox/prod/data/admin.db'); con.row_factory=sqlite3.Row
r=con.execute("select c_extra_config,c_mtime from t_service_deployments where c_node_id='storage' and c_service_name='storage-primary'").fetchone()
cfg=json.loads(r['c_extra_config']); methods=[]
for route in cfg.get('gateway_routes',[]):
    if route.get('service_path')=='trpc.moox.storage.PrimaryStore': methods += route.get('gateway_methods',[])
print(json.dumps({'mtime':r['c_mtime'],'has_ensure':'EnsureDatasetPeriod' in methods,'has_commit':'CommitTimeSeriesBatch' in methods}, ensure_ascii=False))
PY`, "moox-storage-route-fix", snapshot.Manifest.StorageHost.Address, eventbusURL}, nil)
    t.Logf("%s", result.Stdout)
    if result.Stderr != "" { t.Logf("stderr=%s", result.Stderr) }
    require.NoError(t, err)
}
