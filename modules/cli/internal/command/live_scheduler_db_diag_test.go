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

func TestLiveSchedulerDBDiagnostic(t *testing.T) {
	if os.Getenv("MOOX_LIVE_SCHED_DB_DIAG") != "1" {
		t.Skip("opt-in")
	}
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	require.NoError(t, err)
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	require.NoError(t, err)
	defer control.Close()
	result, err := control.Run(ctx, []string{"bash", "-lc", `python3 - <<'PY'
import sqlite3,time,json
p='/data/moox/prod/data/collector/moox_collector.db'
con=sqlite3.connect('file:'+p+'?mode=ro', uri=True)
con.row_factory=sqlite3.Row

def timed(name, sql, args=()):
    st=time.perf_counter(); rows=list(con.execute(sql,args)); dt=(time.perf_counter()-st)*1000
    print(name, 'ms=%.2f'%dt, 'rows='+str(len(rows)), json.dumps([dict(r) for r in rows[:5]],ensure_ascii=False))
    return rows

for table in ['t_collector_runs','t_collector_task_instances','t_collector_instance_write_targets','t_collector_fetch_batches','t_collector_fetch_batch_items','t_collector_fetch_retry_items','t_collector_tasks']:
    print('INDEXES',table,list(con.execute("pragma index_list(%s)"%table)))

timed('task_states', "select c_enabled,count(*) n from t_collector_tasks where c_space_id='crypto' group by c_enabled")
timed('active_runs', "select c_run_id,c_status,c_ctime from t_collector_runs where c_space_id='crypto' and c_status in ('planned','active') order by c_id asc limit 500")
runs=timed('active_run_ids', "select c_run_id from t_collector_runs where c_space_id='crypto' and c_status in ('planned','active') order by c_id asc limit 1")
for rr in runs:
    run=rr['c_run_id']
    timed('instances:'+run, "select count(*) n,sum(case when c_last_exec_status='success' then 1 else 0 end) s,sum(case when c_last_exec_status='failed' then 1 else 0 end) f from t_collector_task_instances where c_space_id=? and c_run_id=?",('crypto',run))
    timed('targets:'+run, "select count(*) n from t_collector_instance_write_targets targets join t_collector_task_instances instances on instances.c_space_id=targets.c_space_id and instances.c_instance_id=targets.c_instance_id where instances.c_space_id=? and instances.c_run_id=?",('crypto',run))
    timed('batches:'+run, "select count(*) n from t_collector_fetch_batches batches where batches.c_space_id=? and exists (select 1 from t_collector_fetch_batch_items items join t_collector_task_instances instances on instances.c_space_id=items.c_space_id and instances.c_instance_id=items.c_instance_id where items.c_space_id=batches.c_space_id and items.c_batch_id=batches.c_batch_id and instances.c_run_id=?)",('crypto',run))
    timed('retries:'+run, "select count(*) n from t_collector_fetch_retry_items retries join t_collector_task_instances instances on instances.c_space_id=retries.c_space_id and instances.c_instance_id=retries.c_instance_id where retries.c_space_id=? and instances.c_run_id=? and retries.c_status in ('pending','dispatched')",('crypto',run))
    timed('targets_in:'+run, "select count(*) n,sum(case when lower(c_status) in ('succeeded','success','completed') then 1 else 0 end) s from t_collector_instance_write_targets where c_space_id=? and c_instance_id in (select c_instance_id from t_collector_task_instances where c_space_id=? and c_run_id=?)",('crypto','crypto',run))
    timed('batches_in:'+run, "select count(*) n,sum(case when c_status not in ('succeeded','partial_failed','failed','timed_out') then 1 else 0 end) active from t_collector_fetch_batches where c_space_id=? and c_batch_id in (select distinct c_batch_id from t_collector_fetch_batch_items where c_space_id=? and c_instance_id in (select c_instance_id from t_collector_task_instances where c_space_id=? and c_run_id=?))",('crypto','crypto','crypto',run))

print('disabled_targets_safe_count SKIPPED')
PY`}, nil)
	t.Logf("%s", result.Stdout)
	if result.Stderr != "" {
		t.Logf("stderr=%s", result.Stderr)
	}
	require.NoError(t, err)
}
