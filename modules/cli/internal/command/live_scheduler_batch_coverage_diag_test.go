package command
import("context";"os";"path/filepath";"testing";"time"; setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config";"github.com/stretchr/testify/require")
func TestLiveSchedulerBatchCoverageDiagnostic(t *testing.T){
 if os.Getenv("MOOX_LIVE_BATCH_COVERAGE_DIAG")!="1"{t.Skip("opt-in")}
 root,err:=filepath.Abs("../../../..");require.NoError(t,err); snapshot,err:=setupconfig.Load(filepath.Join(root,"moox.toml"),root);require.NoError(t,err);defer clearSetupSecrets(snapshot)
 ctx,cancel:=context.WithTimeout(context.Background(),30*time.Second);defer cancel(); control,err:=dialSetupHost(ctx,snapshot.Manifest.ControlHost);require.NoError(t,err);defer control.Close()
 result,err:=control.Run(ctx,[]string{"bash","-lc",`python3 - <<'PY'
import sqlite3,json
con=sqlite3.connect('/data/moox/prod/data/collector/moox_collector.db');con.row_factory=sqlite3.Row
runs=[r[0] for r in con.execute("select c_run_id from t_collector_runs where c_space_id='crypto' order by c_ctime desc limit 4")]
for rid in runs:
 print('RUN',rid)
 q='''select ti.c_frequency,b.c_status,count(distinct b.c_batch_id) batches,count(distinct bi.c_instance_id) items from t_collector_fetch_batches b join t_collector_fetch_batch_items bi on bi.c_space_id=b.c_space_id and bi.c_batch_id=b.c_batch_id join t_collector_task_instances ti on ti.c_space_id=bi.c_space_id and ti.c_instance_id=bi.c_instance_id where ti.c_space_id='crypto' and ti.c_run_id=? group by ti.c_frequency,b.c_status order by ti.c_frequency,b.c_status'''
 print('BATCH', [dict(x) for x in con.execute(q,(rid,))])
 print('INST', [dict(x) for x in con.execute("select c_frequency,c_last_exec_status,count(*) n from t_collector_task_instances where c_space_id='crypto' and c_run_id=? group by c_frequency,c_last_exec_status order by c_frequency,c_last_exec_status",(rid,))])
PY`},nil);t.Logf("%s",result.Stdout);if result.Stderr!=""{t.Logf("stderr=%s",result.Stderr)};require.NoError(t,err)
}
