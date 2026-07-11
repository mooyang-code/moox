package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/position"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/observability"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

var ErrConflict = errors.New("trade: store conflict")

type Store struct{ db *gorm.DB }
type Tx struct {
	db  *gorm.DB
	ctx context.Context
}
type OutboxRecord struct {
	ID                 int64
	MessageID, Topic   string
	Payload            []byte
	Attempts           int
	TraceID, RequestID string
}
type SagaRecord struct {
	SpaceID, SagaID, Type, State, OrderID, ReplacementOrderID, Payload, LastError string
	Version                                                                       uint64
}
type RebalanceRunRecord struct {
	SpaceID, RunID, AccountID, ChannelID, IdempotencyKey, MarketSnapshotID, PositionSnapshotID, RulesVersion, AlgorithmName, AlgorithmVersion, Status, Residual string
	Version                                                                                                                                                     uint64
}
type RebalanceLegRecord struct {
	SpaceID, RunID, LegID, Symbol, MarketType, BaseAsset, QuoteAsset, Side, Action, Quantity, Price, PlanID, Status string
	ReduceOnly                                                                                                      bool
	Sequence                                                                                                        int
	DependsOn                                                                                                       []int
}
type ControlRecord struct {
	TargetType, TargetID string
	Paused               bool
	Reason               string
}
type HealthStats struct {
	OpenOrders, UnknownOrders, PendingOutbox int64
	OldestOutbox                             time.Time
}
type BalanceRecord struct {
	AccountID, Asset, Bucket, Amount string
	Version                          uint64
}
type PositionRecord struct {
	AccountID, Symbol, Quantity, AveragePrice, RealizedPnL string
	Version                                                uint64
}
type FillRecord struct{ FillID, ExchangeTradeID, OrderID, Quantity, Price, Fee, FeeAsset string }

var claimSequence atomic.Uint64

func Open(path string) (*Store, error) {
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	for _, stmt := range splitSQL(schema.AllSQL()) {
		if _, err = sqlDB.Exec(stmt); err != nil {
			return nil, fmt.Errorf("apply trade schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error {
	db, e := s.db.DB()
	if e != nil {
		return e
	}
	return db.Close()
}
func (s *Store) Transaction(ctx context.Context, fn func(*Tx) error) error {
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error { return fn(&Tx{db: db, ctx: ctx}) })
}
func (s *Store) DBForTest() *gorm.DB { return s.db }

func splitSQL(raw string) []string {
	var out []string
	start := 0
	inTrigger := false
	lines := splitLines(raw)
	var buf string
	for _, line := range lines {
		trim := trimSpace(line)
		if hasPrefix(trim, "CREATE TRIGGER") {
			inTrigger = true
		}
		buf += line + "\n"
		if inTrigger {
			if trim == "END;" || hasSuffix(trim, "END;") {
				out = append(out, buf)
				buf = ""
				inTrigger = false
			}
			continue
		}
		for i, c := range buf {
			if c == ';' {
				out = append(out, buf[:i+1])
				buf = buf[i+1:]
				start++
				break
			}
		}
	}
	if trimSpace(buf) != "" {
		out = append(out, buf)
	}
	_ = start
	return out
}
func splitLines(s string) []string {
	var out []string
	for len(s) > 0 {
		i := indexByte(s, '\n')
		if i < 0 {
			return append(out, s)
		}
		out = append(out, s[:i])
		s = s[i+1:]
	}
	return out
}
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r' || s[0] == '\n') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}
func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func hasSuffix(s, p string) bool { return len(s) >= len(p) && s[len(s)-len(p):] == p }
func indexByte(s string, b byte) int {
	for i := range s {
		if s[i] == b {
			return i
		}
	}
	return -1
}

type OrderRecord struct {
	SpaceID, OrderID, ClientOrderID, AccountID, ChannelID, Symbol, MarketType, BaseAsset, QuoteAsset, Side, Quantity, Price, FilledQuantity, State, ExchangeOrderID, ReservedAsset, ReservedAmount, ConsumedReserved string
	ReduceOnly                                                                                                                                                                                                       bool
	Version                                                                                                                                                                                                          uint64
}

func (*OrderRecord) TableName() string { return "t_trade_order_aggregates" }
func (t *Tx) CreateOrder(v *OrderRecord) error {
	r := map[string]any{"c_space_id": v.SpaceID, "c_order_id": v.OrderID, "c_client_order_id": v.ClientOrderID, "c_account_id": v.AccountID, "c_channel_id": v.ChannelID, "c_symbol": v.Symbol, "c_market_type": v.MarketType, "c_base_asset": v.BaseAsset, "c_quote_asset": v.QuoteAsset, "c_side": v.Side, "c_quantity": v.Quantity, "c_price": v.Price, "c_reduce_only": v.ReduceOnly, "c_reserved_asset": v.ReservedAsset, "c_reserved_amount": v.ReservedAmount, "c_consumed_reserved": v.ConsumedReserved, "c_filled_quantity": v.FilledQuantity, "c_state": v.State, "c_exchange_order_id": v.ExchangeOrderID, "c_version": v.Version}
	if e := t.db.Table("t_trade_order_aggregates").Create(r).Error; e != nil {
		return ErrConflict
	}
	return nil
}
func (s *Store) GetOrder(ctx context.Context, space, id string) (OrderRecord, error) {
	return getOrder(s.db.WithContext(ctx), "c_order_id", space, id)
}
func (s *Store) GetOrderByClientID(ctx context.Context, space, id string) (OrderRecord, error) {
	return getOrder(s.db.WithContext(ctx), "c_client_order_id", space, id)
}
func (s *Store) GetOrderByExchangeID(ctx context.Context, space, id string) (OrderRecord, error) {
	return getOrder(s.db.WithContext(ctx), "c_exchange_order_id", space, id)
}
func (s *Store) GetOrderForPrivateFill(ctx context.Context, space, channel, symbol, exchangeID string) (OrderRecord, error) {
	var id string
	err := s.db.WithContext(ctx).Raw("SELECT c_order_id FROM t_trade_order_aggregates WHERE c_space_id=? AND c_channel_id=? AND c_symbol=? AND c_exchange_order_id=? LIMIT 1", space, channel, symbol, exchangeID).Scan(&id).Error
	if err != nil {
		return OrderRecord{}, err
	}
	if id == "" {
		return OrderRecord{}, gorm.ErrRecordNotFound
	}
	return s.GetOrder(ctx, space, id)
}
func (s *Store) SetControl(ctx context.Context, space string, control ControlRecord) error {
	return s.db.WithContext(ctx).Exec("INSERT INTO t_trade_controls(c_space_id,c_target_type,c_target_id,c_paused,c_reason,c_mtime) VALUES(?,?,?,?,?,?) ON CONFLICT(c_space_id,c_target_type,c_target_id) DO UPDATE SET c_paused=excluded.c_paused,c_reason=excluded.c_reason,c_mtime=excluded.c_mtime", space, control.TargetType, control.TargetID, control.Paused, control.Reason, time.Now().UTC()).Error
}
func (s *Store) IsPaused(ctx context.Context, space, accountID, channelID string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Raw("SELECT COUNT(1) FROM t_trade_controls WHERE c_space_id=? AND c_paused=1 AND ((c_target_type='account' AND c_target_id=?) OR (c_target_type='channel' AND c_target_id=?))", space, accountID, channelID).Scan(&count).Error
	return count > 0, err
}
func (s *Store) Health(ctx context.Context) (HealthStats, error) {
	var stats HealthStats
	if err := s.db.WithContext(ctx).Exec("SELECT 1").Error; err != nil {
		return stats, err
	}
	if err := s.db.WithContext(ctx).Raw("SELECT COUNT(1) FROM t_trade_order_aggregates WHERE c_state IN ('OPEN','PARTIALLY_FILLED','SUBMITTING','SUBMIT_UNKNOWN','CANCELING','CANCEL_UNKNOWN')").Scan(&stats.OpenOrders).Error; err != nil {
		return stats, err
	}
	if err := s.db.WithContext(ctx).Raw("SELECT COUNT(1) FROM t_trade_order_aggregates WHERE c_state IN ('SUBMIT_UNKNOWN','CANCEL_UNKNOWN')").Scan(&stats.UnknownOrders).Error; err != nil {
		return stats, err
	}
	var outbox struct {
		Count  int64      `gorm:"column:c_count"`
		Oldest *time.Time `gorm:"column:c_oldest"`
	}
	if err := s.db.WithContext(ctx).Raw("SELECT COUNT(1) c_count, MIN(c_ctime) c_oldest FROM t_trade_outbox WHERE c_published_at IS NULL").Scan(&outbox).Error; err != nil {
		return stats, err
	}
	stats.PendingOutbox = outbox.Count
	if outbox.Oldest != nil {
		stats.OldestOutbox = outbox.Oldest.UTC()
	}
	return stats, nil
}
func (s *Store) ListRecoverableOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var refs []struct {
		SpaceID string `gorm:"column:c_space_id"`
		OrderID string `gorm:"column:c_order_id"`
	}
	if err := s.db.WithContext(ctx).Raw("SELECT c_space_id,c_order_id FROM t_trade_order_aggregates WHERE c_state IN ('READY','SUBMITTING','SUBMIT_UNKNOWN','CANCELING','CANCEL_UNKNOWN') ORDER BY c_mtime LIMIT ?", limit).Scan(&refs).Error; err != nil {
		return nil, err
	}
	out := make([]OrderRecord, 0, len(refs))
	for _, r := range refs {
		v, e := s.GetOrder(ctx, r.SpaceID, r.OrderID)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) ListOpenOrders(ctx context.Context, limit int) ([]OrderRecord, error) {
	return s.ListOpenOrdersScoped(ctx, "", "", "", limit)
}
func (s *Store) ListOpenOrdersScoped(ctx context.Context, space, account, channel string, limit int) ([]OrderRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	var refs []struct {
		SpaceID string `gorm:"column:c_space_id"`
		OrderID string `gorm:"column:c_order_id"`
	}
	query := "SELECT c_space_id,c_order_id FROM t_trade_order_aggregates WHERE c_state IN ('OPEN','PARTIALLY_FILLED','SUBMIT_UNKNOWN','CANCELING','CANCEL_UNKNOWN')"
	args := []any{}
	if space != "" {
		query += " AND c_space_id=?"
		args = append(args, space)
	}
	if account != "" {
		query += " AND c_account_id=?"
		args = append(args, account)
	}
	if channel != "" {
		query += " AND c_channel_id=?"
		args = append(args, channel)
	}
	query += " ORDER BY c_mtime LIMIT ?"
	args = append(args, limit)
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&refs).Error; err != nil {
		return nil, err
	}
	out := make([]OrderRecord, 0, len(refs))
	for _, r := range refs {
		v, e := s.GetOrder(ctx, r.SpaceID, r.OrderID)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (t *Tx) GetOrder(ctx context.Context, space, id string) (OrderRecord, error) {
	return getOrder(t.db.WithContext(ctx), "c_order_id", space, id)
}
func getOrder(db *gorm.DB, column, space, id string) (OrderRecord, error) {
	var r struct {
		SpaceID          string `gorm:"column:c_space_id"`
		OrderID          string `gorm:"column:c_order_id"`
		ClientOrderID    string `gorm:"column:c_client_order_id"`
		AccountID        string `gorm:"column:c_account_id"`
		ChannelID        string `gorm:"column:c_channel_id"`
		Symbol           string `gorm:"column:c_symbol"`
		MarketType       string `gorm:"column:c_market_type"`
		BaseAsset        string `gorm:"column:c_base_asset"`
		QuoteAsset       string `gorm:"column:c_quote_asset"`
		Side             string `gorm:"column:c_side"`
		Quantity         string `gorm:"column:c_quantity"`
		Price            string `gorm:"column:c_price"`
		FilledQuantity   string `gorm:"column:c_filled_quantity"`
		ReduceOnly       bool   `gorm:"column:c_reduce_only"`
		ReservedAsset    string `gorm:"column:c_reserved_asset"`
		ReservedAmount   string `gorm:"column:c_reserved_amount"`
		ConsumedReserved string `gorm:"column:c_consumed_reserved"`
		State            string `gorm:"column:c_state"`
		ExchangeOrderID  string `gorm:"column:c_exchange_order_id"`
		Version          uint64 `gorm:"column:c_version"`
	}
	e := db.Table("t_trade_order_aggregates").Where("c_space_id=? AND "+column+"=?", space, id).Take(&r).Error
	return OrderRecord{SpaceID: r.SpaceID, OrderID: r.OrderID, ClientOrderID: r.ClientOrderID, AccountID: r.AccountID, ChannelID: r.ChannelID, Symbol: r.Symbol, MarketType: r.MarketType, BaseAsset: r.BaseAsset, QuoteAsset: r.QuoteAsset, Side: r.Side, Quantity: r.Quantity, Price: r.Price, ReduceOnly: r.ReduceOnly, ReservedAsset: r.ReservedAsset, ReservedAmount: r.ReservedAmount, ConsumedReserved: r.ConsumedReserved, FilledQuantity: r.FilledQuantity, State: r.State, ExchangeOrderID: r.ExchangeOrderID, Version: r.Version}, e
}
func (s *Store) ListOrders(ctx context.Context, space, account, channel, symbol string, openOnly bool) ([]OrderRecord, error) {
	query := "SELECT c_order_id FROM t_trade_order_aggregates WHERE c_space_id=?"
	args := []any{space}
	if account != "" {
		query += " AND c_account_id=?"
		args = append(args, account)
	}
	if channel != "" {
		query += " AND c_channel_id=?"
		args = append(args, channel)
	}
	if symbol != "" {
		query += " AND c_symbol=?"
		args = append(args, symbol)
	}
	if openOnly {
		query += " AND c_state NOT IN ('FILLED','CANCELED','PARTIALLY_CANCELED','REJECTED','EXPIRED')"
	}
	query += " ORDER BY c_id DESC"
	var refs []struct {
		OrderID string `gorm:"column:c_order_id"`
	}
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&refs).Error; err != nil {
		return nil, err
	}
	out := make([]OrderRecord, 0, len(refs))
	for _, r := range refs {
		v, e := s.GetOrder(ctx, space, r.OrderID)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) ListBalances(ctx context.Context, space, account string) ([]BalanceRecord, error) {
	var rows []struct {
		AccountID string `gorm:"column:c_account_id"`
		Asset     string `gorm:"column:c_asset"`
		Bucket    string `gorm:"column:c_bucket"`
		Amount    string `gorm:"column:c_amount"`
		Version   uint64 `gorm:"column:c_version"`
	}
	e := s.db.WithContext(ctx).Raw("SELECT c_account_id,c_asset,c_bucket,c_amount,c_version FROM t_trade_balance_projections WHERE c_space_id=? AND c_account_id=? ORDER BY c_asset,c_bucket", space, account).Scan(&rows).Error
	out := make([]BalanceRecord, len(rows))
	for i, r := range rows {
		out[i] = BalanceRecord{r.AccountID, r.Asset, r.Bucket, r.Amount, r.Version}
	}
	return out, e
}
func (s *Store) ReconcileBalances(ctx context.Context, space, account string, desired map[string]map[string]shared.Decimal) error {
	currentRows, err := s.ListBalances(ctx, space, account)
	if err != nil {
		return err
	}
	current := map[string]map[string]shared.Decimal{}
	for _, r := range currentRows {
		if current[r.Asset] == nil {
			current[r.Asset] = map[string]shared.Decimal{}
		}
		v, e := shared.ParseDecimal(r.Amount)
		if e != nil {
			return e
		}
		current[r.Asset][r.Bucket] = v
	}
	assets := make(map[string]struct{}, len(desired)+len(current))
	for asset := range desired {
		assets[asset] = struct{}{}
	}
	for asset := range current {
		assets[asset] = struct{}{}
	}
	for asset := range assets {
		buckets := desired[asset]
		total := buckets["available"].Add(buckets["frozen"])
		localFrozen := current[asset]["frozen"]
		localMargin := current[asset]["margin"]
		available := total.Sub(localFrozen).Sub(localMargin)
		if available.IsNegative() {
			return fmt.Errorf("trade: exchange total %s is below local locked funds %s for %s", total.String(), localFrozen.Add(localMargin).String(), asset)
		}
		desired[asset] = map[string]shared.Decimal{"available": available, "frozen": localFrozen, "margin": localMargin}
	}
	return s.Transaction(ctx, func(tx *Tx) error {
		seq := 0
		for asset, buckets := range desired {
			for _, bucket := range []string{"available", "frozen", "margin"} {
				want := buckets[bucket]
				have := current[asset][bucket]
				delta := want.Sub(have)
				if delta.IsZero() {
					continue
				}
				seq++
				ref := fmt.Sprintf("%s:%s:%s:%d", account, asset, bucket, time.Now().UnixNano()+int64(seq))
				p := ledger.Transaction{ID: shared.LedgerTransactionID("reconcile:" + ref), BizType: "reconcile", RefType: "balance_snapshot", RefID: ref, Entries: []ledger.Entry{{AccountID: "exchange-reconciliation", Asset: asset, Bucket: "reconciliation", Amount: delta.Neg()}, {AccountID: account, Asset: asset, Bucket: bucket, Amount: delta}}}
				if err := tx.PostLedger(space, p); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
func (s *Store) ListPositions(ctx context.Context, space, account, symbol string) ([]PositionRecord, error) {
	q := "SELECT c_account_id,c_symbol,c_quantity,c_average_price,c_realized_pnl,c_version FROM t_trade_position_projections WHERE c_space_id=? AND c_account_id=?"
	args := []any{space, account}
	if symbol != "" {
		q += " AND c_symbol=?"
		args = append(args, symbol)
	}
	var rows []struct {
		AccountID    string `gorm:"column:c_account_id"`
		Symbol       string `gorm:"column:c_symbol"`
		Quantity     string `gorm:"column:c_quantity"`
		AveragePrice string `gorm:"column:c_average_price"`
		RealizedPnL  string `gorm:"column:c_realized_pnl"`
		Version      uint64 `gorm:"column:c_version"`
	}
	e := s.db.WithContext(ctx).Raw(q, args...).Scan(&rows).Error
	out := make([]PositionRecord, len(rows))
	for i, r := range rows {
		out[i] = PositionRecord{r.AccountID, r.Symbol, r.Quantity, r.AveragePrice, r.RealizedPnL, r.Version}
	}
	return out, e
}
func (s *Store) ListFills(ctx context.Context, space, orderID string) ([]FillRecord, error) {
	q := "SELECT c_fill_id,c_exchange_trade_id,c_order_id,c_quantity,c_price,c_fee,c_fee_asset FROM t_trade_fill_events WHERE c_space_id=?"
	args := []any{space}
	if orderID != "" {
		q += " AND c_order_id=?"
		args = append(args, orderID)
	}
	var rows []struct {
		FillID          string `gorm:"column:c_fill_id"`
		ExchangeTradeID string `gorm:"column:c_exchange_trade_id"`
		OrderID         string `gorm:"column:c_order_id"`
		Quantity        string `gorm:"column:c_quantity"`
		Price           string `gorm:"column:c_price"`
		Fee             string `gorm:"column:c_fee"`
		FeeAsset        string `gorm:"column:c_fee_asset"`
	}
	e := s.db.WithContext(ctx).Raw(q, args...).Scan(&rows).Error
	out := make([]FillRecord, len(rows))
	for i, r := range rows {
		out[i] = FillRecord{r.FillID, r.ExchangeTradeID, r.OrderID, r.Quantity, r.Price, r.Fee, r.FeeAsset}
	}
	return out, e
}
func (t *Tx) UpdateOrder(v OrderRecord, expected uint64) error {
	res := t.db.Table("t_trade_order_aggregates").Where("c_space_id=? AND c_order_id=? AND c_version=?", v.SpaceID, v.OrderID, expected).Updates(map[string]any{"c_filled_quantity": v.FilledQuantity, "c_consumed_reserved": v.ConsumedReserved, "c_state": v.State, "c_exchange_order_id": v.ExchangeOrderID, "c_version": v.Version, "c_mtime": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func (t *Tx) InsertInbox(consumer, id, topic string) (bool, error) {
	res := t.db.Exec("INSERT OR IGNORE INTO t_trade_inbox(c_consumer,c_message_id,c_topic) VALUES(?,?,?)", consumer, id, topic)
	return res.RowsAffected == 1, res.Error
}
func (s *Store) RecordInbox(ctx context.Context, consumer, id, topic string) (bool, error) {
	var fresh bool
	err := s.Transaction(ctx, func(tx *Tx) error { var e error; fresh, e = tx.InsertInbox(consumer, id, topic); return e })
	return fresh, err
}
func (t *Tx) AddOutbox(id, topic string, payload []byte) error {
	trace := observability.TraceFromContext(t.ctx)
	return t.db.Exec("INSERT INTO t_trade_outbox(c_message_id,c_topic,c_payload,c_trace_id,c_request_id) VALUES(?,?,?,?,?)", id, topic, payload, trace.TraceID, trace.RequestID).Error
}
func (s *Store) EnqueueOutbox(ctx context.Context, id, topic string, payload []byte) error {
	return s.Transaction(ctx, func(tx *Tx) error { return tx.AddOutbox(id, topic, payload) })
}
func (t *Tx) CreateSaga(s SagaRecord) error {
	return t.db.Exec("INSERT INTO t_trade_sagas(c_space_id,c_saga_id,c_type,c_state,c_order_id,c_replacement_order_id,c_payload,c_version,c_last_error) VALUES(?,?,?,?,?,?,?,?,?)", s.SpaceID, s.SagaID, s.Type, s.State, s.OrderID, s.ReplacementOrderID, s.Payload, s.Version, s.LastError).Error
}
func (s *Store) GetSaga(ctx context.Context, space, id string) (SagaRecord, error) {
	var r struct {
		SpaceID            string `gorm:"column:c_space_id"`
		SagaID             string `gorm:"column:c_saga_id"`
		Type               string `gorm:"column:c_type"`
		State              string `gorm:"column:c_state"`
		OrderID            string `gorm:"column:c_order_id"`
		ReplacementOrderID string `gorm:"column:c_replacement_order_id"`
		Payload            string `gorm:"column:c_payload"`
		Version            uint64 `gorm:"column:c_version"`
		LastError          string `gorm:"column:c_last_error"`
	}
	e := s.db.WithContext(ctx).Raw("SELECT c_space_id,c_saga_id,c_type,c_state,c_order_id,c_replacement_order_id,c_payload,c_version,c_last_error FROM t_trade_sagas WHERE c_space_id=? AND c_saga_id=?", space, id).Scan(&r).Error
	if e == nil && r.SagaID == "" {
		e = gorm.ErrRecordNotFound
	}
	return SagaRecord{r.SpaceID, r.SagaID, r.Type, r.State, r.OrderID, r.ReplacementOrderID, r.Payload, r.LastError, r.Version}, e
}
func (s *Store) GetSagaByReplacementOrder(ctx context.Context, space, orderID string) (SagaRecord, error) {
	var id string
	if err := s.db.WithContext(ctx).Raw("SELECT c_saga_id FROM t_trade_sagas WHERE c_space_id=? AND c_replacement_order_id=? LIMIT 1", space, orderID).Scan(&id).Error; err != nil {
		return SagaRecord{}, err
	}
	if id == "" {
		return SagaRecord{}, gorm.ErrRecordNotFound
	}
	return s.GetSaga(ctx, space, id)
}
func (s *Store) ListRecoverableSagas(ctx context.Context, limit int) ([]SagaRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var refs []struct {
		SpaceID string `gorm:"column:c_space_id"`
		SagaID  string `gorm:"column:c_saga_id"`
	}
	if err := s.db.WithContext(ctx).Raw("SELECT c_space_id,c_saga_id FROM t_trade_sagas WHERE c_state IN ('CANCEL_REQUESTED','CANCEL_UNKNOWN','REPLACEMENT_CREATED','REPLACEMENT_SUBMIT_UNKNOWN') ORDER BY c_mtime LIMIT ?", limit).Scan(&refs).Error; err != nil {
		return nil, err
	}
	out := make([]SagaRecord, 0, len(refs))
	for _, r := range refs {
		v, e := s.GetSaga(ctx, r.SpaceID, r.SagaID)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (t *Tx) UpdateSaga(s SagaRecord, expected uint64) error {
	r := t.db.Exec("UPDATE t_trade_sagas SET c_state=?,c_replacement_order_id=?,c_version=?,c_last_error=?,c_mtime=? WHERE c_space_id=? AND c_saga_id=? AND c_version=?", s.State, s.ReplacementOrderID, s.Version, s.LastError, time.Now().UTC(), s.SpaceID, s.SagaID, expected)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}
func (t *Tx) CreateRebalance(run RebalanceRunRecord, legs []RebalanceLegRecord) error {
	if err := t.db.Exec("INSERT INTO t_rebalance_runs(c_space_id,c_run_id,c_account_id,c_channel_id,c_idempotency_key,c_market_snapshot_id,c_position_snapshot_id,c_rules_version,c_algorithm_name,c_algorithm_version,c_status,c_version,c_residual) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", run.SpaceID, run.RunID, run.AccountID, run.ChannelID, run.IdempotencyKey, run.MarketSnapshotID, run.PositionSnapshotID, run.RulesVersion, run.AlgorithmName, run.AlgorithmVersion, run.Status, run.Version, run.Residual).Error; err != nil {
		return err
	}
	for _, l := range legs {
		deps, _ := json.Marshal(l.DependsOn)
		if err := t.db.Exec("INSERT INTO t_rebalance_legs(c_space_id,c_run_id,c_leg_id,c_symbol,c_market_type,c_base_asset,c_quote_asset,c_side,c_action,c_quantity,c_price,c_reduce_only,c_sequence,c_depends_on,c_plan_id,c_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", l.SpaceID, l.RunID, l.LegID, l.Symbol, l.MarketType, l.BaseAsset, l.QuoteAsset, l.Side, l.Action, l.Quantity, l.Price, l.ReduceOnly, l.Sequence, string(deps), l.PlanID, l.Status).Error; err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) ListActiveRebalanceRuns(ctx context.Context, limit int) ([]RebalanceRunRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []struct {
		SpaceID   string `gorm:"column:c_space_id"`
		RunID     string `gorm:"column:c_run_id"`
		AccountID string `gorm:"column:c_account_id"`
		ChannelID string `gorm:"column:c_channel_id"`
	}
	e := s.db.WithContext(ctx).Raw("SELECT c_space_id,c_run_id,c_account_id,c_channel_id FROM t_rebalance_runs WHERE c_status IN ('PLANNED','EXECUTING') ORDER BY c_mtime LIMIT ?", limit).Scan(&rows).Error
	out := make([]RebalanceRunRecord, len(rows))
	for i, r := range rows {
		out[i] = RebalanceRunRecord{SpaceID: r.SpaceID, RunID: r.RunID, AccountID: r.AccountID, ChannelID: r.ChannelID}
	}
	return out, e
}
func (s *Store) ListRebalanceLegs(ctx context.Context, space, runID string) ([]RebalanceLegRecord, error) {
	var rows []struct {
		SpaceID    string `gorm:"column:c_space_id"`
		RunID      string `gorm:"column:c_run_id"`
		LegID      string `gorm:"column:c_leg_id"`
		Symbol     string `gorm:"column:c_symbol"`
		MarketType string `gorm:"column:c_market_type"`
		BaseAsset  string `gorm:"column:c_base_asset"`
		QuoteAsset string `gorm:"column:c_quote_asset"`
		Side       string `gorm:"column:c_side"`
		Action     string `gorm:"column:c_action"`
		Quantity   string `gorm:"column:c_quantity"`
		Price      string `gorm:"column:c_price"`
		ReduceOnly bool   `gorm:"column:c_reduce_only"`
		Sequence   int    `gorm:"column:c_sequence"`
		DependsOn  string `gorm:"column:c_depends_on"`
		PlanID     string `gorm:"column:c_plan_id"`
		Status     string `gorm:"column:c_status"`
	}
	err := s.db.WithContext(ctx).Raw("SELECT * FROM t_rebalance_legs WHERE c_space_id=? AND c_run_id=? ORDER BY c_sequence", space, runID).Scan(&rows).Error
	out := make([]RebalanceLegRecord, len(rows))
	for i, r := range rows {
		var deps []int
		_ = json.Unmarshal([]byte(r.DependsOn), &deps)
		out[i] = RebalanceLegRecord{SpaceID: r.SpaceID, RunID: r.RunID, LegID: r.LegID, Symbol: r.Symbol, MarketType: r.MarketType, BaseAsset: r.BaseAsset, QuoteAsset: r.QuoteAsset, Side: r.Side, Action: r.Action, Quantity: r.Quantity, Price: r.Price, ReduceOnly: r.ReduceOnly, Sequence: r.Sequence, DependsOn: deps, PlanID: r.PlanID, Status: r.Status}
	}
	return out, err
}
func (t *Tx) UpdateRebalanceLeg(space, legID, status, planID string) error {
	return t.db.Exec("UPDATE t_rebalance_legs SET c_status=?,c_plan_id=? WHERE c_space_id=? AND c_leg_id=?", status, planID, space, legID).Error
}
func (t *Tx) UpdateRebalanceRun(space, runID, status, residual string) error {
	return t.db.Exec("UPDATE t_rebalance_runs SET c_status=?,c_residual=?,c_version=c_version+1,c_mtime=? WHERE c_space_id=? AND c_run_id=?", status, residual, time.Now().UTC(), space, runID).Error
}
func (s *Store) ClaimOutbox(ctx context.Context, limit int, lease time.Duration) ([]OutboxRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []struct {
		ID        int64  `gorm:"column:c_id"`
		MessageID string `gorm:"column:c_message_id"`
		Topic     string `gorm:"column:c_topic"`
		Payload   []byte `gorm:"column:c_payload"`
		Attempts  int    `gorm:"column:c_attempts"`
		TraceID   string `gorm:"column:c_trace_id"`
		RequestID string `gorm:"column:c_request_id"`
	}
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), claimSequence.Add(1))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if e := tx.Exec("UPDATE t_trade_outbox SET c_lease_until=?,c_attempts=c_attempts+1,c_claim_token=? WHERE c_id IN (SELECT c_id FROM t_trade_outbox WHERE c_status='PENDING' AND (c_lease_until IS NULL OR c_lease_until<?) ORDER BY c_id LIMIT ?)", now.Add(lease), token, now, limit).Error; e != nil {
			return e
		}
		return tx.Raw("SELECT c_id,c_message_id,c_topic,c_payload,c_attempts,c_trace_id,c_request_id FROM t_trade_outbox WHERE c_claim_token=? ORDER BY c_id", token).Scan(&rows).Error
	})
	out := make([]OutboxRecord, len(rows))
	for i, r := range rows {
		out[i] = OutboxRecord{ID: r.ID, MessageID: r.MessageID, Topic: r.Topic, Payload: r.Payload, Attempts: r.Attempts + 1, TraceID: r.TraceID, RequestID: r.RequestID}
	}
	return out, err
}
func (s *Store) MarkOutboxPublished(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Exec("UPDATE t_trade_outbox SET c_status='PUBLISHED',c_published_at=?,c_lease_until=NULL,c_claim_token='' WHERE c_id=?", time.Now().UTC(), id).Error
}
func (s *Store) ReleaseOutbox(ctx context.Context, id int64, msg string) error {
	return s.db.WithContext(ctx).Exec("UPDATE t_trade_outbox SET c_lease_until=NULL,c_claim_token='',c_last_error=? WHERE c_id=?", msg, id).Error
}
func (t *Tx) InsertFill(space, fillID, exchangeID, account, channel, symbol, orderID, qty, price, fee, feeAsset string) (bool, error) {
	res := t.db.Exec("INSERT OR IGNORE INTO t_trade_fill_events(c_space_id,c_fill_id,c_exchange_trade_id,c_account_id,c_channel_id,c_symbol,c_order_id,c_quantity,c_price,c_fee,c_fee_asset) VALUES(?,?,?,?,?,?,?,?,?,?,?)", space, fillID, exchangeID, account, channel, symbol, orderID, qty, price, fee, feeAsset)
	return res.RowsAffected == 1, res.Error
}
func (t *Tx) PostLedger(space string, posting ledger.Transaction) error {
	if err := posting.Validate(); err != nil {
		return err
	}
	res := t.db.Exec("INSERT OR IGNORE INTO t_ledger_transactions(c_space_id,c_transaction_id,c_biz_type,c_ref_type,c_ref_id) VALUES(?,?,?,?,?)", space, posting.ID, posting.BizType, posting.RefType, posting.RefID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}
	for _, e := range posting.Entries {
		if err := t.db.Exec("INSERT INTO t_ledger_entries(c_space_id,c_transaction_id,c_account_id,c_asset,c_bucket,c_amount) VALUES(?,?,?,?,?,?)", space, posting.ID, e.AccountID, e.Asset, e.Bucket, e.Amount.String()).Error; err != nil {
			return err
		}
		var current string
		q := t.db.Raw("SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id=? AND c_account_id=? AND c_asset=? AND c_bucket=?", space, e.AccountID, e.Asset, e.Bucket).Scan(&current)
		if q.Error != nil {
			return q.Error
		}
		amount := shared.Zero()
		if current != "" {
			var parseErr error
			amount, parseErr = shared.ParseDecimal(current)
			if parseErr != nil {
				return parseErr
			}
		}
		amount = amount.Add(e.Amount)
		if !strings.HasPrefix(e.AccountID, "exchange-") && (e.Bucket == "available" || e.Bucket == "frozen") && amount.IsNegative() {
			return fmt.Errorf("trade: insufficient %s balance for %s", e.Asset, e.AccountID)
		}
		if err := t.db.Exec("INSERT INTO t_trade_balance_projections(c_space_id,c_account_id,c_asset,c_bucket,c_amount) VALUES(?,?,?,?,?) ON CONFLICT(c_space_id,c_account_id,c_asset,c_bucket) DO UPDATE SET c_amount=excluded.c_amount,c_version=c_version+1", space, e.AccountID, e.Asset, e.Bucket, amount.String()).Error; err != nil {
			return err
		}
	}
	return nil
}
func (t *Tx) ApplyPosition(space, account, symbol string, f position.Fill) error {
	var row struct {
		Quantity     string `gorm:"column:c_quantity"`
		AveragePrice string `gorm:"column:c_average_price"`
		RealizedPnL  string `gorm:"column:c_realized_pnl"`
		Version      uint64 `gorm:"column:c_version"`
	}
	err := t.db.Raw("SELECT c_quantity,c_average_price,c_realized_pnl,c_version FROM t_trade_position_projections WHERE c_space_id=? AND c_account_id=? AND c_symbol=?", space, account, symbol).Scan(&row).Error
	if err != nil {
		return err
	}
	p := position.Position{Symbol: symbol, Quantity: shared.Zero(), AveragePrice: shared.Zero(), RealizedPnL: shared.Zero(), Version: row.Version}
	if row.Quantity != "" {
		if p.Quantity, err = shared.ParseDecimal(row.Quantity); err != nil {
			return err
		}
		if p.AveragePrice, err = shared.ParseDecimal(row.AveragePrice); err != nil {
			return err
		}
		if p.RealizedPnL, err = shared.ParseDecimal(row.RealizedPnL); err != nil {
			return err
		}
	}
	p = p.Apply(f)
	return t.db.Exec("INSERT INTO t_trade_position_projections(c_space_id,c_account_id,c_symbol,c_quantity,c_average_price,c_realized_pnl,c_version) VALUES(?,?,?,?,?,?,?) ON CONFLICT(c_space_id,c_account_id,c_symbol) DO UPDATE SET c_quantity=excluded.c_quantity,c_average_price=excluded.c_average_price,c_realized_pnl=excluded.c_realized_pnl,c_version=excluded.c_version", space, account, symbol, p.Quantity.String(), p.AveragePrice.String(), p.RealizedPnL.String(), p.Version).Error
}
