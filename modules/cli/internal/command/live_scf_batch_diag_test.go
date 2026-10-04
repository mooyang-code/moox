package command

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
)

func TestLiveSCFBatchDiagnostic(t *testing.T) {
	if os.Getenv("MOOX_LIVE_SCF_DIAG") != "1" {
		t.Skip("opt-in")
	}
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	require.NoError(t, err)
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	network, err := tencent.NewNetworkClient(tencent.ClientOptions{SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: "ap-hongkong"})
	require.NoError(t, err)
	functions, err := network.ListSCFFunctions(ctx, "moox-crypto-ns2", nil)
	require.NoError(t, err)
	t.Logf("remote ns2 count=%d", len(functions))
	for _, fn := range functions {
		t.Logf("remote function name=%s status=%s", fn.FunctionName, fn.Status)
	}
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	require.NoError(t, err)
	defer control.Close()
	result, err := control.Run(ctx, []string{"bash", "-lc", `python3 - <<'PY'
import sqlite3, json
p='/data/moox/prod/data/cloudnode/moox_cloudnode.db'
con=sqlite3.connect(p); con.row_factory=sqlite3.Row
print('BATCHES')
for r in con.execute("select b.c_job_id,b.c_operation,b.c_status,b.c_total_count,(select count(*) from t_cloud_node_batch_items i where i.c_job_id=b.c_job_id and i.c_space_id=b.c_space_id and i.c_status='success') as success_count,(select count(*) from t_cloud_node_batch_items i where i.c_job_id=b.c_job_id and i.c_space_id=b.c_space_id and i.c_status='failed') as failed_count,(select count(*) from t_cloud_node_batch_items i where i.c_job_id=b.c_job_id and i.c_space_id=b.c_space_id and i.c_status='running') as running_count,(select count(*) from t_cloud_node_batch_items i where i.c_job_id=b.c_job_id and i.c_space_id=b.c_space_id and i.c_status='pending') as pending_count,b.c_ctime,b.c_mtime from t_cloud_node_batches b order by b.c_ctime desc limit 8"):
    print(json.dumps(dict(r), ensure_ascii=False))
print('FAILED_ITEMS')
for r in con.execute("select c_item_index,c_node_id,c_status,c_error_message,c_result_summary from t_cloud_node_batch_items where c_job_id=(select c_job_id from t_cloud_node_batches order by c_ctime desc limit 1) and c_status='failed' order by c_item_index"):
    print(json.dumps(dict(r), ensure_ascii=False))
print('NODES')
for r in con.execute("select c_namespace,count(*) as n,min(c_node_id),max(c_node_id),min(c_package_version),max(c_package_version) from t_cloud_nodes where c_is_deleted=0 and c_space_id='crypto' group by c_namespace order by c_namespace"):
    print(json.dumps(dict(r), ensure_ascii=False))
print('LEGACY_CATALOG')
for r in con.execute("select c_node_id,c_namespace,c_trigger_type,c_is_deleted,c_package_version,c_metadata from t_cloud_nodes where c_space_id='crypto' and c_node_id like 'moox-fetcher-crypto-binance-ap-hongkong-%' order by c_node_id"):
    d=dict(r); d.pop('c_metadata', None); print(json.dumps(d, ensure_ascii=False))
print('RECENT_ERRORS')
try:
    text=open('/data/moox/prod/logs/cloudnode/stdout.log', errors='replace').read().splitlines()
    for line in [x for x in text[-4000:] if 'failed' in x.lower() or 'error' in x.lower() or 'AccountInsufficient' in x][-20:]: print(line[:600])
except Exception as e: print(e)
PY`}, nil)
	t.Logf("%s", result.Stdout)
	if result.Stderr != "" {
		t.Logf("stderr=%s", result.Stderr)
	}
	require.NoError(t, err)
}
