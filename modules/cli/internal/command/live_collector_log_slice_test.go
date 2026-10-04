package command

import (
	"context"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveCollectorLogSlice(t *testing.T) {
	if os.Getenv("MOOX_LIVE_COLLECTOR_LOG_SLICE") != "1" {
		t.Skip("opt-in")
	}
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	require.NoError(t, err)
	defer clearSetupSecrets(snapshot)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	require.NoError(t, err)
	defer control.Close()
	result, err := control.Run(ctx, []string{"bash", "-lc", `python3 - <<'PY'
p='/data/moox/prod/logs/collector/stdout.log'
lines=open(p,errors='replace').read().splitlines()[-12000:]
keys=['skip invalid collection task','series_hash','became stale','invoke scheduler failed','shared planning','period initialization','context deadline']
for x in lines:
 if ('2026/09/29 11:36:' in x or '2026/09/29 11:37:' in x) and any(k in x for k in keys): print(x[:1600])
PY`}, nil)
	t.Logf("%s", result.Stdout)
	require.NoError(t, err)
}
