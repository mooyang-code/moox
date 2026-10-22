package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

type tradeResetEnv struct {
	life   *lifecycle.Controller
	db     *store.Store
	dbPath string
	synced []string
	// onSync 在向交易所对账时执行（测试用它模拟 plan 到 apply 之间新增的仓位）。
	onSync func()
}

func newTradeResetEnv(t *testing.T) *tradeResetEnv {
	t.Helper()
	env := &tradeResetEnv{dbPath: filepath.Join(t.TempDir(), "moox_trade.db")}
	db, err := store.Open(env.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	env.db = db
	spec := tradeResetSpec(db, env.dbPath, func(_ context.Context, id string) error {
		env.synced = append(env.synced, id)
		if env.onSync != nil {
			env.onSync()
		}
		return nil
	})
	life, err := lifecycle.New(lifecycle.Options{Module: "trade", StateFile: filepath.Join(filepath.Dir(env.dbPath), "lifecycle.json"), Hooks: lifecycle.Hooks{Reset: spec}})
	require.NoError(t, err)
	env.life = life
	life.Refresh(context.Background())
	return env
}

func (e *tradeResetEnv) seedAccounts(t *testing.T) {
	t.Helper()
	require.NoError(t, e.db.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper", Exchange: "BINANCE", MarketType: "SPOT",
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "live-1", Name: "live", Exchange: "BINANCE", MarketType: "SPOT",
			ExecutionMode: "LIVE", Environment: "PRODUCTION", SettlementAsset: "USDT", Status: "ENABLED", CredentialSecretID: "secret-1",
		})
	}))
}

func (e *tradeResetEnv) setPosition(t *testing.T, account, qty string) {
	t.Helper()
	sqlDB, err := e.db.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT OR REPLACE INTO t_trading_positions (c_space_id, c_trading_account_id, c_instrument_id, c_exchange_symbol, c_position_side, c_signed_quantity)
		VALUES ('space-1', ?, 'BTC-USDT', 'BTC-USDT', 'NET', ?)`, account, qty)
	require.NoError(t, err)
}

func (e *tradeResetEnv) count(t *testing.T, table string) int {
	t.Helper()
	sqlDB, err := e.db.SQLDB()
	require.NoError(t, err)
	var n int
	require.NoError(t, sqlDB.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

func (e *tradeResetEnv) pause(t *testing.T) {
	t.Helper()
	_, err := e.life.Execute(context.Background(), lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
}

func TestTradeResetClassifiesEveryTable(t *testing.T) {
	env := newTradeResetEnv(t)
	sqlDB, err := env.db.SQLDB()
	require.NoError(t, err)
	actual, err := lifecycle.BusinessTables(context.Background(), sqlDB)
	require.NoError(t, err)
	unclassified, missing := lifecycle.ClassifyTables(actual, tradeDataTables, tradeConfigTables)
	require.Empty(t, unclassified, "有表没有归类到重置清单")
	require.Empty(t, missing, "重置清单里有不存在的表")
}

// 实盘账户有持仓：plan 直接给阻止项、不发令牌；纸面账户的持仓不挡。
func TestTradeResetBlocksOnLiveExposureOnly(t *testing.T) {
	env := newTradeResetEnv(t)
	ctx := context.Background()
	env.seedAccounts(t)
	env.setPosition(t, "paper-1", "3")
	plan, err := env.life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers, "纸面账户的持仓不挡重置")
	require.NotEmpty(t, plan.Token)

	env.setPosition(t, "live-1", "0.25")
	plan, err = env.life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Len(t, plan.Blockers, 1)
	require.Contains(t, plan.Blockers[0], "实盘账户 live-1 还有持仓")
	require.Empty(t, plan.Token, "有阻止项不发令牌")

	env.setPosition(t, "live-1", "0")
	sqlDB, err := env.db.SQLDB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO t_trade_orders (c_space_id, c_order_id, c_trading_account_id, c_client_order_id, c_exchange, c_market_type, c_instrument_id,
		c_exchange_symbol, c_order_type, c_side, c_quantity, c_reference_price, c_reference_price_at, c_owner_type, c_owner_id, c_state)
		VALUES ('space-1', 'o1', 'live-1', 'c1', 'BINANCE', 'SPOT', 'BTC-USDT', 'BTC-USDT', 'LIMIT', 'BUY', '1', '100', 1, 'EXTERNAL', 'ext', 'NEW')`)
	require.NoError(t, err)
	plan, err = env.life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Len(t, plan.Blockers, 1)
	require.Contains(t, plan.Blockers[0], "未完成订单", "暂停不撤单：未成交的挂单仍可能成交，必须先拦住")
	_, err = sqlDB.Exec(`UPDATE t_trade_orders SET c_state = 'CANCELED'`)
	require.NoError(t, err)
	plan, err = env.life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers, "终态订单不挡")
}

// 执行的第一步重新向交易所对账并核验：plan 之后新增了仓位，拒绝重置，什么都没删；之后仓位清掉可续做。
func TestTradeResetReverifiesAgainstTheExchangeAndKeepsDataOnRefusal(t *testing.T) {
	env := newTradeResetEnv(t)
	ctx := context.Background()
	env.seedAccounts(t)
	env.setPosition(t, "paper-1", "3")
	env.pause(t)
	plan, err := env.life.ResetPlan(ctx, lifecycle.ResetAll)
	require.NoError(t, err)
	require.Empty(t, plan.Blockers)

	// 对账时发现交易所上有新仓位（落进本地账本）。
	env.onSync = func() { env.setPosition(t, "live-1", "1") }
	_, err = env.life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetAll, By: "ops"})
	require.NoError(t, err)
	require.NoError(t, env.life.ResetWait(ctx))
	st := env.life.ResetStatus()
	require.Equal(t, lifecycle.StageResetFailed, st.Stage)
	require.Contains(t, st.Operation.Error, "拒绝重置")
	require.Contains(t, st.Operation.Error, "live-1")
	require.Equal(t, []string{"live-1"}, env.synced, "只对实盘账户向交易所对账")
	require.Equal(t, 2, env.count(t, "t_trading_accounts"), "被拒绝时什么都没删")
	require.Equal(t, 2, env.count(t, "t_trading_positions"))

	// 用户平掉仓位后续做：从失败的那一步重试，通过后继续备份和清表。
	env.onSync = nil
	env.setPosition(t, "live-1", "0")
	_, err = env.life.ResetApply(ctx, lifecycle.ResetApplyRequest{OperationID: st.Operation.OperationID})
	require.NoError(t, err)
	require.NoError(t, env.life.ResetWait(ctx))
	require.Equal(t, lifecycle.StageUninitialized, env.life.ResetStatus().Stage)
	require.Zero(t, env.count(t, "t_trading_accounts"))
	require.Zero(t, env.count(t, "t_trading_positions"))
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(env.dbPath), "reset-backups", "*", "moox_trade.db"))
	require.NoError(t, err)
	require.Len(t, backups, 1)
}

// data 范围：清运行数据，保留账户配置。
func TestTradeResetDataKeepsAccounts(t *testing.T) {
	env := newTradeResetEnv(t)
	ctx := context.Background()
	env.seedAccounts(t)
	env.setPosition(t, "paper-1", "3")
	env.pause(t)
	plan, err := env.life.ResetPlan(ctx, lifecycle.ResetData)
	require.NoError(t, err)
	require.Contains(t, plan.Keeps, "交易账户、逻辑账户及成员、纸面账户配置、行情标的（配置）")
	_, err = env.life.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: plan.Token, Scope: lifecycle.ResetData})
	require.NoError(t, err)
	require.NoError(t, env.life.ResetWait(ctx))
	require.Equal(t, 2, env.count(t, "t_trading_accounts"), "data 范围保留账户")
	require.Zero(t, env.count(t, "t_trading_positions"))
}
