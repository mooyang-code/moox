package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"trpc.group/trpc-go/trpc-go/log"
)

// Trade 的生命周期（设计 3.8）：只有模块级的暂停、恢复追赶与放行。试运行中的策略实例（挂了账户、还没确认实盘）发来的目标带影子标记，
// Trade 只换算并记录，管理台与 moox-cli trade shadow list 经 ListShadowTargets（rpc/shadow.go）查看。

const (
	lifecycleRefreshEvery  = 30 * time.Second
	lifecycleStateFileName = "lifecycle.json"
)

// lifecycleGate 把生命周期门禁交给工人和事件消费者：门禁先建好分发出去，控制器在第一个工人启动之前绑定，
// 所以工人的第一轮就能看到暂停状态；只有绑定之前（装配期间）才放行。
//
// 门禁只看模块是否被有意停下（暂停、恢复追赶）：就绪检查的抖动（单个账户会话断开、EventBus 短暂不可用）不应该让执行和目标接收停掉。
type lifecycleGate struct {
	life atomic.Pointer[lifecycle.Controller]
}

// Bind 绑定生命周期控制器。
func (g *lifecycleGate) Bind(c *lifecycle.Controller) { g.life.Store(c) }

// Open 报告 Trade 是否放行目标接收与订单执行。
func (g *lifecycleGate) Open() bool {
	c := g.life.Load()
	return c == nil || !c.Halted()
}

// accountSyncFreshness 是实盘账户的最近一次交易所同步允许的最大陈旧度：会话每 30 秒同步一次，两分钟内算新鲜。
const accountSyncFreshness = 2 * time.Minute

// accountLister 列出全部交易账户（*store.Store 实现）。
type accountLister interface {
	ListAllTradingAccounts(ctx context.Context) ([]store.TradingAccountRecord, error)
}

// tradeCatchup 是 Trade 的恢复追赶（设计 2.2）：暂停期间账户会话照常消费回报、每 30 秒与交易所对账，
// 所以追赶只需确认「账本与交易所一致」——Trade 自身就绪（数据库、EventBus、账户会话、各工人），每个启用的实盘账户最近一次同步
// 是新鲜的、已就绪、没有错误、余额已确认，并且没有目标还在收敛。暂停前未收敛的目标不强制过期：目标自带有效期，超期的由执行器自然置为过期。
// ready 为空表示不检查就绪（测试）。
func tradeCatchup(ready func(context.Context) (bool, map[string]any), accounts accountLister, worker interface{ Quiesce(context.Context) error }, now func() time.Time) func(context.Context, time.Time) (lifecycle.Catchup, error) {
	return func(ctx context.Context, since time.Time) (lifecycle.Catchup, error) {
		out := lifecycle.Catchup{Since: since}
		if ready != nil {
			if ok, details := ready(ctx); !ok {
				raw, _ := json.Marshal(details)
				out.BlockedBy, out.Message = "Trade 就绪", string(raw)
				return out, nil
			}
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
			if fresh && account.Ready && account.LastError == "" && !account.HasBalancePending() {
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

func newLifecycle(dbPath string, quiesce func(context.Context) error, catchup func(context.Context, time.Time) (lifecycle.Catchup, error)) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     logsink.Audit,
		Module:    "trade",
		StateFile: filepath.Join(filepath.Dir(dbPath), lifecycleStateFileName),
		Hooks: lifecycle.Hooks{
			// 暂停之后等在途的订单提交结束才算真正停下；不撤单、不平仓。
			Quiesce: quiesce,
			Catchup: catchup,
			OnChange: func(from, to lifecycle.Stage, reason string) {
				log.Infof("trade_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}
