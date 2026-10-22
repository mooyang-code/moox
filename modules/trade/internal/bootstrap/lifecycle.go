package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
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

func newLifecycle(dbPath string, readiness health.Readiness) (*lifecycle.Controller, error) {
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
