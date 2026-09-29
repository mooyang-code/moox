package command
import("context";"os";"path/filepath";"testing";"time";setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config";"github.com/stretchr/testify/require")
func TestLiveCollectorProcessDiagnostic(t *testing.T){
 if os.Getenv("MOOX_LIVE_COLLECTOR_PROCESS_DIAG")!="1"{t.Skip("opt-in")}
 root,err:=filepath.Abs("../../../..");require.NoError(t,err); snapshot,err:=setupconfig.Load(filepath.Join(root,"moox.toml"),root);require.NoError(t,err);defer clearSetupSecrets(snapshot)
 ctx,cancel:=context.WithTimeout(context.Background(),20*time.Second);defer cancel(); control,err:=dialSetupHost(ctx,snapshot.Manifest.ControlHost);require.NoError(t,err);defer control.Close()
 result,err:=control.Run(ctx,[]string{"bash","-lc",`date -u; sha256sum /data/moox/prod/bin/moox-collector 2>/dev/null || true; ps -eo pid,lstart,args | grep '[m]oox-collector' | head -20; systemctl status moox-collector --no-pager -l 2>/dev/null | head -30 || true`},nil);t.Logf("%s",result.Stdout);if result.Stderr!=""{t.Logf("stderr=%s",result.Stderr)};require.NoError(t,err)
}
