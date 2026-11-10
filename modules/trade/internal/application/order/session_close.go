package order

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// ReasonSessionClose 是收盘撤单写进订单的原因。
const ReasonSessionClose = "收盘撤单"

// SessionCloser 是收盘撤单（A 股这类有日收盘的场所，模拟盘和实盘一样）：日收盘（15:00）之后，
// 先对账户做一次全量同步，再把“提交时间早于最近一次日收盘、状态已确认在途”的未完成单按已撤结束，原因写“收盘撤单”。
//   - 只按日收盘判断：午休时 LastClose 仍是上一个交易日的 15:00，上午的挂单不受影响；
//   - 每轮都按订单现状扫描、不记进度，进程重启后自然补做，不需要跨日查询；
//   - 提交结果未知（SUBMIT_UNKNOWN）、撤单结果未知（CANCEL_UNKNOWN）的单不在这里结束，先走现有的结果不明处理，
//     处理完的结果由下一轮收盘撤单接手；还没提交的（PENDING）不碰。
//   - 本地强制结束排在收盘后最后一次同步之后，之后再到的成交照常入账。
type SessionCloser struct {
	Store     *store.Store
	Exchanges execution.ExchangeCatalog
	// Sync 对账户做一次全量同步（实盘先把券商侧最后的成交、撤单拉回来）。
	Sync func(ctx context.Context, tradingAccountID string) error
	Now  func() time.Time
}

// sessionCloseStates 是收盘撤单处理的状态：交易所已确认在途的单。
var sessionCloseStates = map[orderdomain.State]bool{
	orderdomain.Open:            true,
	orderdomain.PartiallyFilled: true,
	orderdomain.Canceling:       true,
}

func (c *SessionCloser) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Run 扫一遍全部启用的账户；单个账户失败不影响其他账户，错误合在一起返回。
func (c *SessionCloser) Run(ctx context.Context) error {
	if c == nil || c.Store == nil {
		return ErrServiceConfig
	}
	accounts, err := c.Store.ListEnabledTradingAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, account := range accounts {
		if _, err := c.CloseAccount(ctx, account); err != nil {
			errs = append(errs, fmt.Errorf("账户 %s 收盘撤单：%w", account.TradingAccountID, err))
		}
	}
	return errors.Join(errs...)
}

// CloseAccount 处理一个账户，返回结束的订单数。没有日收盘的场所（加密货币）直接跳过。
func (c *SessionCloser) CloseAccount(ctx context.Context, account store.TradingAccountRecord) (int, error) {
	lastClose, ok := execution.LastClose(c.Exchanges, account.Exchange, c.now())
	if !ok {
		return 0, nil
	}
	candidates, err := c.candidates(ctx, account, lastClose)
	if err != nil || len(candidates) == 0 {
		return 0, err
	}
	if c.Sync != nil {
		if err := c.Sync(ctx, account.TradingAccountID); err != nil {
			return 0, fmt.Errorf("收盘后同步失败，稍后重试：%w", err)
		}
		// 同步可能已经把一部分单收尾（成交、撤单），按同步之后的状态重新挑。
		if candidates, err = c.candidates(ctx, account, lastClose); err != nil {
			return 0, err
		}
	}
	closed := 0
	// 一笔失败不挡住后面的：记下错误继续处理，最后一起返回，调用方稍后重试。
	var errs []error
	for _, current := range candidates {
		done, err := c.closeOrder(ctx, current, lastClose)
		if err != nil {
			errs = append(errs, fmt.Errorf("订单 %s：%w", current.OrderID, err))
			continue
		}
		if done {
			closed++
		}
	}
	return closed, errors.Join(errs...)
}

func (c *SessionCloser) candidates(ctx context.Context, account store.TradingAccountRecord, lastClose time.Time) ([]store.OrderRecord, error) {
	orders, err := c.Store.ListActiveOrdersForAccount(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	var result []store.OrderRecord
	for _, current := range orders {
		if closable(current, lastClose) {
			result = append(result, current)
		}
	}
	return result, nil
}

// closable 报告订单是否该收盘撤单：已确认在途，且在日收盘之前提交过（SubmittedAt 为 0 的是还没提交的单）。
func closable(current store.OrderRecord, lastClose time.Time) bool {
	return sessionCloseStates[orderdomain.State(current.State)] &&
		current.SubmittedAt > 0 && current.SubmittedAt < lastClose.UnixMilli()
}

// closeOrder 在一个事务里重新读订单、确认它仍该结束，再按已撤结束并释放剩余预留（和确认撤单同一个状态迁移）。
func (c *SessionCloser) closeOrder(ctx context.Context, current store.OrderRecord, lastClose time.Time) (bool, error) {
	done := false
	err := c.Store.Transaction(ctx, func(tx *store.Tx) error {
		record, err := tx.GetOrder(current.SpaceID, current.OrderID)
		if err != nil {
			return err
		}
		if !closable(record, lastClose) {
			return nil
		}
		quantity, err := shared.ParseDecimal(record.Quantity)
		if err != nil {
			return fmt.Errorf("%w: order quantity", store.ErrInvalidRecord)
		}
		filled, err := shared.ParseDecimal(record.FilledQuantity)
		if err != nil {
			return fmt.Errorf("%w: order filled quantity", store.ErrInvalidRecord)
		}
		aggregate := orderdomain.Order{
			ID:             shared.OrderID(record.OrderID),
			Spec:           orderdomain.OrderSpec{ClientOrderSpec: orderdomain.ClientOrderSpec{Quantity: quantity}},
			FilledQuantity: filled, State: orderdomain.State(record.State), Version: record.Version,
		}
		if _, err := aggregate.ConfirmCancel(); err != nil {
			return err
		}
		reducer := &consumer.Reducer{Store: c.Store, Now: c.Now}
		if err := reducer.FinishOrderTx(tx, record, aggregate.State, ReasonSessionClose); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}
