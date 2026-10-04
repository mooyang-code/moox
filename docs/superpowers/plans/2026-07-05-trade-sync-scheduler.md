# Trade Sync Scheduler Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a scheduled trade-data synchronization pipeline so MooX periodically stores account balances, swap positions, orders, and trade fills in the local trade database.

**Architecture:** Keep the current three-layer shape: tRPC timer handler -> service coordinator -> DAO/exchange adapters. The timer only parses params and invokes a coordinator; all persistence remains behind `service.Store`, and all exchange calls continue through `exchange.ExchangeAdapter`.

**Tech Stack:** Go 1.24, tRPC-Go, `trpc.group/trpc-go/trpc-database/timer` v1.0.0, GORM, SQLite, existing Binance/OKX adapters, existing `t_accounts/t_account_balances/t_orders/t_trades/t_positions`.

---

## Scope And Decisions

This plan implements:

- Periodic balance snapshot sync for every active account in a `space_id`.
- Periodic position snapshot sync for swap/futures accounts.
- Periodic order and trade-fill history sync using persisted cursors.
- One timer service in `moox-trade` using the tRPC timer framework.
- A small sync-cursor table so history sync resumes incrementally after restart.
- Tests for cursor persistence, coordinator behavior, and timer param parsing.

This plan intentionally does not implement full funding/deposit/withdraw ledger sync in the first pass. `t_account_fund_flows` remains a separate follow-up because exchange fund-flow APIs differ more sharply and need a domain mapping decision.

## Trading Request Semantics

Scheduled synchronization is a reconciliation and compensation mechanism, not the primary trading path.

When a user trades from the MooX admin console, or when another backend service trades through the MooX API, the request must continue through the existing MooX trade orchestration layer:

1. MooX creates a local trading intent first: local order, operation audit, `client_order_id` idempotency key, and spot pre-freeze when applicable.
2. MooX calls the exchange API through the configured exchange adapter.
3. MooX immediately writes the exchange result back to local storage: `exchange_order_id`, order status, reject reason, operation status, and freeze rollback on failure.
4. The scheduled sync later reconciles exchange truth back into local snapshots and history: balances, positions, orders, and trades.

Do not change admin or backend API trading calls to "call exchange only and wait for timer sync". That would lose immediate auditability, idempotency, local failure state, and operator traceability. The timer should repair drift and fill final exchange state; it should not replace `PlaceOrderExec`, `CancelOrderExec`, `AmendOrderExec`, or the liquidation flow.

## Target Files

- Modify: `modules/trade/go.mod`
  Add `trpc.group/trpc-go/trpc-database/timer v1.0.0` if absent.

- Modify: `modules/trade/config/trpc_go.yaml`
  Add `trpc.moox.trade.sync.timer`.

- Modify: `modules/trade/config/app.yaml`
  Add `sync:` defaults for window size, page size, and enabled sections.

- Modify: `modules/trade/internal/config/app.go`
  Add `SyncConfig`, defaults, env override if needed, validation.

- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
  Import timer package and register the trade sync scheduler.

- Create: `modules/trade/internal/rpc/schedule.go`
  Timer handler: `HandleSyncSchedule(ctx context.Context, params string) error`.

- Create: `modules/trade/internal/rpc/schedule_test.go`
  Tests timer parameter parsing and service dispatch.

- Modify: `modules/trade/internal/service/store.go`
  Add cursor and account/channel enumeration methods needed by the scheduler.

- Modify: `modules/trade/internal/service/types.go`
  Add `SyncCursor`, `SyncReport`, `SyncOptions`, `SyncSection`.

- Create: `modules/trade/internal/service/sync.go`
  Coordinator for snapshots and trading history.

- Create: `modules/trade/internal/service/sync_test.go`
  Tests sync orchestration with fake store/fake adapter.

- Create: `modules/trade/internal/service/dao/sync_cursor.go`
  GORM implementation for sync cursor CRUD.

- Create: `modules/trade/internal/service/dao/sync_cursor_test.go`
  SQLite-backed DAO tests.

- Modify: `modules/trade/schema/account.sql` or create `modules/trade/schema/sync.sql`
  Add `t_trade_sync_cursors`.

- Modify: `modules/trade/schema/schema.go`
  Include new sync schema in `AllSQL()`.

- Modify: `modules/trade/schema/schema_test.go`
  Assert new table DDL is included.

- Modify: `modules/trade/DESIGN.md`
  Document scheduled sync semantics and limitations.

- Create: `examples/trade-sync/README.md`
  Operational example for timer params and manual verification queries.

---

### Task 1: Add Sync Cursor Schema

**Files:**
- Create: `modules/trade/schema/sync.sql`
- Modify: `modules/trade/schema/schema.go`
- Modify: `modules/trade/schema/schema_test.go`

- [ ] **Step 1: Write failing schema test**

Add to `modules/trade/schema/schema_test.go`:

```go
func TestAllSQLIncludesTradeSyncCursorSchema(t *testing.T) {
	sql := AllSQL()
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS t_trade_sync_cursors",
		"idx_trade_sync_cursors_unique",
		"idx_trade_sync_cursors_account",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("AllSQL() missing %q", want)
		}
	}
}
```

Run:

```bash
go test ./modules/trade/schema -run TestAllSQLIncludesTradeSyncCursorSchema -count=1
```

Expected: FAIL because `t_trade_sync_cursors` is not included.

- [ ] **Step 2: Add schema**

Create `modules/trade/schema/sync.sql`:

```sql
-- Trade scheduled sync cursors.
CREATE TABLE IF NOT EXISTS t_trade_sync_cursors (
    c_id INTEGER PRIMARY KEY AUTOINCREMENT,
    c_space_id TEXT NOT NULL DEFAULT '',
    c_account_id TEXT NOT NULL,
    c_channel_id TEXT NOT NULL DEFAULT '',
    c_exchange TEXT NOT NULL DEFAULT '',
    c_market_type TEXT NOT NULL DEFAULT '',
    c_sync_type TEXT NOT NULL,                         -- balances/positions/orders/trades
    c_symbol TEXT NOT NULL DEFAULT '',                 -- empty for account-wide cursors
    c_cursor_start_ms INTEGER NOT NULL DEFAULT 0,
    c_cursor_end_ms INTEGER NOT NULL DEFAULT 0,
    c_last_success_at DATETIME,
    c_last_error TEXT NOT NULL DEFAULT '',
    c_is_enabled INTEGER NOT NULL DEFAULT 1,
    c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_trade_sync_cursors_unique
ON t_trade_sync_cursors(c_space_id, c_account_id, c_sync_type, c_symbol);

CREATE INDEX IF NOT EXISTS idx_trade_sync_cursors_account
ON t_trade_sync_cursors(c_space_id, c_account_id, c_channel_id);

CREATE INDEX IF NOT EXISTS idx_trade_sync_cursors_type
ON t_trade_sync_cursors(c_space_id, c_sync_type, c_is_enabled);

CREATE TRIGGER IF NOT EXISTS update_trade_sync_cursors_mtime AFTER UPDATE ON t_trade_sync_cursors BEGIN
    UPDATE t_trade_sync_cursors SET c_mtime = CURRENT_TIMESTAMP WHERE rowid = NEW.rowid;
END;
```

- [ ] **Step 3: Wire schema into embedded SQL**

Update `modules/trade/schema/schema.go` so `AllSQL()` concatenates the new file after account/order schemas:

```go
//go:embed sync.sql
var syncSQL string

func AllSQL() string {
	return strings.Join([]string{
		accountSQL,
		orderSQL,
		syncSQL,
	}, "\n\n")
}
```

Use the actual existing variable names in `schema.go`; preserve the current ordering and only append `syncSQL`.

- [ ] **Step 4: Verify schema test**

Run:

```bash
go test ./modules/trade/schema -count=1
```

Expected: PASS.

---

### Task 2: Add Sync Cursor Domain Model And DAO

**Files:**
- Modify: `modules/trade/internal/service/types.go`
- Modify: `modules/trade/internal/service/store.go`
- Create: `modules/trade/internal/service/dao/sync_cursor.go`
- Create: `modules/trade/internal/service/dao/sync_cursor_test.go`

- [ ] **Step 1: Write failing DAO test**

Create `modules/trade/internal/service/dao/sync_cursor_test.go`:

```go
package dao

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
)

func TestSyncCursorUpsertAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cursor := &service.SyncCursor{
		AccountID:      "acc_1",
		ChannelID:      "ch_1",
		Exchange:       "binance",
		MarketType:     "spot",
		SyncType:       service.SyncTypeTrades,
		Symbol:         "GALAUSDT",
		CursorStartMS:  1000,
		CursorEndMS:    2000,
		LastSuccessAt:  time.Unix(2, 0),
		LastError:      "",
		IsEnabled:      true,
	}
	if err := store.UpsertSyncCursor(ctx, "crypto", cursor); err != nil {
		t.Fatalf("UpsertSyncCursor returned error: %v", err)
	}
	cursor.CursorEndMS = 3000
	if err := store.UpsertSyncCursor(ctx, "crypto", cursor); err != nil {
		t.Fatalf("second UpsertSyncCursor returned error: %v", err)
	}
	got, err := store.GetSyncCursor(ctx, "crypto", "acc_1", service.SyncTypeTrades, "GALAUSDT")
	if err != nil {
		t.Fatalf("GetSyncCursor returned error: %v", err)
	}
	if got.CursorEndMS != 3000 || got.Symbol != "GALAUSDT" || !got.IsEnabled {
		t.Fatalf("cursor = %+v, want updated cursor", got)
	}
}
```

Run:

```bash
go test ./modules/trade/internal/service/dao -run TestSyncCursorUpsertAndGet -count=1
```

Expected: FAIL because `SyncCursor` and DAO methods do not exist.

- [ ] **Step 2: Add domain types**

Add to `modules/trade/internal/service/types.go`:

```go
type SyncType string

const (
	SyncTypeBalances  SyncType = "balances"
	SyncTypePositions SyncType = "positions"
	SyncTypeOrders    SyncType = "orders"
	SyncTypeTrades    SyncType = "trades"
)

const SyncCursorTableName = "t_trade_sync_cursors"

type SyncCursor struct {
	ID            int64     `gorm:"column:c_id;primaryKey;autoIncrement" json:"id"`
	SpaceID       string    `gorm:"column:c_space_id" json:"space_id"`
	AccountID     string    `gorm:"column:c_account_id" json:"account_id"`
	ChannelID     string    `gorm:"column:c_channel_id" json:"channel_id"`
	Exchange      string    `gorm:"column:c_exchange" json:"exchange"`
	MarketType    string    `gorm:"column:c_market_type" json:"market_type"`
	SyncType      SyncType  `gorm:"column:c_sync_type" json:"sync_type"`
	Symbol        string    `gorm:"column:c_symbol" json:"symbol"`
	CursorStartMS int64     `gorm:"column:c_cursor_start_ms" json:"cursor_start_ms"`
	CursorEndMS   int64     `gorm:"column:c_cursor_end_ms" json:"cursor_end_ms"`
	LastSuccessAt time.Time `gorm:"column:c_last_success_at" json:"last_success_at"`
	LastError     string    `gorm:"column:c_last_error" json:"last_error"`
	IsEnabled     bool      `gorm:"column:c_is_enabled" json:"is_enabled"`
	CreatedAt     time.Time `gorm:"column:c_ctime" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:c_mtime" json:"updated_at"`
}

func (SyncCursor) TableName() string { return SyncCursorTableName }
```

- [ ] **Step 3: Extend Store interface**

Add to `modules/trade/internal/service/store.go`:

```go
GetSyncCursor(ctx context.Context, spaceID, accountID string, syncType SyncType, symbol string) (*SyncCursor, error)
UpsertSyncCursor(ctx context.Context, spaceID string, cursor *SyncCursor) error
ListSyncCursors(ctx context.Context, spaceID string, accountID string, syncType SyncType) ([]*SyncCursor, error)
```

- [ ] **Step 4: Implement DAO**

Create `modules/trade/internal/service/dao/sync_cursor.go`:

```go
package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (g *GormStore) GetSyncCursor(ctx context.Context, spaceID, accountID string, syncType service.SyncType, symbol string) (*service.SyncCursor, error) {
	var out service.SyncCursor
	err := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_account_id = ? AND c_sync_type = ? AND c_symbol = ?", spaceID, accountID, syncType, symbol).
		First(&out).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, service.ErrNotFound
		}
		return nil, err
	}
	return &out, nil
}

func (g *GormStore) UpsertSyncCursor(ctx context.Context, spaceID string, cursor *service.SyncCursor) error {
	cursor.SpaceID = spaceID
	if !cursor.IsEnabled {
		cursor.IsEnabled = true
	}
	return g.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_space_id"},
			{Name: "c_account_id"},
			{Name: "c_sync_type"},
			{Name: "c_symbol"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_channel_id",
			"c_exchange",
			"c_market_type",
			"c_cursor_start_ms",
			"c_cursor_end_ms",
			"c_last_success_at",
			"c_last_error",
			"c_is_enabled",
		}),
	}).Create(cursor).Error
}

func (g *GormStore) ListSyncCursors(ctx context.Context, spaceID string, accountID string, syncType service.SyncType) ([]*service.SyncCursor, error) {
	q := g.db.WithContext(ctx).Where("c_space_id = ?", spaceID)
	if accountID != "" {
		q = q.Where("c_account_id = ?", accountID)
	}
	if syncType != "" {
		q = q.Where("c_sync_type = ?", syncType)
	}
	var out []*service.SyncCursor
	if err := q.Order("c_account_id ASC, c_sync_type ASC, c_symbol ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
```

- [ ] **Step 5: Verify DAO tests**

Run:

```bash
go test ./modules/trade/internal/service/dao -run TestSyncCursorUpsertAndGet -count=1
go test ./modules/trade/internal/service/dao -count=1
```

Expected: PASS.

---

### Task 3: Add Sync Configuration

**Files:**
- Modify: `modules/trade/internal/config/app.go`
- Modify: `modules/trade/config/app.yaml`

- [ ] **Step 1: Write failing config test**

Create or extend `modules/trade/internal/config/app_test.go`:

```go
func TestDefaultConfigIncludesSyncDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Sync.Enabled {
		t.Fatalf("sync must be enabled by default")
	}
	if cfg.Sync.WindowHours != 24 {
		t.Fatalf("WindowHours=%d, want 24", cfg.Sync.WindowHours)
	}
	if cfg.Sync.PageSize != 500 {
		t.Fatalf("PageSize=%d, want 500", cfg.Sync.PageSize)
	}
	if cfg.Sync.MaxSymbolsPerRun != 10 {
		t.Fatalf("MaxSymbolsPerRun=%d, want 10", cfg.Sync.MaxSymbolsPerRun)
	}
}
```

Run:

```bash
go test ./modules/trade/internal/config -run TestDefaultConfigIncludesSyncDefaults -count=1
```

Expected: FAIL because `Sync` config does not exist.

- [ ] **Step 2: Add config structs**

Add to `modules/trade/internal/config/app.go`:

```go
type AppConfig struct {
	Database       DatabaseConfig       `yaml:"database"`
	Security       SecurityConfig       `yaml:"security"`
	ControlGateway ControlGatewayConfig `yaml:"control_gateway"`
	Sync           SyncConfig           `yaml:"sync"`
	Log            LogConfig            `yaml:"log"`
}

type SyncConfig struct {
	Enabled         bool `yaml:"enabled"`
	SyncBalances    bool `yaml:"sync_balances"`
	SyncPositions   bool `yaml:"sync_positions"`
	SyncOrders      bool `yaml:"sync_orders"`
	SyncTrades      bool `yaml:"sync_trades"`
	WindowHours     int  `yaml:"window_hours"`
	PageSize        int  `yaml:"page_size"`
	MaxSymbolsPerRun int `yaml:"max_symbols_per_run"`
}
```

Set defaults in `DefaultConfig()`:

```go
Sync: SyncConfig{
	Enabled:         true,
	SyncBalances:    true,
	SyncPositions:   true,
	SyncOrders:      true,
	SyncTrades:      true,
	WindowHours:     24,
	PageSize:        500,
	MaxSymbolsPerRun: 10,
},
```

Extend `Validate()` with:

```go
if c.Sync.WindowHours <= 0 {
	c.Sync.WindowHours = 24
}
if c.Sync.PageSize <= 0 || c.Sync.PageSize > 1000 {
	c.Sync.PageSize = 500
}
if c.Sync.MaxSymbolsPerRun <= 0 {
	c.Sync.MaxSymbolsPerRun = 10
}
```

- [ ] **Step 3: Add YAML defaults**

Add to `modules/trade/config/app.yaml`:

```yaml
sync:
  enabled: true
  sync_balances: true
  sync_positions: true
  sync_orders: true
  sync_trades: true
  window_hours: 24
  page_size: 500
  max_symbols_per_run: 10
```

- [ ] **Step 4: Verify config tests**

Run:

```bash
go test ./modules/trade/internal/config -count=1
```

Expected: PASS.

---

### Task 4: Add Sync Coordinator For Balances And Positions

**Files:**
- Modify: `modules/trade/internal/service/types.go`
- Create: `modules/trade/internal/service/sync.go`
- Create: `modules/trade/internal/service/sync_test.go`

- [ ] **Step 1: Write failing service test**

Create `modules/trade/internal/service/sync_test.go`:

```go
func TestSyncSnapshotsSyncsBalancesForAllAccountsAndPositionsForSwap(t *testing.T) {
	store := &syncCoordinatorStore{
		accounts: []*Account{
			{AccountID: "spot_1", AccountType: AccountSpot, ChannelID: "ch_spot"},
			{AccountID: "swap_1", AccountType: AccountSwap, ChannelID: "ch_swap"},
		},
		channels: map[string]*TradeChannel{
			"ch_spot": {ChannelID: "ch_spot", Exchange: "binance", MarketType: "spot", APIKeyID: "ak_spot"},
			"ch_swap": {ChannelID: "ch_swap", Exchange: "binance", MarketType: "swap", APIKeyID: "ak_swap"},
		},
		apiKeys: map[string]*APIKey{
			"ak_spot": {APIKeyID: "ak_spot", APIKey: "key", APISecret: "secret"},
			"ak_swap": {APIKeyID: "ak_swap", APIKey: "key", APISecret: "secret"},
		},
	}
	adapter := &syncCoordinatorAdapter{
		balances: []exchange.Balance{{Currency: "USDT", Available: "1", Total: "1"}},
		positions: []exchange.Position{{Symbol: "BTCUSDT", PosSide: "long", Quantity: "0.01"}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	report, err := svc.SyncAllSnapshots(context.Background(), SyncOptions{SpaceID: "crypto", PageSize: 100})
	if err != nil {
		t.Fatalf("SyncAllSnapshots returned error: %v", err)
	}
	if report.BalancesSynced != 2 {
		t.Fatalf("BalancesSynced=%d, want 2", report.BalancesSynced)
	}
	if report.PositionsSynced != 1 {
		t.Fatalf("PositionsSynced=%d, want 1", report.PositionsSynced)
	}
}
```

The fake store can embed `Store` and implement only methods called by `SyncAllSnapshots`: `ListAccounts`, `GetAccount`, `GetChannel`, `GetAPIKey`, `UpsertBalances`, `ReplacePositions`, `ListPositions`, `GetBalances`.

Run:

```bash
go test ./modules/trade/internal/service -run TestSyncSnapshotsSyncsBalancesForAllAccountsAndPositionsForSwap -count=1
```

Expected: FAIL because `SyncAllSnapshots` does not exist.

- [ ] **Step 2: Add report and options types**

Add to `modules/trade/internal/service/types.go`:

```go
type SyncOptions struct {
	SpaceID          string
	AccountID        string
	Sections         map[SyncType]bool
	WindowHours      int
	PageSize         int
	MaxSymbolsPerRun int
	Now              time.Time
}

type SyncReport struct {
	SpaceID          string
	AccountsScanned  int
	BalancesSynced   int
	PositionsSynced  int
	OrdersSynced     int
	TradesSynced     int
	SkippedAccounts  int
	Errors           []string
}
```

- [ ] **Step 3: Implement account enumeration helper**

In `modules/trade/internal/service/sync.go`:

```go
func (s *Service) syncAccounts(ctx context.Context, opts SyncOptions) ([]*Account, error) {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	if opts.AccountID != "" {
		a, err := s.Account.store.GetAccount(ctx, opts.SpaceID, opts.AccountID)
		if err != nil {
			return nil, err
		}
		return []*Account{a}, nil
	}
	var all []*Account
	for pageNo := 1; ; pageNo++ {
		items, total, err := s.Account.store.ListAccounts(ctx, opts.SpaceID, AccountFilter{}, Page{PageNo: pageNo, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(all) >= total || len(items) == 0 {
			return all, nil
		}
	}
}
```

- [ ] **Step 4: Implement snapshot sync coordinator**

In `modules/trade/internal/service/sync.go`:

```go
func (s *Service) SyncAllSnapshots(ctx context.Context, opts SyncOptions) (*SyncReport, error) {
	report := &SyncReport{SpaceID: opts.SpaceID}
	accounts, err := s.syncAccounts(ctx, opts)
	if err != nil {
		return report, err
	}
	for _, account := range accounts {
		report.AccountsScanned++
		if account.ChannelID == "" {
			report.SkippedAccounts++
			continue
		}
		if _, err := s.Account.SyncBalances(ctx, opts.SpaceID, account.AccountID); err != nil {
			report.Errors = append(report.Errors, account.AccountID+": balances: "+err.Error())
		} else {
			report.BalancesSynced++
		}
		if account.AccountType == AccountSwap {
			positions, err := s.Order.SyncPositions(ctx, opts.SpaceID, account.AccountID, "")
			if err != nil {
				report.Errors = append(report.Errors, account.AccountID+": positions: "+err.Error())
			} else {
				report.PositionsSynced += len(positions)
			}
		}
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("trade snapshot sync finished with %d errors", len(report.Errors))
	}
	return report, nil
}
```

- [ ] **Step 5: Verify service test**

Run:

```bash
go test ./modules/trade/internal/service -run TestSyncSnapshotsSyncsBalancesForAllAccountsAndPositionsForSwap -count=1
go test ./modules/trade/internal/service -count=1
```

Expected: PASS.

---

### Task 5: Add Symbol Selection For History Sync

**Files:**
- Create or extend: `modules/trade/internal/service/sync.go`
- Extend: `modules/trade/internal/service/sync_test.go`

- [ ] **Step 1: Write failing symbol selection test**

Add to `modules/trade/internal/service/sync_test.go`:

```go
func TestHistorySymbolsPreferExistingLocalAndNonZeroBalanceSymbols(t *testing.T) {
	store := &syncCoordinatorStore{
		balancesByAccount: map[string][]*Balance{
			"acc_1": {
				{Currency: "GALA", Total: "1225.773"},
				{Currency: "USDT", Total: "10"},
				{Currency: "SHIB", Total: "0.0001"},
			},
		},
		ordersByAccount: map[string][]*Order{
			"acc_1": {{Symbol: "SOLUSDT"}},
		},
		tradesByAccount: map[string][]*Trade{
			"acc_1": {{Symbol: "BTCUSDT"}},
		},
	}
	svc := New("trade", WithStore(store))
	got := svc.historySymbols(context.Background(), "crypto", &Account{AccountID: "acc_1", BaseCurrency: "USDT"}, 10)
	want := []string{"BTCUSDT", "GALAUSDT", "SHIBUSDT", "SOLUSDT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols=%v, want %v", got, want)
	}
}
```

Run:

```bash
go test ./modules/trade/internal/service -run TestHistorySymbolsPreferExistingLocalAndNonZeroBalanceSymbols -count=1
```

Expected: FAIL because `historySymbols` does not exist.

- [ ] **Step 2: Add symbol helper**

Implement in `modules/trade/internal/service/sync.go`:

```go
func (s *Service) historySymbols(ctx context.Context, spaceID string, account *Account, maxSymbols int) []string {
	base := strings.ToUpper(strings.TrimSpace(account.BaseCurrency))
	if base == "" {
		base = "USDT"
	}
	seen := map[string]bool{}
	add := func(symbol string) {
		symbol = strings.ToUpper(strings.TrimSpace(symbol))
		if symbol != "" {
			seen[symbol] = true
		}
	}
	balances, _ := s.Account.store.GetBalances(ctx, spaceID, account.AccountID, nil)
	for _, balance := range balances {
		ccy := strings.ToUpper(strings.TrimSpace(balance.Currency))
		if ccy == "" || ccy == base || !isPositiveDecimal(balance.Total) {
			continue
		}
		add(ccy + base)
	}
	orders, _, _ := s.Order.store.ListOrders(ctx, spaceID, OrderFilter{AccountID: account.AccountID}, Page{PageNo: 1, PageSize: maxSymbols})
	for _, order := range orders {
		add(order.Symbol)
	}
	trades, _, _ := s.Order.store.ListTrades(ctx, spaceID, TradeFilter{AccountID: account.AccountID}, Page{PageNo: 1, PageSize: maxSymbols})
	for _, trade := range trades {
		add(trade.Symbol)
	}
	out := make([]string, 0, len(seen))
	for symbol := range seen {
		out = append(out, symbol)
	}
	sort.Strings(out)
	if maxSymbols > 0 && len(out) > maxSymbols {
		out = out[:maxSymbols]
	}
	return out
}
```

Use existing decimal helpers if present; otherwise add a small private helper:

```go
func isPositiveDecimal(value string) bool {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && f > 0
}
```

- [ ] **Step 3: Verify symbol test**

Run:

```bash
go test ./modules/trade/internal/service -run TestHistorySymbolsPreferExistingLocalAndNonZeroBalanceSymbols -count=1
```

Expected: PASS.

---

### Task 6: Add Incremental Orders And Trades Sync

**Files:**
- Extend: `modules/trade/internal/service/sync.go`
- Extend: `modules/trade/internal/service/sync_test.go`

- [ ] **Step 1: Write failing cursor advancement test**

Add to `modules/trade/internal/service/sync_test.go`:

```go
func TestSyncTradingHistoryAdvancesOrderAndTradeCursors(t *testing.T) {
	now := time.UnixMilli(1_800_000)
	store := &syncCoordinatorStore{
		accounts: []*Account{{AccountID: "acc_1", AccountType: AccountSpot, ChannelID: "ch_1", BaseCurrency: "USDT"}},
		channels: map[string]*TradeChannel{"ch_1": {ChannelID: "ch_1", Exchange: "binance", MarketType: "spot", APIKeyID: "ak_1"}},
		apiKeys: map[string]*APIKey{"ak_1": {APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"}},
		balancesByAccount: map[string][]*Balance{"acc_1": {{Currency: "GALA", Total: "1"}}},
	}
	adapter := &syncCoordinatorAdapter{
		orders: []exchange.Order{{ExchangeOrderID: "1001", Symbol: "GALAUSDT", Market: exchange.MarketSpot}},
		trades: []exchange.Trade{{TradeID: "2001", Symbol: "GALAUSDT", TradedAt: now.UnixMilli()}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	report, err := svc.SyncTradingHistory(context.Background(), SyncOptions{
		SpaceID: "crypto", WindowHours: 24, PageSize: 500, MaxSymbolsPerRun: 10, Now: now,
	})
	if err != nil {
		t.Fatalf("SyncTradingHistory returned error: %v", err)
	}
	if report.OrdersSynced != 1 || report.TradesSynced != 1 {
		t.Fatalf("report=%+v, want one order and one trade", report)
	}
	orderCursor, _ := store.GetSyncCursor(context.Background(), "crypto", "acc_1", SyncTypeOrders, "GALAUSDT")
	tradeCursor, _ := store.GetSyncCursor(context.Background(), "crypto", "acc_1", SyncTypeTrades, "GALAUSDT")
	if orderCursor.CursorEndMS != now.UnixMilli() || tradeCursor.CursorEndMS != now.UnixMilli() {
		t.Fatalf("cursors order=%+v trade=%+v, want end=%d", orderCursor, tradeCursor, now.UnixMilli())
	}
}
```

Run:

```bash
go test ./modules/trade/internal/service -run TestSyncTradingHistoryAdvancesOrderAndTradeCursors -count=1
```

Expected: FAIL because `SyncTradingHistory` does not exist.

- [ ] **Step 2: Implement cursor window helper**

Add in `modules/trade/internal/service/sync.go`:

```go
func syncWindow(cursor *SyncCursor, opts SyncOptions) (startMS int64, endMS int64) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	windowHours := opts.WindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	endMS = now.UnixMilli()
	if cursor != nil && cursor.CursorEndMS > 0 {
		startMS = cursor.CursorEndMS + 1
	} else {
		startMS = now.Add(-time.Duration(windowHours) * time.Hour).UnixMilli()
	}
	if startMS > endMS {
		startMS = endMS
	}
	return startMS, endMS
}
```

- [ ] **Step 3: Implement `SyncTradingHistory`**

Add in `modules/trade/internal/service/sync.go`:

```go
func (s *Service) SyncTradingHistory(ctx context.Context, opts SyncOptions) (*SyncReport, error) {
	report := &SyncReport{SpaceID: opts.SpaceID}
	accounts, err := s.syncAccounts(ctx, opts)
	if err != nil {
		return report, err
	}
	for _, account := range accounts {
		report.AccountsScanned++
		if account.ChannelID == "" {
			report.SkippedAccounts++
			continue
		}
		symbols := s.historySymbols(ctx, opts.SpaceID, account, opts.MaxSymbolsPerRun)
		for _, symbol := range symbols {
			orderCount, err := s.syncOrdersForSymbol(ctx, opts, account, symbol)
			if err != nil {
				report.Errors = append(report.Errors, account.AccountID+": orders "+symbol+": "+err.Error())
			} else {
				report.OrdersSynced += orderCount
			}
			tradeCount, err := s.syncTradesForSymbol(ctx, opts, account, symbol)
			if err != nil {
				report.Errors = append(report.Errors, account.AccountID+": trades "+symbol+": "+err.Error())
			} else {
				report.TradesSynced += tradeCount
			}
		}
	}
	if len(report.Errors) > 0 {
		return report, fmt.Errorf("trade history sync finished with %d errors", len(report.Errors))
	}
	return report, nil
}
```

- [ ] **Step 4: Implement per-symbol helpers**

Add helper methods:

```go
func (s *Service) syncOrdersForSymbol(ctx context.Context, opts SyncOptions, account *Account, symbol string) (int, error) {
	cursor, _ := s.Order.store.GetSyncCursor(ctx, opts.SpaceID, account.AccountID, SyncTypeOrders, symbol)
	startMS, endMS := syncWindow(cursor, opts)
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	orders, _, err := s.Order.SyncOrders(ctx, opts.SpaceID, account.AccountID, symbol, false, startMS, endMS, Page{PageNo: 1, PageSize: pageSize})
	if err != nil {
		_ = s.Order.store.UpsertSyncCursor(ctx, opts.SpaceID, failedCursor(account, SyncTypeOrders, symbol, startMS, cursorEnd(cursor), err))
		return 0, err
	}
	_ = s.Order.store.UpsertSyncCursor(ctx, opts.SpaceID, successCursor(account, SyncTypeOrders, symbol, startMS, endMS))
	return len(orders), nil
}

func (s *Service) syncTradesForSymbol(ctx context.Context, opts SyncOptions, account *Account, symbol string) (int, error) {
	cursor, _ := s.Order.store.GetSyncCursor(ctx, opts.SpaceID, account.AccountID, SyncTypeTrades, symbol)
	startMS, endMS := syncWindow(cursor, opts)
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	trades, _, err := s.Order.SyncTrades(ctx, opts.SpaceID, account.AccountID, symbol, "", startMS, endMS, Page{PageNo: 1, PageSize: pageSize})
	if err != nil {
		_ = s.Order.store.UpsertSyncCursor(ctx, opts.SpaceID, failedCursor(account, SyncTypeTrades, symbol, startMS, cursorEnd(cursor), err))
		return 0, err
	}
	_ = s.Order.store.UpsertSyncCursor(ctx, opts.SpaceID, successCursor(account, SyncTypeTrades, symbol, startMS, endMS))
	return len(trades), nil
}
```

Define cursor constructors:

```go
func successCursor(account *Account, syncType SyncType, symbol string, startMS, endMS int64) *SyncCursor {
	return &SyncCursor{
		AccountID: account.AccountID, ChannelID: account.ChannelID,
		SyncType: syncType, Symbol: symbol,
		MarketType: string(account.AccountType),
		CursorStartMS: startMS, CursorEndMS: endMS,
		LastSuccessAt: time.Now(), LastError: "", IsEnabled: true,
	}
}

func failedCursor(account *Account, syncType SyncType, symbol string, startMS, endMS int64, err error) *SyncCursor {
	return &SyncCursor{
		AccountID: account.AccountID, ChannelID: account.ChannelID,
		SyncType: syncType, Symbol: symbol,
		MarketType: string(account.AccountType),
		CursorStartMS: startMS, CursorEndMS: endMS,
		LastError: err.Error(), IsEnabled: true,
	}
}

func cursorEnd(cursor *SyncCursor) int64 {
	if cursor == nil {
		return 0
	}
	return cursor.CursorEndMS
}
```

- [ ] **Step 5: Verify history tests**

Run:

```bash
go test ./modules/trade/internal/service -run 'TestSyncTradingHistory|TestHistorySymbols' -count=1
go test ./modules/trade/internal/service -count=1
```

Expected: PASS.

---

### Task 7: Add tRPC Timer Handler

**Files:**
- Create: `modules/trade/internal/rpc/schedule.go`
- Create: `modules/trade/internal/rpc/schedule_test.go`
- Modify: `modules/trade/internal/bootstrap/bootstrap.go`
- Modify: `modules/trade/config/trpc_go.yaml`
- Modify: `modules/trade/go.mod`

- [ ] **Step 1: Write failing param parsing test**

Create `modules/trade/internal/rpc/schedule_test.go`:

```go
func TestParseSyncScheduleParams(t *testing.T) {
	got := parseSyncScheduleParams("space_id=crypto;account_id=acc_1;sections=balances,positions;window_hours=12;max_symbols=20")
	if got.SpaceID != "crypto" || got.AccountID != "acc_1" {
		t.Fatalf("ids = %#v", got)
	}
	if got.WindowHours != 12 || got.MaxSymbolsPerRun != 20 {
		t.Fatalf("window/max = %#v", got)
	}
	if !got.Sections[service.SyncTypeBalances] || !got.Sections[service.SyncTypePositions] {
		t.Fatalf("sections = %#v", got.Sections)
	}
	if got.Sections[service.SyncTypeTrades] {
		t.Fatalf("trades should not be selected: %#v", got.Sections)
	}
}
```

Run:

```bash
go test ./modules/trade/internal/rpc -run TestParseSyncScheduleParams -count=1
```

Expected: FAIL because parser does not exist.

- [ ] **Step 2: Implement timer handler**

Create `modules/trade/internal/rpc/schedule.go`:

```go
package rpc

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	defaultSyncServiceMu sync.RWMutex
	defaultSyncService   *service.Service
	syncRunMu            sync.Mutex
)

func SetDefaultSyncService(svc *service.Service) {
	defaultSyncServiceMu.Lock()
	defer defaultSyncServiceMu.Unlock()
	defaultSyncService = svc
}

func HandleSyncSchedule(ctx context.Context, params string) error {
	defaultSyncServiceMu.RLock()
	svc := defaultSyncService
	defaultSyncServiceMu.RUnlock()
	if svc == nil {
		log.WarnContext(ctx, "[TradeSync] default sync service is nil, skip")
		return nil
	}
	if !syncRunMu.TryLock() {
		log.WarnContext(ctx, "[TradeSync] previous sync still running, skip")
		return nil
	}
	defer syncRunMu.Unlock()
	opts := parseSyncScheduleParams(params)
	if opts.SpaceID == "" {
		opts.SpaceID = "crypto"
	}
	if opts.PageSize == 0 {
		opts.PageSize = 500
	}
	if _, err := svc.SyncAllSnapshots(ctx, opts); err != nil {
		log.ErrorContextf(ctx, "[TradeSync] snapshot sync failed: %v", err)
	}
	if _, err := svc.SyncTradingHistory(ctx, opts); err != nil {
		log.ErrorContextf(ctx, "[TradeSync] history sync failed: %v", err)
	}
	return nil
}

func parseSyncScheduleParams(params string) service.SyncOptions {
	opts := service.SyncOptions{Sections: map[service.SyncType]bool{}}
	for _, part := range strings.Split(params, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			if opts.SpaceID == "" {
				opts.SpaceID = strings.TrimSpace(kv[0])
			}
			continue
		}
		key, value := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "space_id":
			opts.SpaceID = value
		case "account_id":
			opts.AccountID = value
		case "window_hours":
			opts.WindowHours, _ = strconv.Atoi(value)
		case "page_size":
			opts.PageSize, _ = strconv.Atoi(value)
		case "max_symbols":
			opts.MaxSymbolsPerRun, _ = strconv.Atoi(value)
		case "sections":
			for _, section := range strings.Split(value, ",") {
				switch strings.TrimSpace(section) {
				case "balances":
					opts.Sections[service.SyncTypeBalances] = true
				case "positions":
					opts.Sections[service.SyncTypePositions] = true
				case "orders":
					opts.Sections[service.SyncTypeOrders] = true
				case "trades":
					opts.Sections[service.SyncTypeTrades] = true
				}
			}
		}
	}
	return opts
}
```

If `sync.Mutex.TryLock` is not available in the project Go version, replace it with a package-level `atomic.Bool` guard.

- [ ] **Step 3: Wire default service and timer registration**

Modify `modules/trade/internal/bootstrap/bootstrap.go`:

```go
import "trpc.group/trpc-go/trpc-database/timer"
```

After `rpc.RegisterAll(s, svc)`:

```go
rpc.SetDefaultSyncService(svc)
registerTradeSyncSchedule(s)
```

Add:

```go
func registerTradeSyncSchedule(s *server.Server) {
	timer.RegisterScheduler("tradeSyncSchedule", &timer.DefaultScheduler{})
	service := s.Service("trpc.moox.trade.sync.timer")
	if service == nil {
		log.Warn("trade sync timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, rpc.HandleSyncSchedule)
}
```

- [ ] **Step 4: Add timer service config**

Modify `modules/trade/config/trpc_go.yaml` under `server.service`:

```yaml
      ######## 定时同步服务：余额/持仓/订单/成交增量同步 ########
    - name: trpc.moox.trade.sync.timer
      port: 11209
      network: "0 */5 * * * *?scheduler=tradeSyncSchedule&startAtOnce=1&params=space_id=crypto;sections=balances,positions,orders,trades;window_hours=24;page_size=500;max_symbols=10"
      protocol: timer
      timeout: 60000
```

- [ ] **Step 5: Add module dependency**

Run:

```bash
cd modules/trade
go get trpc.group/trpc-go/trpc-database/timer@v1.0.0
go mod tidy
```

- [ ] **Step 6: Verify timer tests**

Run:

```bash
go test ./modules/trade/internal/rpc -run TestParseSyncScheduleParams -count=1
go test ./modules/trade/internal/rpc -count=1
```

Expected: PASS.

---

### Task 8: Respect Section Selection And App Config

**Files:**
- Modify: `modules/trade/internal/rpc/schedule.go`
- Modify: `modules/trade/internal/service/sync.go`
- Extend: `modules/trade/internal/service/sync_test.go`

- [ ] **Step 1: Write failing section test**

Add:

```go
func TestSyncAllSnapshotsSkipsPositionsWhenSectionDisabled(t *testing.T) {
	store := &syncCoordinatorStore{
		accounts: []*Account{{AccountID: "swap_1", AccountType: AccountSwap, ChannelID: "ch_1"}},
		channels: map[string]*TradeChannel{"ch_1": {ChannelID: "ch_1", Exchange: "binance", MarketType: "swap", APIKeyID: "ak_1"}},
		apiKeys: map[string]*APIKey{"ak_1": {APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"}},
	}
	adapter := &syncCoordinatorAdapter{balances: []exchange.Balance{{Currency: "USDT", Total: "1"}}}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) { return adapter, nil }))
	report, err := svc.SyncAllSnapshots(context.Background(), SyncOptions{
		SpaceID: "crypto",
		Sections: map[SyncType]bool{SyncTypeBalances: true},
	})
	if err != nil {
		t.Fatalf("SyncAllSnapshots returned error: %v", err)
	}
	if report.BalancesSynced != 1 || report.PositionsSynced != 0 {
		t.Fatalf("report=%+v, want balances only", report)
	}
}
```

Run:

```bash
go test ./modules/trade/internal/service -run TestSyncAllSnapshotsSkipsPositionsWhenSectionDisabled -count=1
```

Expected: FAIL until section selection is respected.

- [ ] **Step 2: Add section helper**

In `modules/trade/internal/service/sync.go`:

```go
func syncSectionEnabled(opts SyncOptions, section SyncType) bool {
	if len(opts.Sections) == 0 {
		return true
	}
	return opts.Sections[section]
}
```

Use it in `SyncAllSnapshots`:

```go
if syncSectionEnabled(opts, SyncTypeBalances) {
	// SyncBalances
}
if account.AccountType == AccountSwap && syncSectionEnabled(opts, SyncTypePositions) {
	// SyncPositions
}
```

Use it in `SyncTradingHistory`:

```go
if syncSectionEnabled(opts, SyncTypeOrders) {
	// sync orders
}
if syncSectionEnabled(opts, SyncTypeTrades) {
	// sync trades
}
```

- [ ] **Step 3: Apply app config defaults in timer handler**

In `modules/trade/internal/rpc/schedule.go`, read global config:

```go
cfg := config.GetGlobalConfig()
if cfg != nil {
	if opts.WindowHours == 0 {
		opts.WindowHours = cfg.Sync.WindowHours
	}
	if opts.PageSize == 0 {
		opts.PageSize = cfg.Sync.PageSize
	}
	if opts.MaxSymbolsPerRun == 0 {
		opts.MaxSymbolsPerRun = cfg.Sync.MaxSymbolsPerRun
	}
}
```

If params do not set `sections`, populate sections from config booleans:

```go
if len(opts.Sections) == 0 && cfg != nil {
	opts.Sections = map[service.SyncType]bool{
		service.SyncTypeBalances:  cfg.Sync.SyncBalances,
		service.SyncTypePositions: cfg.Sync.SyncPositions,
		service.SyncTypeOrders:    cfg.Sync.SyncOrders,
		service.SyncTypeTrades:    cfg.Sync.SyncTrades,
	}
}
```

- [ ] **Step 4: Verify section behavior**

Run:

```bash
go test ./modules/trade/internal/service -run 'TestSyncAllSnapshotsSkipsPositionsWhenSectionDisabled|TestSyncTradingHistory' -count=1
```

Expected: PASS.

---

### Task 9: Add Docs And Examples

**Files:**
- Modify: `modules/trade/DESIGN.md`
- Create: `examples/trade-sync/README.md`

- [ ] **Step 1: Update design document**

Add a section to `modules/trade/DESIGN.md`:

```markdown
## Trading Write Path

MooX trading APIs are the authoritative write path for user-initiated and backend-initiated trading.
The admin console and backend services call MooX APIs, not exchange APIs directly.

The execution sequence is:

1. create a local order intent and operation audit record;
2. preserve `client_order_id` as the business idempotency key;
3. pre-freeze spot balances when required;
4. call the exchange through the exchange adapter;
5. immediately update local order status, exchange order id, rejection reason, and operation result;
6. let scheduled synchronization reconcile final exchange truth afterward.

Scheduled synchronization must not replace the primary trading write path. It only repairs drift and fills
final state that may arrive later, especially fills, final order status, balances, and positions.

## Scheduled Synchronization

`moox-trade` registers `trpc.moox.trade.sync.timer` through the tRPC timer plugin.
The timer periodically synchronizes:

1. account balance snapshots into `t_account_balances`;
2. swap/futures position snapshots into `t_positions`;
3. order snapshots into `t_orders`;
4. trade fills into `t_trades`;
5. per-account/per-symbol cursors into `t_trade_sync_cursors`.

The first implementation does not treat `t_account_fund_flows` as a complete exchange ledger.
Fund-flow synchronization is a separate mapping task because each exchange exposes different
funding/deposit/withdraw/transfer semantics.
```

- [ ] **Step 2: Add operational example**

Create `examples/trade-sync/README.md`:

```markdown
# Trade Sync Examples

## Timer Service

`modules/trade/config/trpc_go.yaml` registers:

```yaml
- name: trpc.moox.trade.sync.timer
  port: 11209
  network: "0 */5 * * * *?scheduler=tradeSyncSchedule&startAtOnce=1&params=space_id=crypto;sections=balances,positions,orders,trades;window_hours=24;page_size=500;max_symbols=10"
  protocol: timer
  timeout: 60000
```

## Verify Local SQLite

```bash
python3 - <<'PY'
import sqlite3
conn = sqlite3.connect("/home/ubuntu/moox-cloud/trade/data/moox_trade.db")
cur = conn.cursor()
for table in ["t_accounts", "t_account_balances", "t_positions", "t_orders", "t_trades", "t_trade_sync_cursors"]:
    cur.execute(f"select count(*) from {table}")
    print(table, cur.fetchone()[0])
PY
```

## Expected Semantics

- admin-console and backend API trading requests use MooX trade APIs as the primary write path;
- the timer is reconciliation/compensation, not "exchange-only now, local write later";
- balances and positions are snapshots;
- orders are upserted by deterministic MooX order id;
- trades are append-only and deduplicated by `c_trade_id`;
- cursors let the next timer run continue from the previous successful end time.
```

- [ ] **Step 3: Verify docs are present**

Run:

```bash
test -f examples/trade-sync/README.md
rg -n "Scheduled Synchronization|t_trade_sync_cursors|tradeSyncSchedule" modules/trade/DESIGN.md examples/trade-sync/README.md
```

Expected: both files mention scheduled sync and cursor table.

---

### Task 10: End-To-End Verification And Release

**Files:**
- No source changes expected.

- [ ] **Step 1: Run focused tests**

Run:

```bash
go test ./modules/trade/schema -count=1
go test ./modules/trade/internal/config -count=1
go test ./modules/trade/internal/service/dao -count=1
go test ./modules/trade/internal/service -count=1
go test ./modules/trade/internal/rpc -count=1
```

Expected: PASS.

- [ ] **Step 2: Run module tests**

Run:

```bash
cd modules/trade
go test ./internal/...
```

Expected: PASS.

- [ ] **Step 3: Build Linux binary**

Run:

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 scripts/build/build.sh trade
```

Expected:

```text
==> build moox-trade (linux/amd64)
==> binaries written to .../moox/bin
```

- [ ] **Step 4: Deploy trade service**

Use the existing deploy pattern:

```bash
export SSHPASS='<remote-ssh-password>'
sshpass -e scp -o StrictHostKeyChecking=no bin/moox-trade ubuntu@43.132.204.177:/tmp/moox-trade.new
sshpass -e ssh -o StrictHostKeyChecking=no ubuntu@43.132.204.177 '
set -e
cd /home/ubuntu/moox-cloud/trade
ts=$(date +%Y%m%d_%H%M%S)
cp -f bin/moox-trade bin/moox-trade.bak.$ts 2>/dev/null || true
if [ -f run.pid ] && kill -0 $(cat run.pid) 2>/dev/null; then kill $(cat run.pid); sleep 1; fi
mv /tmp/moox-trade.new bin/moox-trade
chmod +x bin/moox-trade
nohup ./bin/moox-trade -conf=config/trpc_go.yaml >> log/stdout.log 2>&1 &
echo $! > run.pid
sleep 1
ps -fp $(cat run.pid)
tail -n 30 log/stdout.log
'
```

- [ ] **Step 5: Verify table creation remotely**

Run:

```bash
sshpass -e ssh -o StrictHostKeyChecking=no ubuntu@43.132.204.177 '
cd /home/ubuntu/moox-cloud/trade
python3 - << "PY"
import sqlite3
conn=sqlite3.connect("data/moox_trade.db")
cur=conn.cursor()
cur.execute("select name from sqlite_master where type=\"table\" and name=\"t_trade_sync_cursors\"")
print(cur.fetchone())
PY
'
```

Expected:

```text
('t_trade_sync_cursors',)
```

- [ ] **Step 6: Verify timer activity**

Run after one timer interval:

```bash
sshpass -e ssh -o StrictHostKeyChecking=no ubuntu@43.132.204.177 '
cd /home/ubuntu/moox-cloud/trade
tail -n 100 log/stdout.log | grep -E "TradeSync|trade sync" || true
python3 - << "PY"
import sqlite3
conn=sqlite3.connect("data/moox_trade.db")
cur=conn.cursor()
for table in ["t_account_balances", "t_positions", "t_orders", "t_trades", "t_trade_sync_cursors"]:
    cur.execute(f"select count(*) from {table}")
    print(table, cur.fetchone()[0])
PY
'
```

Expected:

- service is running;
- `t_trade_sync_cursors` has rows after history sync sees at least one symbol;
- `t_account_balances` and `t_positions` update timestamps move forward.

---

## Self-Review

### Spec Coverage

- Periodic sync uses tRPC framework timer: Task 7.
- Account data remains in `t_accounts` and balance snapshots in `t_account_balances`: Tasks 4 and 10.
- Position data is periodically synchronized: Tasks 4, 7, and 8.
- Order/trade history is stored locally with incremental cursors: Tasks 2, 5, and 6.
- User/admin/backend API trading remains on the existing MooX execution path; scheduled sync only reconciles and compensates: Trading Request Semantics and Task 9.
- Remote verification and examples are included: Tasks 9 and 10.

### Known Limitations

- Binance `ListOrders` and `ListTrades` require `symbol`; this plan syncs discovered symbols rather than every listed exchange symbol. This is intentional for a personal project to avoid huge API scans.
- `t_account_fund_flows` remains out of first pass. A later plan should add exchange-specific fund-flow mapping and cursor types.
- Timer overlap is guarded in-process. If moox-trade is horizontally scaled later, this needs a DB lease.
