package store

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/position"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

var ErrConflict = errors.New("trade: store conflict")

type Store struct{ db *gorm.DB }
type Tx struct{ db *gorm.DB }
type OutboxRecord struct {
	ID               int64
	MessageID, Topic string
	Payload          []byte
	Attempts         int
}

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
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error { return fn(&Tx{db: db}) })
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
	SpaceID, OrderID, ClientOrderID, Symbol, Side, Quantity, Price, FilledQuantity, State, ExchangeOrderID string
	Version                                                                                                uint64
}

func (*OrderRecord) TableName() string { return "t_trade_order_aggregates" }
func (t *Tx) CreateOrder(v *OrderRecord) error {
	r := map[string]any{"c_space_id": v.SpaceID, "c_order_id": v.OrderID, "c_client_order_id": v.ClientOrderID, "c_symbol": v.Symbol, "c_side": v.Side, "c_quantity": v.Quantity, "c_price": v.Price, "c_filled_quantity": v.FilledQuantity, "c_state": v.State, "c_exchange_order_id": v.ExchangeOrderID, "c_version": v.Version}
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
func (t *Tx) GetOrder(ctx context.Context, space, id string) (OrderRecord, error) {
	return getOrder(t.db.WithContext(ctx), "c_order_id", space, id)
}
func getOrder(db *gorm.DB, column, space, id string) (OrderRecord, error) {
	var r struct {
		SpaceID         string `gorm:"column:c_space_id"`
		OrderID         string `gorm:"column:c_order_id"`
		ClientOrderID   string `gorm:"column:c_client_order_id"`
		Symbol          string `gorm:"column:c_symbol"`
		Side            string `gorm:"column:c_side"`
		Quantity        string `gorm:"column:c_quantity"`
		Price           string `gorm:"column:c_price"`
		FilledQuantity  string `gorm:"column:c_filled_quantity"`
		State           string `gorm:"column:c_state"`
		ExchangeOrderID string `gorm:"column:c_exchange_order_id"`
		Version         uint64 `gorm:"column:c_version"`
	}
	e := db.Table("t_trade_order_aggregates").Where("c_space_id=? AND "+column+"=?", space, id).Take(&r).Error
	return OrderRecord{SpaceID: r.SpaceID, OrderID: r.OrderID, ClientOrderID: r.ClientOrderID, Symbol: r.Symbol, Side: r.Side, Quantity: r.Quantity, Price: r.Price, FilledQuantity: r.FilledQuantity, State: r.State, ExchangeOrderID: r.ExchangeOrderID, Version: r.Version}, e
}
func (t *Tx) UpdateOrder(v OrderRecord, expected uint64) error {
	res := t.db.Table("t_trade_order_aggregates").Where("c_space_id=? AND c_order_id=? AND c_version=?", v.SpaceID, v.OrderID, expected).Updates(map[string]any{"c_filled_quantity": v.FilledQuantity, "c_state": v.State, "c_exchange_order_id": v.ExchangeOrderID, "c_version": v.Version, "c_mtime": time.Now().UTC()})
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
func (t *Tx) AddOutbox(id, topic string, payload []byte) error {
	return t.db.Exec("INSERT INTO t_trade_outbox(c_message_id,c_topic,c_payload) VALUES(?,?,?)", id, topic, payload).Error
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
	}
	token := fmt.Sprintf("%d-%d", time.Now().UnixNano(), claimSequence.Add(1))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if e := tx.Exec("UPDATE t_trade_outbox SET c_lease_until=?,c_attempts=c_attempts+1,c_claim_token=? WHERE c_id IN (SELECT c_id FROM t_trade_outbox WHERE c_status='PENDING' AND (c_lease_until IS NULL OR c_lease_until<?) ORDER BY c_id LIMIT ?)", now.Add(lease), token, now, limit).Error; e != nil {
			return e
		}
		return tx.Raw("SELECT c_id,c_message_id,c_topic,c_payload,c_attempts FROM t_trade_outbox WHERE c_claim_token=? ORDER BY c_id", token).Scan(&rows).Error
	})
	out := make([]OutboxRecord, len(rows))
	for i, r := range rows {
		out[i] = OutboxRecord{ID: r.ID, MessageID: r.MessageID, Topic: r.Topic, Payload: r.Payload, Attempts: r.Attempts + 1}
	}
	return out, err
}
func (s *Store) MarkOutboxPublished(ctx context.Context, id int64) error {
	return s.db.WithContext(ctx).Exec("UPDATE t_trade_outbox SET c_status='PUBLISHED',c_published_at=?,c_lease_until=NULL,c_claim_token='' WHERE c_id=?", time.Now().UTC(), id).Error
}
func (s *Store) ReleaseOutbox(ctx context.Context, id int64, msg string) error {
	return s.db.WithContext(ctx).Exec("UPDATE t_trade_outbox SET c_lease_until=NULL,c_claim_token='',c_last_error=? WHERE c_id=?", msg, id).Error
}
func (t *Tx) InsertFill(space, fillID, exchangeID, orderID, qty, price, fee, feeAsset string) (bool, error) {
	res := t.db.Exec("INSERT OR IGNORE INTO t_trade_fill_events(c_space_id,c_fill_id,c_exchange_trade_id,c_order_id,c_quantity,c_price,c_fee,c_fee_asset) VALUES(?,?,?,?,?,?,?,?)", space, fillID, exchangeID, orderID, qty, price, fee, feeAsset)
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
