package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// cancelRecordingOrders 是只记撤单的订单服务：关闭会话之后执行器只走过期路径，不会下单。
type cancelRecordingOrders struct {
	mu       sync.Mutex
	canceled []string
}

var errUnexpectedOrderCall = errors.New("关闭会话之后不应下单")

func (o *cancelRecordingOrders) Capacity(context.Context, string, orderdomain.OrderSpec) (shared.Decimal, error) {
	return shared.Decimal{}, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) Place(context.Context, string, orderdomain.OrderSpec) (orderdomain.Order, error) {
	return orderdomain.Order{}, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) Submit(context.Context, string, string) (orderdomain.Order, error) {
	return orderdomain.Order{}, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) SubmitBatch(context.Context, string, []string) ([]orderapp.BatchResult, error) {
	return nil, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) ResolveUnknownBatch(context.Context, string, []string) ([]orderapp.BatchResult, error) {
	return nil, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) DiscardPending(context.Context, string, string) (orderdomain.Order, error) {
	return orderdomain.Order{}, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) ResolveUnknown(context.Context, string, string) (orderdomain.Order, error) {
	return orderdomain.Order{}, errUnexpectedOrderCall
}
func (o *cancelRecordingOrders) Cancel(_ context.Context, _ string, orderID string) (orderdomain.Order, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.canceled = append(o.canceled, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Canceled}, nil
}
func (o *cancelRecordingOrders) canceledOrders() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.canceled...)
}

type fixedPrice struct{}

func (fixedPrice) LatestPrice(context.Context, string, exchange.SymbolKey) (targetapp.Quote, error) {
	return targetapp.Quote{Price: shared.MustDecimal("100"), UpdatedAt: time.Now()}, nil
}

// 已收敛并带挂单的目标被关闭后，挂单在几秒内撤掉，不等旧定时器（批 3 的 3.6、M1）：定时扫描只看 PENDING、CONVERGING，
// 这里定时器也拨到一小时以后，目标按旧有效期还有两小时；只有 CloseTargetSession 提交之后的 WakeTarget 能让执行器
// 立刻走过期路径撤掉挂单、把目标置为过期。用真实的目标工人、执行器和关闭服务。
func TestClosedConvergedTargetCancelsRestingOrderWithinSeconds(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	orders := &cancelRecordingOrders{}
	worker := &TargetWorker{
		Store: tradeStore, Interval: time.Hour,
		Executor: &targetapp.Executor{Store: tradeStore, Orders: orders, Prices: fixedPrice{}},
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// 先让工人做完启动时的那一轮全量扫描，之后只有唤醒能让它处理已收敛的目标。
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, time.Second, time.Millisecond)

	now := time.Now().UTC()
	limitPrice := "99.9"
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "main", Exchange: "binance", Markets: []string{"swap"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED", Ready: true,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if err := tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", ""); err != nil {
			return err
		}
		if _, _, err := tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1",
			InstanceID: "instance-1", SessionID: "session-1", SessionStartedAt: 1_000, StrategyID: "strategy-1",
			BarEndTime: now.Add(-time.Minute).UnixMilli(), ValidUntil: now.Add(2 * time.Hour).UnixMilli(),
			Targets: []store.InstrumentQuantity{}, Status: targetapp.StatusPending, AcceptedAt: now.UnixMilli(),
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "resting-1", TradingAccountID: "account-1", LogicalAccountID: "logical-1", InstanceID: "instance-1",
			ClientOrderID: "resting-1", ExchangeOrderID: "exchange-resting-1", Exchange: "binance", MarketType: "swap",
			InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY",
			Quantity: "1", LimitPrice: &limitPrice, ReferencePrice: "100", FilledQuantity: "0", OwnerType: "TARGET", OwnerID: "target-1",
			State: "OPEN", SubmittedAt: now.UnixMilli(), Version: 1,
		})
	}))
	target, err := tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	target.Status = targetapp.StatusConverged
	updated, err := tradeStore.UpdateLogicalAccountTargetState(ctx, target)
	require.NoError(t, err)
	require.True(t, updated)
	require.Never(t, func() bool { return len(orders.canceledOrders()) > 0 }, 200*time.Millisecond, 10*time.Millisecond,
		"不关闭时，已收敛的目标不被定时扫描，挂单留着")

	logical := &logicalapp.Service{Store: tradeStore, WakeTarget: worker.WakeTarget}
	_, err = logical.CloseTargetSession(ctx, "space-1", "logical-1", "instance-1", "session-1", 1_000)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		current, err := tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
		return err == nil && current.Status == targetapp.StatusExpired
	}, 3*time.Second, 10*time.Millisecond, "关闭之后几秒内目标置为过期")
	require.Equal(t, []string{"resting-1"}, orders.canceledOrders(), "挂单被撤")
}
