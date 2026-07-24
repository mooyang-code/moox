package store

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/position"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestTransactionRollbackInboxAndOutbox(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "trade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	e = s.Transaction(ctx, func(tx *Tx) error {
		ok, e := tx.InsertInbox("c", "m", "t")
		if e != nil || !ok {
			t.Fatalf("insert: %v %v", ok, e)
		}
		if e = tx.AddOutbox("m", []byte("x")); e != nil {
			t.Fatal(e)
		}
		return ErrConflict
	})
	if e != ErrConflict {
		t.Fatal(e)
	}
	var n int64
	s.db.Table("t_trade_inbox").Count(&n)
	if n != 0 {
		t.Fatalf("inbox rows=%d", n)
	}
}
func TestInboxAndFillIdempotency(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "trade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Transaction(ctx, func(tx *Tx) error {
		a, e := tx.InsertInbox("c", "m", "t")
		if e != nil || !a {
			return e
		}
		b, e := tx.InsertInbox("c", "m", "t")
		if e != nil || b {
			t.Fatal("duplicate inbox applied")
		}
		a, e = tx.InsertFill("s", "f", "ef", "a", "c", "BTCUSDT", "o", "1", "1", "0", "", 0)
		if e != nil || !a {
			return e
		}
		b, e = tx.InsertFill("s", "f2", "ef", "a", "c", "BTCUSDT", "o", "1", "1", "0", "", 0)
		if e != nil || b {
			t.Fatal("duplicate exchange fill applied")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestRecordTradingSignalIsAtomicAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	record := TradingSignalRecord{
		SpaceID: "space", EventID: "event-1", SignalID: "signal-1", StrategyID: "strategy-1",
		Symbol: "BTC-USDT", Side: "SIGNAL_SIDE_BUY", Action: "SIGNAL_ACTION_OPEN",
		TargetPrice: "101.5", SignalTime: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), Tags: `{"source":"test"}`,
	}
	fresh, err := s.RecordTradingSignal(ctx, "trade_trading_signal_v1", record.EventID, "moox.trading.signal.v1.space.subject", record)
	require.NoError(t, err)
	assert.True(t, fresh)
	fresh, err = s.RecordTradingSignal(ctx, "trade_trading_signal_v1", record.EventID, "moox.trading.signal.v1.space.subject", record)
	require.NoError(t, err)
	assert.False(t, fresh)

	var count int64
	require.NoError(t, s.db.Table("t_trade_signal_recommendations").Where("c_space_id=?", record.SpaceID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var got struct {
		SignalID    string `gorm:"column:c_signal_id"`
		Action      string `gorm:"column:c_action"`
		TargetPrice string `gorm:"column:c_target_price"`
		Tags        string `gorm:"column:c_tags"`
	}
	require.NoError(t, s.db.Table("t_trade_signal_recommendations").Where("c_space_id=? AND c_signal_id=?", record.SpaceID, record.SignalID).Take(&got).Error)
	assert.Equal(t, record.SignalID, got.SignalID)
	assert.Equal(t, record.Action, got.Action)
	assert.Equal(t, record.TargetPrice, got.TargetPrice)
	assert.JSONEq(t, record.Tags, got.Tags)
}

func TestRecordTradingSignalTreatsSameSignalWithNewEventAsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	record := TradingSignalRecord{
		SpaceID: "space", EventID: "event-1", SignalID: "signal-1", StrategyID: "strategy-1",
		Symbol: "BTC-USDT", Side: "SIGNAL_SIDE_BUY", Action: "SIGNAL_ACTION_OPEN",
		SignalTime: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), Tags: `{}`,
	}
	fresh, err := s.RecordTradingSignal(ctx, "trade_trading_signal_v1", record.EventID, "subject", record)
	require.NoError(t, err)
	require.True(t, fresh)

	record.EventID = "event-2"
	fresh, err = s.RecordTradingSignal(ctx, "trade_trading_signal_v1", record.EventID, "subject", record)
	require.NoError(t, err)
	assert.True(t, fresh, "the new envelope was recorded in the inbox")

	var count int64
	require.NoError(t, s.db.Table("t_trade_signal_recommendations").Where("c_space_id=? AND c_signal_id=?", record.SpaceID, record.SignalID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestEpochTimeAcceptsSecondsAndMilliseconds(t *testing.T) {
	seconds := int64(1_700_000_000)
	assert.Equal(t, seconds*1000, epochMillis(seconds))
	assert.Equal(t, seconds*1000, epochMillis(seconds*1000))
	assert.Equal(t, time.Unix(seconds, 0).UTC(), epochTime(seconds))
}

func TestListFillsPageUsesExchangeTradeTime(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tradeTime := int64(1_700_000_000_000)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.InsertFill("s", "f", "ef", "a", "c", "BTCUSDT", "o", "1", "1", "0", "", tradeTime)
		return err
	}))

	rows, total, err := s.ListFillsPage(ctx, "s", FillQuery{StartTimeMS: tradeTime / 1000, EndTimeMS: tradeTime, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, tradeTime, rows[0].TradedAtMS)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_Health_EmptyDB_ShouldReportZeroOrders(t *testing.T) {
	s := openTestStore(t)
	stats, err := s.Health(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.OpenOrders)
	assert.Equal(t, int64(0), stats.UnknownOrders)
}

func TestStore_SetControlAndIsPaused_ShouldToggle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.SetControl(ctx, "space-1", ControlRecord{TargetType: "account", TargetID: "acc-1", Paused: true, Reason: "test"}))
	paused, err := s.IsPaused(ctx, "space-1", "acc-1", "")
	require.NoError(t, err)
	assert.True(t, paused)
	require.NoError(t, s.SetControl(ctx, "space-1", ControlRecord{TargetType: "account", TargetID: "acc-1", Paused: false}))
	paused, err = s.IsPaused(ctx, "space-1", "acc-1", "")
	require.NoError(t, err)
	assert.False(t, paused)
}

func TestStore_OrderCRUD_ShouldRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rec := &OrderRecord{
		SpaceID: "space-1", OrderID: "order-1", ClientOrderID: "client-1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		Side: "BUY", Quantity: "1", Price: "100", State: string(order.Ready), ExchangeOrderID: "ex-1", Version: 1,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateOrder(rec) }))
	got, err := s.GetOrder(ctx, "space-1", "order-1")
	require.NoError(t, err)
	assert.Equal(t, "client-1", got.ClientOrderID)

	byClient, err := s.GetOrderByClientID(ctx, "space-1", "client-1")
	require.NoError(t, err)
	assert.Equal(t, "order-1", byClient.OrderID)

	byExchange, err := s.GetOrderByExchangeID(ctx, "space-1", "ex-1")
	require.NoError(t, err)
	assert.Equal(t, "order-1", byExchange.OrderID)

	privateFillOrder, err := s.GetOrderForPrivateFill(ctx, "space-1", "chan-1", "BTC-USDT", "ex-1")
	require.NoError(t, err)
	assert.Equal(t, "order-1", privateFillOrder.OrderID)
}

func TestStore_SagaCRUD_ShouldRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	rec := SagaRecord{SpaceID: "space-1", SagaID: "saga-1", Type: "CANCEL_REPLACE", State: "REQUESTED", OrderID: "o1", Version: 1}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateSaga(rec) }))
	got, err := s.GetSaga(ctx, "space-1", "saga-1")
	require.NoError(t, err)
	assert.Equal(t, "REQUESTED", got.State)
}

func TestStore_ListBalances_AfterLedgerPost_ShouldReturnRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PostLedger("space-1", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed"), BizType: "seed", RefType: "test", RefID: "1",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("100").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100")},
			},
		})
	}))
	rows, err := s.ListBalances(ctx, "space-1", "acct-1")
	require.NoError(t, err)
	require.NotEmpty(t, rows)
}

func TestStore_EnqueueOutbox_ShouldPersist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnqueueOutbox(ctx, "msg-1", []byte("event-data")))
	var count int64
	s.DBForTest().Table("t_trade_outbox").Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestStore_ListOpenOrders_ShouldFilterByState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, st := range []string{string(order.Open), string(order.Canceled)} {
		rec := OrderRecord{
			SpaceID: "space-1", OrderID: "order-" + st, ClientOrderID: "client-" + st,
			AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
			Side: "BUY", Quantity: "1", Price: "100", State: st, Version: 1,
		}
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateOrder(&rec) }))
	}
	open, err := s.ListOpenOrders(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, open, 1)
	assert.Equal(t, string(order.Open), open[0].State)
}

func TestStore_ListRecoverableOrders_ShouldReturnRecoverableStates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, st := range []string{string(order.Ready), string(order.Submitting), string(order.Filled)} {
		rec := OrderRecord{
			SpaceID: "space-1", OrderID: "recover-" + st, ClientOrderID: "client-" + st,
			AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
			Side: "BUY", Quantity: "1", Price: "100", State: st, Version: 1,
		}
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateOrder(&rec) }))
	}

	rows, err := s.ListRecoverableOrders(ctx, 0)

	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.NotEqual(t, string(order.Filled), rows[0].State)
}

func TestStore_ListPositions_AfterApplyPosition_ShouldFilterBySymbol(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fill := position.Fill{Side: "BUY", Quantity: shared.MustDecimal("2"), Price: shared.MustDecimal("100")}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.ApplyPosition("space-1", "acct-1", "BTC-USDT", fill)
	}))

	allRows, err := s.ListPositions(ctx, "space-1", "acct-1", "")
	require.NoError(t, err)
	require.Len(t, allRows, 1)
	assert.Equal(t, "BTC-USDT", allRows[0].Symbol)

	filtered, err := s.ListPositions(ctx, "space-1", "acct-1", "BTC-USDT")
	require.NoError(t, err)
	require.Len(t, filtered, 1)
}

func TestStore_ListRecoverableSagas_ShouldReturnActiveSagaStates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	recoverable := SagaRecord{SpaceID: "space-1", SagaID: "saga-1", Type: "CANCEL_REPLACE", State: "CANCEL_UNKNOWN", OrderID: "old-1", Version: 1}
	done := SagaRecord{SpaceID: "space-1", SagaID: "saga-2", Type: "CANCEL_REPLACE", State: "COMPLETED", OrderID: "old-2", Version: 1}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateSaga(recoverable); err != nil {
			return err
		}
		return tx.CreateSaga(done)
	}))

	rows, err := s.ListRecoverableSagas(ctx, 0)

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "saga-1", rows[0].SagaID)
}

func TestStore_ClaimReleaseAndMarkOutbox_ShouldUpdateLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.EnqueueOutbox(ctx, "msg-1", []byte("event-data")))

	claimed, err := s.ClaimOutbox(ctx, 0, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, "msg-1", claimed[0].MessageID)

	require.NoError(t, s.ReleaseOutbox(ctx, claimed[0].ID, "retry later"))
	reclaimed, err := s.ClaimOutbox(ctx, 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.NoError(t, s.MarkOutboxPublished(ctx, reclaimed[0].ID))

	empty, err := s.ClaimOutbox(ctx, 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestSplitSQL_SkipsCommentsAndEmptyLines(t *testing.T) {
	got := splitSQL("-- comment\nCREATE TABLE t (id INT);\n\nINSERT INTO t VALUES (1);")
	assert.Len(t, got, 2)
}
