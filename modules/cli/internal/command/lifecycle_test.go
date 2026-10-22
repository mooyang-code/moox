package command

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func TestLifecycleCommandsAgainstController(t *testing.T) {
	ctrl, err := lifecycle.New(lifecycle.Options{Module: "factor-engine", Hooks: lifecycle.Hooks{
		Objects: func() []lifecycle.Ref { return []lifecycle.Ref{{Kind: "factor_set", ID: "fset_a"}} },
		CheckObject: func(context.Context, lifecycle.Ref) []lifecycle.Check {
			return []lifecycle.Check{{Name: "本地窗口", Status: lifecycle.CheckFail, Message: "窗口还没有载入完成", Remedy: "moox-cli lifecycle init --set fset_a"}}
		},
	}})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(lifecycle.StatusPath, ctrl.StatusHandler())
	mux.Handle(lifecycle.CommandPath, ctrl.CommandHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "k")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "s")
	ctx := context.Background()
	ctrl.Refresh(ctx)

	opts := lifecycleOptions{Endpoint: srv.URL, Set: "fset_a"}
	var out bytes.Buffer
	require.NoError(t, runLifecycleCommand(ctx, opts, lifecycle.CmdInit, &out))
	require.Contains(t, out.String(), "未就绪")
	require.Contains(t, out.String(), "处理：moox-cli lifecycle init --set fset_a")

	out.Reset()
	require.NoError(t, runLifecycleStatus(ctx, lifecycleOptions{Endpoint: srv.URL}, &out))
	require.Contains(t, out.String(), "factor_set/fset_a")

	require.Error(t, runLifecycleCommand(ctx, opts, lifecycle.CmdStart, &out), "未就绪的集合不能启动")
	_, err = lifecycleOptions{Set: "a", Kind: "x", ID: "y"}.ref()
	require.Error(t, err)
}
