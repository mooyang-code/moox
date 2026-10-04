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

func TestLiveSchedulerPostfixDiagnostic(t *testing.T) {
	if os.Getenv("MOOX_LIVE_SCHEDULER_POSTFIX_DIAG") != "1" {
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
import sqlite3,json,hashlib,os
p='/data/moox/prod/data/collector/moox_collector.db'; con=sqlite3.connect(p); con.row_factory=sqlite3.Row
print('OPEN', [dict(r) for r in con.execute("select c_status,count(*) n from t_collector_runs where c_space_id='crypto' and c_status in ('planned','active') group by c_status")])
for r in con.execute("select c_run_id,c_status,c_target_time,c_error_summary,c_ctime,c_mtime from t_collector_runs where c_space_id='crypto' order by c_ctime desc limit 10"):
 d=dict(r); rid=d['c_run_id']
 d['freqs']=[dict(x) for x in con.execute("select c_frequency,count(*) n,sum(c_last_exec_status=2) success,sum(c_last_exec_status=3) failed from t_collector_task_instances where c_space_id='crypto' and c_run_id=? group by c_frequency order by c_frequency",(rid,))]
 d['targets']=[dict(x) for x in con.execute("select ti.c_frequency,wt.c_status,count(*) n from t_collector_instance_write_targets wt join t_collector_task_instances ti on ti.c_space_id=wt.c_space_id and ti.c_instance_id=wt.c_instance_id where wt.c_space_id='crypto' and ti.c_run_id=? group by ti.c_frequency,wt.c_status order by ti.c_frequency,wt.c_status",(rid,))]
 print('RUN',json.dumps(d,ensure_ascii=False))
PY`}, nil)
	t.Logf("%s", result.Stdout)
	if result.Stderr != "" {
		t.Logf("stderr=%s", result.Stderr)
	}
	require.NoError(t, err)
}
