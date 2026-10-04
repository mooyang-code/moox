package command

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

func TestLiveGatewayRouteDiagnostic(t *testing.T) {
	if os.Getenv("MOOX_LIVE_GATEWAY_DIAG") != "1" {
		t.Skip("opt-in")
	}
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	require.NoError(t, err)
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	require.NoError(t, err)
	defer control.Close()
	result, err := control.Run(ctx, []string{"bash", "-lc", `python3 - <<'PY'
import sqlite3, json, os
p='/data/moox/prod/data/admin.db'
con=sqlite3.connect(p); con.row_factory=sqlite3.Row
for r in con.execute("select c_node_id,c_service_name,c_gateway_service_id,c_gateway_enabled,c_status,c_extra_config,c_mtime from t_service_deployments where c_service_name='storage-primary'"):
    print('DEPLOYMENT', json.dumps(dict(r), ensure_ascii=False))
for lp in ['/data/moox/prod/logs/gateway/stdout.log','/data/moox/prod/gateway/logs/stdout.log']:
    if not os.path.exists(lp): continue
    print('GATEWAY_LOG', lp)
    lines=open(lp,errors='replace').read().splitlines()[-4000:]
    for line in [x for x in lines if any(k in x for k in ['route','snapshot','EnsureDatasetPeriod','refresh'])][-80:]: print(line[:1200])
PY`}, nil)
	t.Logf("%s", result.Stdout)
	if result.Stderr != "" {
		t.Logf("stderr=%s", result.Stderr)
	}
	require.NoError(t, err)
}
