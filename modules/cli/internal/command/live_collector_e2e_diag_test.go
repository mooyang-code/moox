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

func TestLiveCollectorE2EDiagnostic(t *testing.T) {
	if os.Getenv("MOOX_LIVE_COLLECTOR_E2E_DIAG") != "1" {
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
p='/data/moox/prod/data/collector/moox_collector.db'
con=sqlite3.connect(p); con.row_factory=sqlite3.Row
print('RUNS')
for r in con.execute("select c_run_id,c_run_type,c_frequency,c_status,c_target_time,c_error_summary,c_ctime,c_mtime from t_collector_runs order by c_ctime desc limit 12"):
    print(json.dumps(dict(r), ensure_ascii=False))
for table in ['t_collector_task_instances','t_collector_instance_write_targets','t_collector_fetch_batches','t_collector_fetch_retry_items']:
    try:
        print('COUNT', table, con.execute('select count(*) from '+table).fetchone()[0])
        cols=[x[1] for x in con.execute('pragma table_info('+table+')')]
        wanted=[c for c in cols if c in ('c_instance_id','c_task_id','c_run_id','c_status','c_provider','c_source','c_market_type','c_batch_id','c_dataset_id','c_frequency','c_period_time','c_ctime','c_mtime','c_error_message','c_last_error')]
        if wanted:
            q='select '+','.join(wanted)+' from '+table+' order by c_ctime desc limit 8' if 'c_ctime' in cols else 'select '+','.join(wanted)+' from '+table+' limit 8'
            for r in con.execute(q): print('ROW', table, json.dumps(dict(r), ensure_ascii=False))
    except Exception as e: print('ERR',table,type(e).__name__,e)
print('LOGS')
for lp in ['/data/moox/prod/logs/collector/stdout.log','/data/moox/prod/collector/logs/stdout.log']:
    if not os.path.exists(lp): continue
    lines=open(lp,errors='replace').read().splitlines()[-3000:]
    for line in [x for x in lines if any(k in x for k in ['invoke scheduler','market fetch','TaskInstance','task instance','completion','timer reconciliation'])][-40:]:
        print(line[:800])
PY`}, nil)
	t.Logf("%s", result.Stdout)
	if result.Stderr != "" {
		t.Logf("stderr=%s", result.Stderr)
	}
	require.NoError(t, err)
}
