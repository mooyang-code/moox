package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const pinnedTargetJSON = `[{"instrument_id":"BTC-USDT","quantity":"2","trading_account_id":"account-1","exchange_symbol":"BTCUSDT"}]`

func TestOpenInvalidatesPinnedCurrentTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trade.db")
	s, err := Open(path)
	require.NoError(t, err)
	seedLogicalAccount(t, s, "runner-1")
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		if err := tx.UpsertInstrument(InstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01", ExchangeQuantityStep: "0.0001", Status: "TRADING"}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true}); err != nil {
			return err
		}
		return tx.CreateOrder(OrderRecord{SpaceID: "space-1", TradingAccountID: "account-1", OrderID: "old-order", ClientOrderID: "old-client", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "2", ReferencePrice: "100", OwnerType: "TARGET", OwnerID: "target-1", LogicalAccountID: "logical-1", RunnerID: "runner-1", State: "OPEN"})
	}))
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		_, err := tx.InsertFill(FillRecord{SpaceID: "space-1", TradingAccountID: "account-1", FillID: "old-fill", ExchangeTradeID: "old-trade", OrderID: "old-order", Price: "100", Quantity: "1", Fee: "0.1", FeeAsset: "USDT", SettlementAsset: "USDT", TradedAt: 123})
		return err
	}))
	_, _, err = s.AcceptLogicalAccountTarget(context.Background(), validLogicalAccountTarget())
	require.NoError(t, err)
	require.NoError(t, s.db.Exec(`UPDATE t_logical_account_targets SET c_targets_json = ?`, pinnedTargetJSON).Error)
	require.NoError(t, s.db.Exec(`UPDATE t_logical_accounts SET c_owner_instance_id='runner-1', c_owner_session_id='old-session', c_auth_fence='old-fence', c_automation_state='ACTIVE', c_pause_reason=''`).Error)
	receipt := TargetReceiptRecord{SpaceID: "space-1", TargetID: "target-1", LogicalAccountID: "logical-1", RunnerID: "runner-1", CommandSequence: 1, RequestHash: "old-hash", SignalTime: 1, WeightsJSON: "[]", Equity: "200", EquitySourceTime: 1, ReferencePricesJSON: `{"evidence":[{"trading_account_id":"account-1","exchange_symbol":"BTCUSDT","price":"100"}]}`, QuantityTargetsJSON: pinnedTargetJSON, AcceptedAt: 1}
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { return tx.InsertTargetReceipt(receipt) }))
	facts := pinMigrationFacts(t, s.db)
	before, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	after, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", after.AutomationState)
	require.Empty(t, after.OwnerRunnerID)
	require.Empty(t, after.OwnerInstanceID)
	require.Empty(t, after.OwnerSessionID)
	require.NotEqual(t, before.AuthFence, after.AuthFence)
	require.Greater(t, after.OwnerClaimedAt, before.OwnerClaimedAt)
	_, err = s.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	again, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, after, again)
	require.Equal(t, facts, pinMigrationFacts(t, s.db))
	stored, err := s.GetTargetReceipt(context.Background(), "space-1", "target-1")
	require.NoError(t, err)
	require.Equal(t, receipt, stored)
	err = s.Transaction(context.Background(), func(tx *Tx) error {
		_, _, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "runner-1", "old-session", "old-fence")
		return err
	})
	require.ErrorIs(t, err, ErrConflict)
	newTarget := validLogicalAccountTarget()
	newTarget.InstanceID, newTarget.SessionID, newTarget.StrategyID = "runner-1", "old-session", "strategy-1"
	newTarget.BarEndTime = time.Now().Add(-time.Second).UnixMilli()
	newTarget.EffectiveAt, newTarget.ValidUntil = newTarget.BarEndTime, time.Now().Add(time.Minute).UnixMilli()
	_, _, err = s.AcceptLogicalAccountTarget(context.Background(), newTarget)
	require.ErrorIs(t, err, ErrTargetAuthorization)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		_, _, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "runner-1", "new-session", after.AuthFence)
		return err
	}))
	newTarget.TargetID, newTarget.SessionID = "new-target", "new-session"
	_, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), newTarget)
	require.NoError(t, err)
	require.True(t, accepted)
}

func pinMigrationFacts(t *testing.T, db *gorm.DB) map[string][]map[string]interface{} {
	t.Helper()
	result := map[string][]map[string]interface{}{}
	for _, table := range []string{"t_trading_accounts", "t_trade_orders", "t_order_fills", "t_paper_balance_projections", "t_paper_asset_balances", "t_logical_account_target_receipts"} {
		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Find(&rows).Error)
		result[table] = rows
	}
	return result
}

func TestPinnedTargetMigrationRejectsUnknownJSONWithoutMutation(t *testing.T) {
	for _, raw := range []string{
		`not-json`, `null`, `[null]`, `[{}]`,
		`[{"instrument_id":"BTC","quantity":"2","extra":true}]`,
		`[{"instrument_id":"BTC","quantity":"2","quantity":"3"}]`,
		`[{"instrument_id":"BTC","quantity":"2","trading_account_id":"a"}]`,
		`[{"instrument_id":"BTC","quantity":"2","trading_account_id":null,"exchange_symbol":"BTC"}]`,
		`[{"instrument_id":"BTC","quantity":"1","trading_account_id":"a","exchange_symbol":"BTCUSDT"},{"instrument_id":"ETH","quantity":"1"}]`,
		`[{"instrument_id":"ETH","quantity":"1"},{"instrument_id":"BTC","quantity":"1","trading_account_id":"a","exchange_symbol":"BTCUSDT"}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trade.db")
			s, err := Open(path)
			require.NoError(t, err)
			seedLogicalAccount(t, s, "runner-1")
			_, _, err = s.AcceptLogicalAccountTarget(context.Background(), validLogicalAccountTarget())
			require.NoError(t, err)
			require.NoError(t, s.db.Exec(`PRAGMA ignore_check_constraints=ON`).Error)
			require.NoError(t, s.db.Exec(`UPDATE t_logical_account_targets SET c_targets_json=?`, raw).Error)
			require.NoError(t, s.db.Exec(`UPDATE t_logical_accounts SET c_owner_instance_id=NULL, c_auth_fence=''`).Error)
			before, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.NoError(t, s.Close())
			_, err = Open(path)
			require.ErrorIs(t, err, ErrIncompatibleSchema)
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			check := &Store{db: db}
			defer check.Close()
			after, err := check.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, before, after)
			var current logicalAccountTargetRow
			require.NoError(t, db.Take(&current).Error)
			require.Equal(t, raw, current.TargetsJSON)
		})
	}
}

func TestPinnedTargetMigrationRollsBackAndRetries(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	_, _, err := s.AcceptLogicalAccountTarget(context.Background(), validLogicalAccountTarget())
	require.NoError(t, err)
	require.NoError(t, s.db.Exec(`UPDATE t_logical_account_targets SET c_targets_json=?`, pinnedTargetJSON).Error)
	before, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	injected := errors.New("injected pin delete failure")
	require.NoError(t, s.db.Callback().Raw().Before("gorm:raw").Register("fail_pin_delete", func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM t_logical_account_targets") {
			tx.AddError(injected)
		}
	}))
	require.ErrorIs(t, migratePinnedCurrentTargets(s.db), injected)
	require.NoError(t, s.db.Callback().Raw().Remove("fail_pin_delete"))
	after, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, before, after)
	var row logicalAccountTargetRow
	require.NoError(t, s.db.Take(&row).Error)
	require.Equal(t, pinnedTargetJSON, row.TargetsJSON)
	require.NoError(t, migratePinnedCurrentTargets(s.db))
	_, err = s.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestCurrentTargetReadRejectsPinsAndUnknownFields(t *testing.T) {
	for _, raw := range []string{pinnedTargetJSON, `[{"instrument_id":"BTC","quantity":"2","extra":true}]`} {
		_, err := logicalAccountTargetRecord(logicalAccountTargetRow{TargetsJSON: raw, BlockedTargetsJSON: "[]"})
		require.ErrorIs(t, err, ErrInvalidRecord)
	}
	raw := `[{"instrument_id":"BTC","quantity":"2"}]`
	targets, pinned, err := decodeCurrentTargets(raw, false)
	require.NoError(t, err)
	require.False(t, pinned)
	require.Equal(t, []InstrumentTarget{{InstrumentID: "BTC", Quantity: "2"}}, targets)
}

func TestPinnedTargetMigrationLeavesUnpinnedTargetUnchanged(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	before, _, err := s.AcceptLogicalAccountTarget(context.Background(), validLogicalAccountTarget())
	require.NoError(t, err)
	account, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.NoError(t, migratePinnedCurrentTargets(s.db))
	after, err := s.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterAccount, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, account, afterAccount)
}

func TestPinnedTargetMigrationRejectsUnknownDependencies(t *testing.T) {
	for _, statement := range []string{
		`CREATE TRIGGER unknown_pin_trigger BEFORE DELETE ON t_logical_account_targets BEGIN SELECT 1; END`,
		`CREATE TABLE unknown_pin_child (space TEXT, logical TEXT, FOREIGN KEY(space, logical) REFERENCES t_logical_account_targets(c_space_id, c_logical_account_id) ON DELETE CASCADE)`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := openTestStore(t)
			seedLogicalAccount(t, s, "runner-1")
			_, _, err := s.AcceptLogicalAccountTarget(context.Background(), validLogicalAccountTarget())
			require.NoError(t, err)
			require.NoError(t, s.db.Exec(`UPDATE t_logical_account_targets SET c_targets_json=?`, pinnedTargetJSON).Error)
			require.NoError(t, s.db.Exec(statement).Error)
			before, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.ErrorIs(t, migratePinnedCurrentTargets(s.db), ErrIncompatibleSchema)
			after, err := s.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, before, after)
			var row logicalAccountTargetRow
			require.NoError(t, s.db.Take(&row).Error)
			require.Equal(t, pinnedTargetJSON, row.TargetsJSON)
		})
	}
}
