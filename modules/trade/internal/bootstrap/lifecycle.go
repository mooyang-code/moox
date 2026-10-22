package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	replayapp "github.com/mooyang-code/moox/modules/trade/internal/application/replay"
	"github.com/mooyang-code/moox/modules/trade/internal/health"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Trade 的生命周期（设计第 6 节）：模块自身的检查就是就绪判定（数据库、EventBus、账户会话、各工人）。
// 预热中的策略实例发来的目标带影子标记，Trade 只换算并记录，这一份记录通过 /v1/shadow/targets 查看。

const (
	lifecycleRefreshEvery  = 30 * time.Second
	lifecycleStateFileName = "lifecycle.json"
	shadowTargetsPath      = "/v1/shadow/targets"
)

// lifecycleGate 把生命周期门禁交给在生命周期创建之前就启动的工人和事件消费者：绑定之前放行。
//
// 门禁用 Halted 而不是 Allowed：就绪检查的抖动（单个账户会话断开、EventBus 短暂不可用）不应该让执行和目标接收停掉，
// 只有用户或系统有意停下（暂停、恢复追赶、重置、故障）才停。
type lifecycleGate struct {
	life atomic.Pointer[lifecycle.Controller]
}

// Bind 绑定生命周期控制器。
func (g *lifecycleGate) Bind(c *lifecycle.Controller) { g.life.Store(c) }

// Open 报告 Trade 是否放行目标接收与订单执行。
func (g *lifecycleGate) Open() bool {
	c := g.life.Load()
	return c == nil || !c.Halted(lifecycle.Ref{})
}

func newLifecycle(dbPath string, readiness health.Readiness, quiesce func(context.Context) error) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Module:    "trade",
		StateFile: filepath.Join(filepath.Dir(dbPath), lifecycleStateFileName),
		Hooks: lifecycle.Hooks{
			CheckModule: func(ctx context.Context) []lifecycle.Check {
				ready, details := readiness.Evaluate(ctx)
				if ready {
					return []lifecycle.Check{{Name: "Trade 就绪", Status: lifecycle.CheckPass}}
				}
				raw, _ := json.Marshal(details)
				return []lifecycle.Check{{Name: "Trade 就绪", Status: lifecycle.CheckFail, Message: string(raw)}}
			},
			// 暂停之后等在途的订单提交结束才算真正停下；不撤单、不平仓。
			Quiesce: func(ctx context.Context, _ lifecycle.Ref) error { return quiesce(ctx) },
			OnChange: func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("trade_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}

func lifecycleRoutes(life *lifecycle.Controller, tradeStore *store.Store) []health.Route {
	routes := []health.Route{
		{Path: lifecycle.StatusPath, Handler: life.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: life.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: life.CatchupHandler()},
		{Path: shadowTargetsPath, Handler: shadowTargetsHandler(tradeStore)},
	}
	routes = append(routes, replayRoutes(&replayapp.Service{Store: tradeStore})...)
	return routes
}

// shadowTargetsHandler 返回实例最近的影子目标：GET ?space=<空间>&instance=<实例>&limit=<条数>。
func shadowTargetsHandler(tradeStore *store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "GET only"})
			return
		}
		query := r.URL.Query()
		limit, _ := strconv.Atoi(query.Get("limit"))
		records, err := tradeStore.ListShadowTargets(r.Context(), query.Get("space"), query.Get("instance"), limit)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"targets": records})
	})
}
