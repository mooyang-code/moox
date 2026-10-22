package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
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

// accountSyncFreshness 是实盘账户的最近一次交易所同步允许的最大陈旧度：会话每 30 秒同步一次，两分钟内算新鲜。
const accountSyncFreshness = 2 * time.Minute

// accountLister 列出全部交易账户（*store.Store 实现）。
type accountLister interface {
	ListAllTradingAccounts(ctx context.Context) ([]store.TradingAccountRecord, error)
}

// tradeCatchup 是 Trade 的恢复追赶（设计 2.2）：暂停期间账户会话照常消费回报、每 30 秒与交易所对账，
// 所以追赶只需确认「账本与交易所一致」——每个启用的实盘账户最近一次同步是新鲜的、已就绪、没有错误、余额已确认，
// 并且没有目标还在收敛。暂停前未收敛的目标不强制过期：目标自带有效期，超期的由执行器自然置为过期。
func tradeCatchup(accounts accountLister, worker interface{ Quiesce(context.Context) error }, now func() time.Time) func(context.Context, lifecycle.Ref, time.Time) (lifecycle.Catchup, error) {
	return func(ctx context.Context, ref lifecycle.Ref, since time.Time) (lifecycle.Catchup, error) {
		out := lifecycle.Catchup{Since: since}
		if ref != (lifecycle.Ref{}) {
			out.CaughtUp = true
			return out, nil
		}
		list, err := accounts.ListAllTradingAccounts(ctx)
		if err != nil {
			out.Message = "读取交易账户失败：" + err.Error()
			return out, err
		}
		var total, done int64
		var first string
		at := now()
		for _, account := range list {
			if account.ExecutionMode != "LIVE" || account.Status != "ENABLED" {
				continue
			}
			total++
			fresh := account.LastSyncAt > 0 && at.Sub(time.UnixMilli(account.LastSyncAt)) <= accountSyncFreshness
			if fresh && account.Ready && account.LastError == "" && account.BalancePendingUntil == 0 {
				done++
				continue
			}
			if first == "" {
				first = account.TradingAccountID
			}
		}
		out.Total, out.Done = total, done
		if first != "" {
			out.BlockedBy = "交易所对账"
			out.Message = fmt.Sprintf("账户 %s 与交易所尚未对账一致（%d/%d）", first, done, total)
			return out, nil
		}
		// 没有在途的订单提交。
		quiesceCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := worker.Quiesce(quiesceCtx); err != nil {
			out.Message = "仍有目标在收敛：" + err.Error()
			return out, nil
		}
		out.CaughtUp = true
		return out, nil
	}
}

func newLifecycle(dbPath string, readiness health.Readiness, quiesce func(context.Context) error, catchup func(context.Context, lifecycle.Ref, time.Time) (lifecycle.Catchup, error), reset *lifecycle.ResetSpec) (*lifecycle.Controller, error) {
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
			Catchup: catchup,
			Reset:   reset,
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
		{Path: lifecycle.ResetPrefix, Handler: life.ResetHandler()},
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
