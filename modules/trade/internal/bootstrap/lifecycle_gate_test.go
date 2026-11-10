package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/health"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

type reconciledSessions struct{}

func (reconciledSessions) Snapshot() traderuntime.SessionSnapshot {
	return traderuntime.SessionSnapshot{Reconciled: true}
}

// 状态文件里是暂停：重启后控制器一建好并绑定，门禁就是关的，工人第一轮不会在暂停状态下执行。
func TestGateIsClosedAfterBindWhenStateFileSaysPaused(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "trade.db")
	ctx := context.Background()
	quiesce := func(context.Context) error { return nil }
	catchup := func(context.Context, lifecycle.Ref, time.Time) (lifecycle.Catchup, error) {
		return lifecycle.Catchup{CaughtUp: true}, nil
	}
	ready := health.Readiness{
		DatabaseReady:      func(context.Context) error { return nil },
		Sessions:           reconciledSessions{},
		TargetWorker:       func() traderuntime.TargetWorkerSnapshot { return traderuntime.TargetWorkerSnapshot{Ready: true} },
		OperatorWorker:     func() traderuntime.OperatorWorkerSnapshot { return traderuntime.OperatorWorkerSnapshot{Ready: true} },
		PaperMatcherWorker: func() (bool, string) { return true, "" },
	}

	// 上一次运行：用户暂停了 Trade，状态写进文件。
	before, err := newLifecycle(dbPath, ready, quiesce, catchup, nil)
	require.NoError(t, err)
	before.Refresh(ctx)
	_, err = before.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, Reason: "维护"})
	require.NoError(t, err)

	// 重启：就绪判定里的依赖还没起来，控制器刚读完状态文件。
	gate := &lifecycleGate{}
	require.True(t, gate.Open(), "绑定之前放行，所以必须在第一个工人启动之前绑定")
	after, err := newLifecycle(dbPath, health.Readiness{}, quiesce, catchup, nil)
	require.NoError(t, err)
	gate.Bind(after)
	require.False(t, gate.Open(), "状态文件为暂停：绑定后门禁关闭")
}

// 控制器和门禁绑定必须在第一个工人启动之前完成，否则暂停状态下重启的第一轮会按“放行”执行。
func TestLifecycleIsBoundBeforeTheFirstWorkerStarts(t *testing.T) {
	source, err := os.ReadFile("bootstrap.go")
	require.NoError(t, err)
	text := string(source)
	bind := strings.Index(text, "lifeGate.Bind(life)")
	firstWorker := strings.Index(text, "runWorker(manager.Run)")
	require.NotEqual(t, -1, bind, "找不到 lifeGate.Bind(life)")
	require.NotEqual(t, -1, firstWorker, "找不到第一个 runWorker 调用")
	require.Less(t, bind, firstWorker, "lifeGate.Bind 必须在第一个 runWorker 之前")
	require.Equal(t, 1, strings.Count(text, "lifeGate.Bind("), "只在一处绑定")
}
