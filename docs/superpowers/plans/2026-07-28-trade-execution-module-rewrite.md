# Generic Trade Execution Module Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current mixed Trade implementation with one small execution kernel that supports Binance and OKX SPOT/SWAP MARKET and LIMIT orders, Exchange-authoritative account synchronization, and continuously converging Strategy targets.

**Architecture:** Keep one Go process, one SQLite handle, one Exchange vocabulary, one `OrderSpec`, and one `ExchangeAdapter` interface. Strategy publishes final base-asset target quantities; Trade serializes execution by Exchange account and symbol, persists Order and Fill facts, and synchronizes Account, Position, OpenOrder, and RecentFill state before allowing submission.

**Tech Stack:** Go 1.25, tRPC-Go, Protocol Buffers, SQLite/GORM, NATS JetStream, Vue 3/TypeScript, Binance REST/WebSocket, OKX REST/WebSocket.

**Approved design:** `docs/superpowers/specs/2026-07-28-trade-execution-module-design.md`

---

## Delivery Rules

- Treat the current schema and protobuf as replaceable. Do not add old-field
  aliases, migration readers, dual writes, or `reserved` declarations.
- Keep the scope to Binance/OKX, SPOT/USDT-linear SWAP, cross margin, NET
  position mode, MARKET/LIMIT, and base-asset quantity.
- Do not add hedge mode, isolated margin, quote-amount MARKET buys, delivery
  futures, STOP orders, or execution-algorithm plugins.
- Keep the Admin Secret API generic `provider` field only at the
  `secretclient` boundary. Use `Exchange` everywhere inside Trade.
- Use TDD for every behavioral task. A task is complete only after its focused
  tests pass and its commit contains no unrelated files.
- Preserve unrelated worktree changes. Stage only the files named by the task.

## Design-to-Task Traceability

| Approved decision | Implemented and proved by |
|---|---|
| One `Exchange` vocabulary | Tasks 1, 5, 6, 14 |
| `ExchangeAccount` replaces Account plus TradeChannel | Tasks 3, 4, 6, 13 |
| Public method name is `SyncAccount` | Tasks 3, 9, 11, 13, 15 |
| Binance and OKX SPOT/SWAP | Tasks 5, 7, 15 |
| MARKET and LIMIT with base quantity | Tasks 3, 5, 7, 8, 15 |
| USDT-linear, cross-margin, NET-mode SWAP | Tasks 3, 5, 7, 15 |
| Leverage per ExchangeAccount and Symbol | Tasks 3, 4, 6, 7, 15 |
| Strategy publishes final target quantities | Tasks 2, 10, 12, 15 |
| Continuous target convergence | Tasks 8, 10, 15 |
| Exchange-authoritative account and Position state | Tasks 4, 7, 9, 15 |
| Nine-table schema with local double-entry ledger | Tasks 4, 8, 9, 15 |
| Two public services only | Tasks 3, 11, 13, 14 |
| No Saga, fixed Rebalance Legs, or execution plugins | Tasks 5, 10, 14 |

## Final File Structure

```text
modules/trade/
  internal/
    application/
      account/
        service.go
        service_test.go
      accountsync/
        service.go
        service_test.go
      order/
        service.go
        service_test.go
        validator.go
        validator_test.go
      target/
        executor.go
        executor_test.go
    domain/
      exchangeaccount/
        account.go
        account_test.go
      execution/
        target.go
        target_test.go
      instrument/
        instrument.go
        instrument_test.go
      order/
        order.go
        order_test.go
        spec.go
        state.go
      position/
        position.go
        position_test.go
      shared/
        decimal.go
        decimal_test.go
        ids.go
    eventconsumer/
      jetstream.go
      target.go
      target_test.go
    exchange/
      adapter.go
      errors.go
      errors_test.go
      registry.go
      registry_test.go
      types.go
      binance/
      okx/
      httpclient/
    infra/
      store/
        store.go
        account.go
        account_test.go
        execution.go
        execution_test.go
        fact.go
        fact_test.go
        target.go
        target_test.go
    rpc/
      account.go
      account_test.go
      convert.go
      execution.go
      execution_test.go
      register.go
    runtime/
      manager.go
      manager_test.go
      session.go
      session_test.go
      target_worker.go
      target_worker_test.go
    secretclient/
    telemetry/
  proto/
    trade_service.proto
    tradegen/
  schema/
    account.sql
    execution.sql
    instrument.sql
    ledger.sql
    schema.go
    schema_test.go
  test/
    account_sync_e2e_test.go
    spot_market_e2e_test.go
    strategy_target_e2e_test.go
    swap_execution_e2e_test.go
    uncertain_order_e2e_test.go
```

The rewrite deletes the current `internal/service`,
`internal/infra/exchangebridge`, `internal/application/rebalance`,
`internal/application/reconciliation`, cancel-replace Saga, and unused
execution-plan code after their behavior has moved into the final packages.

### Task 1: Lock the Greenfield Contract and Terminology

**Files:**
- Create: `scripts/check-trade-exchange-terminology.go`
- Create: `scripts/test-trade-exchange-terminology.sh`
- Modify: `scripts/check-package-boundaries.sh`
- Modify: `scripts/verify-event-contracts.sh`
- Test: `scripts/test-trade-exchange-terminology.sh`

- [ ] **Step 1: Add a failing terminology fixture test**

Create a shell test that builds the checker, runs it against temporary Go,
protobuf, TypeScript, YAML, and Markdown fixtures, and asserts:

```text
ExchangeAccount, ExchangeAdapter, ExchangeOrder -> accepted
Trade Provider, Broker, Venue, Platform         -> rejected
secretclient external json:"provider"           -> accepted
OpenTelemetry TracerProvider                    -> ignored
Vue ConfigProvider                              -> ignored
```

Run:

```bash
bash scripts/test-trade-exchange-terminology.sh
```

Expected: FAIL because `check-trade-exchange-terminology.go` does not exist.

- [ ] **Step 2: Implement the source-aware terminology checker**

The checker must inspect `modules/trade`, `packages/tradeeventpb`, and
`web/src/api/trade`. It must report filename and line for identifiers or
domain-facing fields that use an Exchange synonym:

```go
var forbiddenExchangeTerms = regexp.MustCompile(
    `(?i)(exchange.*\b(provider|broker|venue|platform)\b|` +
        `\b(provider|broker|venue|platform).*(binance|okx|exchange))`,
)
```

Use an explicit allowlist for:

```text
modules/trade/internal/secretclient external JSON field provider
generated protobuf files
third-party Provider type names unrelated to Exchange
```

Do not blanket-reject every English word `provider`; the check governs only
Exchange vocabulary.

- [ ] **Step 3: Wire the checker into repository verification**

Add:

```bash
go run ./scripts/check-trade-exchange-terminology.go
```

to `scripts/check-package-boundaries.sh`, and run the fixture script from
`scripts/verify-event-contracts.sh`.

- [ ] **Step 4: Verify RED against the current Trade tree**

Run:

```bash
bash scripts/test-trade-exchange-terminology.sh
go run ./scripts/check-trade-exchange-terminology.go
```

Expected: the fixture test passes; the repository scan fails on
`SyncExchangeAccountsReq.provider`, `ExchangeSecret.Provider`, and Trade-local
`provider` variables that refer to Binance or OKX.

- [ ] **Step 5: Commit the executable rule**

```bash
git add scripts/check-trade-exchange-terminology.go \
  scripts/test-trade-exchange-terminology.sh \
  scripts/check-package-boundaries.sh \
  scripts/verify-event-contracts.sh
git commit -m "test(trade): enforce Exchange terminology"
```

### Task 2: Replace the Strategy-to-Trade Event Contract

**Files:**
- Modify: `packages/tradeeventpb/trade_events.proto`
- Regenerate: `packages/tradeeventpb/trade_events.pb.go`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/validation.go`
- Modify: `packages/events/validation_test.go`
- Modify: `packages/events/events_test.go`
- Modify: `scripts/verify-event-contracts.sh`

- [ ] **Step 1: Write failing Registry and validator tests**

Add tests for the exact new payload:

```protobuf
message TargetPosition {
  string instrument_id = 1;
  string symbol = 2;
  string target_quantity = 3;
}

message TargetIntent {
  string execution_id = 1;
  string strategy_run_id = 2;
  string execution_binding_id = 3;
  string exchange_account_id = 4;
  string data_revision = 5;
  uint64 command_sequence = 6;
  int64 not_after_unix_ms = 7;
  repeated TargetPosition targets = 8;
}
```

The tests must reject:

- Empty execution, strategy run, binding, Exchange account, or data revision.
- Zero command sequence.
- Expired or non-positive `not_after_unix_ms`.
- Duplicate symbol.
- Empty symbol or non-decimal target quantity.
- Event envelope ID different from `execution_id`.
- Event subject different from `execution_binding_id`.

Run:

```bash
cd packages/events
go test ./...
```

Expected: FAIL because `TargetIntent` and `TradeTargetRequested` do not exist.

- [ ] **Step 2: Replace the protobuf without compatibility fields**

Replace `RebalanceTarget` and `RebalanceRequested` with the messages above.
Do not retain old fields or field numbers as `reserved`.

Generate:

```bash
make -C packages/tradeeventpb all
```

- [ ] **Step 3: Rename the Registry event**

Replace:

```go
TradeRebalanceRequested
```

with:

```go
TradeTargetRequested = declareEvent(
    "trade.target.requested",
    1,
    "MOOX_TRADE",
    "strategy",
    func() proto.Message { return &tradeeventpb.TargetIntent{} },
    validateTradeTargetRequested,
)
```

Update Registry tests, subject rendering, and the event-contract verifier.
Do not add an alias for the old event name.

- [ ] **Step 4: Run event package tests**

```bash
cd packages/events
go test -count=1 ./...

cd ../tradeeventpb
go test -count=1 ./...

cd ../..
bash scripts/verify-event-contracts.sh
```

Expected: package tests pass. The verifier may still fail in Strategy and
Trade because their callers still use the deleted payload; record that as the
expected cross-module RED for later tasks.

- [ ] **Step 5: Commit the new public event**

```bash
git add packages/tradeeventpb packages/events scripts/verify-event-contracts.sh
git commit -m "refactor(trade): define target quantity event"
```

### Task 3: Replace the Trade RPC Contract

**Files:**
- Modify: `modules/trade/proto/trade_service.proto`
- Regenerate: `modules/trade/proto/tradegen/trade_service.pb.go`
- Regenerate: `modules/trade/proto/tradegen/trade_service.trpc.go`
- Modify: `modules/trade/proto/tradegen/validation.go`
- Modify: `modules/trade/proto/tradegen/security.go`
- Test: `modules/trade/proto/tradegen/security_test.go`

- [ ] **Step 1: Write failing protobuf-shape tests**

Extend `security_test.go` with descriptor assertions that require exactly two
services:

```text
trpc.moox.trade.ExchangeAccountService
trpc.moox.trade.TradeExecutionService
```

Require these methods:

```text
ExchangeAccountService:
  CreateAccount, UpdateAccount, GetAccount, ListAccounts,
  SetLeverage, PauseAccount, SyncAccount

TradeExecutionService:
  PlaceOrder, CancelOrder, CancelAllOrders,
  SubmitTarget, GetExecution, ListExecutions,
  GetOrder, ListOrders, ListFills, ListPositions
```

Assert that no old AccountSvc, ChannelSvc, ApiKeySvc, FundSvc, RebalanceSvc,
TradeOpsSvc, `provider`, `channel_id`, quote-amount, STOP, Saga, or
`SyncPositions` declaration remains.

Run:

```bash
cd modules/trade/proto/tradegen
go test ./...
```

Expected: FAIL on the current eleven-service descriptor.

- [ ] **Step 2: Define the exact public enums**

Use:

```protobuf
enum Exchange {
  EXCHANGE_UNSPECIFIED = 0;
  EXCHANGE_BINANCE = 1;
  EXCHANGE_OKX = 2;
}

enum MarketType {
  MARKET_TYPE_UNSPECIFIED = 0;
  MARKET_TYPE_SPOT = 1;
  MARKET_TYPE_SWAP = 2;
}

enum ExecutionMode {
  EXECUTION_MODE_UNSPECIFIED = 0;
  EXECUTION_MODE_PAPER = 1;
  EXECUTION_MODE_LIVE = 2;
}

enum OrderType {
  ORDER_TYPE_UNSPECIFIED = 0;
  ORDER_TYPE_MARKET = 1;
  ORDER_TYPE_LIMIT = 2;
}

enum TimeInForce {
  TIME_IN_FORCE_UNSPECIFIED = 0;
  TIME_IN_FORCE_GTC = 1;
  TIME_IN_FORCE_IOC = 2;
  TIME_IN_FORCE_FOK = 3;
}

enum PositionSide {
  POSITION_SIDE_UNSPECIFIED = 0;
  POSITION_SIDE_NET = 1;
}
```

- [ ] **Step 3: Define the exact order request**

```protobuf
message PlaceOrderReq {
  string exchange_account_id = 1;
  string client_order_id = 2;
  string symbol = 3;
  OrderType order_type = 4;
  TimeInForce time_in_force = 5;
  OrderSide side = 6;
  PositionSide position_side = 7;
  string quantity = 8;
  optional string limit_price = 9;
  bool reduce_only = 10;
  string source = 11;
  string strategy_execution_id = 12;
}
```

Define matching Order, Fill, Position, ExchangeAccount,
ExchangeAccountSnapshot, TargetExecution, and paginated query messages with
the field set approved in the design. Every decimal remains a string. Every
timestamp uses Unix milliseconds.

- [ ] **Step 4: Define account requests with concise method context**

Use `CreateAccountReq`, `UpdateAccountReq`, `GetAccountReq`,
`ListAccountsReq`, `SetLeverageReq`, `PauseAccountReq`, and `SyncAccountReq`.
The entity returned by these messages is still named `ExchangeAccount`.

`SyncAccountRsp` contains:

```protobuf
common.RetInfo ret_info = 1;
int32 fills_ingested = 2;
int32 orders_updated = 3;
int32 positions_updated = 4;
bool account_snapshot_updated = 5;
int32 unknown_orders_resolved = 6;
bool ready = 7;
repeated string warnings = 8;
```

- [ ] **Step 5: Generate and test the new API**

```bash
make -C modules/trade/proto all
cd modules/trade/proto/tradegen
go test -count=1 ./...
```

Expected: PASS with exactly two service descriptors and no removed contract.

- [ ] **Step 6: Commit the API replacement**

```bash
git add modules/trade/proto
git commit -m "refactor(trade): replace public execution API"
```

### Task 4: Rebuild the SQLite Schema and Single Store

**Files:**
- Rewrite: `modules/trade/schema/account.sql`
- Rewrite: `modules/trade/schema/execution.sql`
- Create: `modules/trade/schema/instrument.sql`
- Rewrite: `modules/trade/schema/ledger.sql`
- Delete: `modules/trade/schema/bus.sql`
- Delete: `modules/trade/schema/order.sql`
- Delete: `modules/trade/schema/rebalance.sql`
- Delete: `modules/trade/schema/sync.sql`
- Modify: `modules/trade/schema/schema.go`
- Modify: `modules/trade/schema/schema_test.go`
- Rewrite: `modules/trade/internal/infra/store/store.go`
- Create: `modules/trade/internal/infra/store/account.go`
- Create: `modules/trade/internal/infra/store/execution.go`
- Create: `modules/trade/internal/infra/store/fact.go`
- Create: `modules/trade/internal/infra/store/target.go`
- Test: corresponding `*_test.go` files

- [ ] **Step 1: Write failing schema inventory tests**

Require exactly these owned tables:

```text
t_exchange_accounts
t_exchange_instruments
t_trade_orders
t_order_fills
t_exchange_positions
t_target_executions
t_ledger_transactions
t_ledger_entries
t_trade_balance_projections
```

Reject every deleted table from the approved design, including the six tables
merged into the nine-table schema. Load `schema.AllSQL()` into an empty SQLite
database and assert all foreign keys and unique indexes can be created.

Run:

```bash
cd modules/trade
go test ./schema
```

Expected: FAIL because the current schema still contains channels, API keys,
sync cursors, plans, slices, sagas, and Rebalance tables.

- [ ] **Step 2: Define the account and instrument keys**

Use:

```text
t_exchange_accounts:
  PRIMARY KEY (c_space_id, c_exchange_account_id)
  UNIQUE (c_space_id, c_name)

t_exchange_instruments:
  PRIMARY KEY (c_exchange, c_market_type, c_symbol)
```

Store Exchange, market type, execution mode, credential secret ID, settlement
asset, margin mode, status, pause state, last sync/ready timestamps, and last
error on the account row.

Also store:

```text
c_sync_symbols_json
c_leverage_settings_json
c_fill_cursors_json
c_snapshot_json
c_snapshot_source_time
```

`c_sync_symbols_json` is the explicit SPOT synchronization universe. It lets
first-start catch-up inspect a manually traded symbol even when its asset has
already been sold to zero. Catch-up remains bounded by the history window the
Exchange API exposes. `c_fill_cursors_json` stores one cursor per symbol on
the same account row; it is not a separate table.
`c_leverage_settings_json` is a canonical symbol-to-leverage object.
`c_snapshot_json` contains only the latest typed ExchangeAccountSnapshot,
including balances, equity, available funds, and margin values. Validate and
encode both through domain types in the Store; application code must not
manipulate arbitrary JSON maps.

- [ ] **Step 3: Define immutable facts and execution state**

Use these uniqueness scopes:

```text
t_trade_orders:
  UNIQUE (c_space_id, c_order_id)
  UNIQUE (c_space_id, c_exchange_account_id, c_client_order_id)

t_order_fills:
  UNIQUE (
    c_space_id,
    c_exchange_account_id,
    c_symbol,
    c_exchange_trade_id
  )

t_exchange_positions:
  PRIMARY KEY (
    c_space_id,
    c_exchange_account_id,
    c_symbol,
    c_position_side
  )

t_target_executions:
  UNIQUE (c_space_id, c_execution_id)
  UNIQUE (c_space_id, c_execution_binding_id, c_command_sequence)
  UNIQUE (c_space_id, c_event_id)
```

Order rows must include every `OrderSpec` field plus Exchange, MarketType,
ExchangeOrderID, state, filled quantity, average price, reservation totals,
source, strategy execution ID, and version.

`t_target_executions` stores canonical `c_targets_json`, event ID, execution
binding ID, command sequence, expiry, processing status, and progress. These
columns replace separate TargetPosition, Inbox, and CommandOffset tables.
Accepting a target and advancing its sequence is one transaction.

- [ ] **Step 4: Keep the local double-entry ledger focused**

Use:

```text
t_ledger_transactions:
  UNIQUE (c_space_id, c_transaction_id)
  UNIQUE (c_space_id, c_exchange_account_id, c_source_type, c_source_id)

t_ledger_entries:
  PRIMARY KEY (c_space_id, c_transaction_id, c_entry_no)

t_trade_balance_projections:
  PRIMARY KEY (c_space_id, c_exchange_account_id, c_asset, c_bucket)
```

Ledger transaction types are limited to reservation, reservation release,
Fill settlement, fee, and synchronization adjustment. Every transaction must
balance per asset across its ledger buckets. Insert LedgerEntries and update
BalanceProjections in the same SQLite transaction.

ExchangeAccountSnapshot remains authoritative. The local ledger is an audit
and pre-trade risk projection, not a replacement Exchange margin engine.

- [ ] **Step 5: Split one Store across focused files**

`store.Open(path)` must open one GORM handle, execute `schema.AllSQL()`, set a
bounded SQLite busy timeout, enable foreign keys, and expose one
`Transaction` method:

```go
func (s *Store) Transaction(
    ctx context.Context,
    fn func(*Tx) error,
) error
```

Account, Order, Fill, Position, TargetExecution, LedgerTransaction,
LedgerEntry, and BalanceProjection writes must all use this `Tx`. Remove the
second database manager from later bootstrap wiring rather than introducing
another repository abstraction.

- [ ] **Step 6: Validate schema formatting and Store behavior**

```bash
cd modules/trade
go test -count=1 ./schema ./internal/infra/store
go test -race -count=1 ./internal/infra/store
git diff --check -- modules/trade/schema modules/trade/internal/infra/store
```

Expected: PASS. Every SQL file loads into a fresh SQLite database and follows
the repository schema format. Add focused tests for malformed account JSON,
duplicate ExchangeTradeID, target sequence compare-and-set, unbalanced ledger
transactions, and atomic projection updates.

- [ ] **Step 7: Commit the persistence reset**

```bash
git add modules/trade/schema modules/trade/internal/infra/store
git commit -m "refactor(trade): rebuild execution persistence"
```

### Task 5: Consolidate the Domain and Exchange Interface

**Files:**
- Create: `modules/trade/internal/domain/exchangeaccount/account.go`
- Create: `modules/trade/internal/domain/exchangeaccount/account_test.go`
- Rewrite: `modules/trade/internal/domain/instrument/instrument.go`
- Rewrite: `modules/trade/internal/domain/instrument/instrument_test.go`
- Create: `modules/trade/internal/domain/order/spec.go`
- Rewrite: `modules/trade/internal/domain/order/order.go`
- Rewrite: `modules/trade/internal/domain/order/state.go`
- Rewrite: `modules/trade/internal/domain/order/order_test.go`
- Rewrite: `modules/trade/internal/domain/position/position.go`
- Rewrite: `modules/trade/internal/domain/position/position_test.go`
- Create: `modules/trade/internal/domain/execution/target.go`
- Create: `modules/trade/internal/domain/execution/target_test.go`
- Rewrite: `modules/trade/internal/exchange/adapter.go`
- Rewrite: `modules/trade/internal/exchange/types.go`
- Create: `modules/trade/internal/exchange/errors.go`
- Create: `modules/trade/internal/exchange/errors_test.go`
- Modify: `modules/trade/internal/exchange/registry.go`
- Delete: `modules/trade/internal/exchange/exchange.go`
- Delete: `modules/trade/internal/exchange/contracts.go`
- Delete: `modules/trade/internal/domain/execution/contracts.go`
- Delete: `modules/trade/internal/domain/execution/saga.go`
- Delete: `modules/trade/internal/domain/execution/saga_test.go`
- Delete: `modules/trade/internal/domain/rebalance/rebalance.go`
- Delete: `modules/trade/internal/domain/rebalance/rebalance_test.go`

- [ ] **Step 1: Write the OrderSpec validation matrix**

Add table tests for:

```text
SPOT MARKET: positive Quantity, no LimitPrice, no PositionSide, no ReduceOnly
SPOT LIMIT:  positive Quantity and LimitPrice, GTC/IOC/FOK
SWAP MARKET: positive Quantity, no LimitPrice, PositionSide NET
SWAP LIMIT:  positive Quantity and LimitPrice, PositionSide NET
```

Reject quote amount, zero or negative order quantity, SPOT ReduceOnly,
MARKET with TimeInForce, LIMIT without TimeInForce, stale ReferencePrice, and
unsupported market or order type.

Run:

```bash
cd modules/trade
go test ./internal/domain/order
```

Expected: FAIL because `OrderSpec` does not exist.

- [ ] **Step 2: Implement the approved core types**

Use:

```go
type OrderSpec struct {
    ExchangeAccountID   string
    ClientOrderID       string
    Symbol              string
    OrderType           exchange.OrderType
    TimeInForce         exchange.TimeInForce
    Side                exchange.Side
    PositionSide        exchange.PositionSide
    Quantity            shared.Decimal
    LimitPrice          *shared.Decimal
    ReferencePrice      shared.Decimal
    ReferencePriceAt    time.Time
    ReduceOnly          bool
    Source              string
    StrategyExecutionID string
}
```

Keep quantity positive on Orders. Use signed quantity only on NET Positions.
Keep the current exact decimal implementation; do not introduce float64.

- [ ] **Step 3: Define one typed Exchange error**

```go
type ErrorKind string

const (
    ErrorRejected            ErrorKind = "REJECTED"
    ErrorInsufficientBalance ErrorKind = "INSUFFICIENT_BALANCE"
    ErrorRateLimited         ErrorKind = "RATE_LIMITED"
    ErrorOrderNotFound       ErrorKind = "ORDER_NOT_FOUND"
    ErrorAuthentication      ErrorKind = "AUTHENTICATION"
    ErrorNotReady            ErrorKind = "NOT_READY"
    ErrorTransportUnknown    ErrorKind = "TRANSPORT_UNKNOWN"
)

type Error struct {
    Kind       ErrorKind
    HTTPStatus int
    Code       string
    Err        error
}
```

Add `IsKind(err, kind)` using `errors.As`. Delete string-matching error
classification when the adapters switch in later tasks.

- [ ] **Step 4: Define one account-bound ExchangeAdapter**

```go
type Adapter interface {
    Exchange() Exchange
    LoadInstruments(context.Context) ([]Instrument, error)
    GetAccountSnapshot(context.Context) (AccountSnapshot, error)
    ListPositionSnapshots(context.Context) ([]Position, error)
    ListOpenOrders(context.Context) ([]Order, error)
    ListRecentFills(
        context.Context,
        string,
    ) ([]Fill, string, error)
    PlaceOrder(context.Context, OrderRequest) (Order, error)
    CancelOrder(
        context.Context,
        string,
        string,
    ) (Order, error)
    SetLeverage(
        context.Context,
        string,
        shared.Decimal,
    ) error
    SetMarginMode(
        context.Context,
        string,
        MarginMode,
    ) error
    SubscribePrivate(context.Context, EventHandler) error
}
```

Bind credential, market type, and Exchange account configuration when the
Registry creates the adapter. Do not pass credentials through every call.

- [ ] **Step 5: Run focused domain and interface tests**

```bash
cd modules/trade
go test -count=1 ./internal/domain/... ./internal/exchange
go test -race -count=1 ./internal/domain/order ./internal/domain/execution
```

Expected: PASS with no Saga, Rebalance, dual Exchange interface, or Exchange
synonym.

- [ ] **Step 6: Commit the core vocabulary**

```bash
git add modules/trade/internal/domain modules/trade/internal/exchange
git commit -m "refactor(trade): consolidate execution domain"
```

### Task 6: Implement ExchangeAccount and Credential Resolution

**Files:**
- Create: `modules/trade/internal/application/account/service.go`
- Create: `modules/trade/internal/application/account/service_test.go`
- Modify: `modules/trade/internal/secretclient/client.go`
- Modify: `modules/trade/internal/secretclient/client_test.go`
- Modify: `modules/trade/internal/config/app.go`
- Modify: `modules/trade/internal/config/app_test.go`
- Modify: `modules/trade/config/app.yaml`
- Delete: `modules/trade/internal/service/`

- [ ] **Step 1: Write failing aggregate tests**

Prove that:

- An account requires SpaceID, ID, name, Exchange, market, execution mode,
  credential secret ID, and settlement asset.
- SPOT rejects margin mode and leverage rows.
- SWAP requires CROSS margin mode.
- Paper and live accounts with the same name cannot alias the same row.
- UpdateAccount changes only explicitly mutable fields.
- Disabled or paused accounts fail execution eligibility.
- Credential category must be `exchange`, its provider value must equal the
  account Exchange, and its status must be active.

Run:

```bash
cd modules/trade
go test ./internal/application/account
```

Expected: FAIL because the account application package does not exist.

- [ ] **Step 2: Implement one ExchangeAccount service**

Expose:

```go
type Service struct {
    Store        Store
    Secrets      SecretSource
    SessionState SessionState
}

func (s *Service) Create(
    context.Context,
    exchangeaccount.Account,
) (exchangeaccount.Account, error)
func (s *Service) Update(
    context.Context,
    UpdateCommand,
) (exchangeaccount.Account, error)
func (s *Service) SetLeverage(
    context.Context,
    string,
    string,
    shared.Decimal,
) error
func (s *Service) Pause(
    context.Context,
    string,
    bool,
    string,
) error
```

Use command-specific updates. Do not reconstruct a partial aggregate and write
its zero values.

- [ ] **Step 3: Rename Trade-local Provider fields**

Use these internal names:

```text
ExchangeSecret.Exchange
ListExchangeSecrets(ctx, exchange)
SyncExchangeAccountsOptions.Exchange
```

At the Admin HTTP boundary only:

```go
type listSecretsRequest struct {
    Provider string `json:"provider"`
}
```

Convert `Provider` to `exchange.Exchange` immediately after decoding. Do not
propagate `Provider` into the application or domain package.

- [ ] **Step 4: Make live encryption configuration fail closed**

Remove the checked-in default key. `MOOX_TRADE_ENCRYPTION_KEY` is optional for
paper-only startup but required before creating or enabling a live account.
Reject the old literal:

```text
moox-cloud-secret-key-32bytes
```

Tests must prove that live credential resolution cannot proceed with empty,
short, or checked-in default material.

- [ ] **Step 5: Remove the old DAO and second database manager**

Delete `internal/service`, including its Account, Channel, API key, Fund,
sync-cursor, DAO, and database manager types. Move any still-required
read-only behavior into the new application service and single Store.

Run:

```bash
cd modules/trade
go test -count=1 ./internal/application/account \
  ./internal/secretclient ./internal/config ./internal/infra/store
go test -race -count=1 ./internal/application/account
```

Expected: PASS. `rg -n 'internal/service|TradeChannel|ExchangeSecret\\.Provider'
modules/trade` returns no active production reference.

- [ ] **Step 6: Commit account consolidation**

```bash
git add modules/trade/internal/application/account \
  modules/trade/internal/secretclient \
  modules/trade/internal/config \
  modules/trade/config/app.yaml \
  modules/trade/internal/service
git commit -m "refactor(trade): consolidate Exchange accounts"
```

### Task 7: Complete Binance and OKX MARKET/SWAP Adapters

**Files:**
- Modify: `modules/trade/internal/exchange/binance/binance.go`
- Modify: `modules/trade/internal/exchange/binance/helpers.go`
- Modify: `modules/trade/internal/exchange/binance/private_stream.go`
- Modify: corresponding Binance tests
- Modify: `modules/trade/internal/exchange/okx/okx.go`
- Modify: `modules/trade/internal/exchange/okx/helpers.go`
- Modify: `modules/trade/internal/exchange/okx/private_stream.go`
- Modify: corresponding OKX tests
- Modify: `modules/trade/internal/exchange/httpclient/httpclient.go`
- Modify: `modules/trade/internal/exchange/httpclient/httpclient_test.go`
- Delete: `modules/trade/internal/infra/exchangebridge/`

- [ ] **Step 1: Write shared adapter contract tests**

Add a reusable test suite that each adapter must pass:

```text
SPOT MARKET omits price and TimeInForce
SPOT LIMIT transmits price and GTC/IOC/FOK
SWAP MARKET sends NET position mode
SWAP reduce-only close cannot increase a position
client_order_id survives request and response
HTTP/auth/rate/order errors become typed Exchange errors
EOF/deadline/cancel-after-dispatch/5xx become TRANSPORT_UNKNOWN
```

Run the suite against both adapters. Expected: FAIL because the current bridge
drops OrderSpec fields and the HTTP layer returns untyped errors.

- [ ] **Step 2: Normalize Binance USDT SWAP rules**

Parse `/fapi/v1/exchangeInfo` filters into `ExchangeInstrument`. For Binance
USDT perpetuals, Domain base quantity maps through the Exchange quantity step;
record the conversion explicitly rather than assuming SPOT rules.

Map:

```text
MARKET -> MARKET with no price
LIMIT  -> LIMIT with GTC/IOC/FOK
NET    -> BOTH/one-way position mode
ReduceOnly -> reduceOnly=true when Binance permits it
```

Reject hedge-mode account responses and non-USDT settlement assets.

- [ ] **Step 3: Normalize OKX contract quantity**

Parse `ctVal`, `ctValCcy`, `settleCcy`, `lotSz`, `minSz`, and `ctType`.
Enable only `instType=SWAP`, linear contracts, and USDT settlement.

Use exact Decimal conversion:

```text
exchange_contracts = base_quantity / contract_value
base_quantity      = exchange_contracts * contract_value
```

Require `exchange_contracts` to satisfy lot size and minimum size. Convert
REST orders, REST fills, private-stream fills, and position snapshots in both
directions.

- [ ] **Step 4: Normalize private events**

Every adapter event must include:

```go
type Fill struct {
    ExchangeTradeID string
    ExchangeOrderID string
    ClientOrderID   string
    Symbol          string
    Side            Side
    PositionSide    PositionSide
    Quantity        shared.Decimal
    Price           shared.Decimal
    Fee             shared.Decimal
    FeeAsset        string
    RealizedPnL     shared.Decimal
    SettlementAsset string
    LiquidityRole   string
    TradedAt        time.Time
}
```

Do not emit Exchange contract quantity above the adapter boundary.

- [ ] **Step 5: Delete the lossy bridge**

Update callers to use the single account-bound Adapter and Registry. Delete
`internal/infra/exchangebridge`; do not preserve a wrapper alias.

- [ ] **Step 6: Run adapter tests**

```bash
cd modules/trade
go test -count=1 ./internal/exchange/...
go test -race -count=1 ./internal/exchange/binance \
  ./internal/exchange/okx
```

Expected: PASS for SPOT/SWAP MARKET and LIMIT mappings, OKX conversion, typed
errors, and private Fill normalization.

- [ ] **Step 7: Commit Exchange adapter completion**

```bash
git add modules/trade/internal/exchange \
  modules/trade/internal/infra/exchangebridge
git commit -m "feat(trade): complete MARKET and SWAP adapters"
```

### Task 8: Implement the Order Engine and Fill Reducer

**Files:**
- Create: `modules/trade/internal/application/order/validator.go`
- Create: `modules/trade/internal/application/order/validator_test.go`
- Create: `modules/trade/internal/application/order/service.go`
- Create: `modules/trade/internal/application/order/service_test.go`
- Rewrite: `modules/trade/internal/application/consumer/fill.go`
- Rewrite: `modules/trade/internal/application/consumer/fill_test.go`
- Delete: `modules/trade/internal/application/command/`

- [ ] **Step 1: Write failing command-path tests**

Cover:

```text
SPOT MARKET buy reserves reference_price * quantity plus fee buffer
SPOT MARKET sell reserves base quantity
SPOT LIMIT buy reserves limit_price * quantity
SWAP open reserves reference_notional / leverage plus fee buffer
SWAP reduce-only close does not reserve opening margin
MARKET never parses an empty LimitPrice
disabled, paused, not-ready, stale-price, ownership mismatch rejected
```

Run:

```bash
cd modules/trade
go test ./internal/application/order
```

Expected: FAIL because the new order service does not exist.

- [ ] **Step 2: Add the minimal PreTradeValidator**

The validator checks only:

```text
ExchangeAccount ownership and status
session readiness
OrderSpec matrix
instrument status and quantity rule
reference price freshness
maximum child notional
available SPOT funds or SWAP margin
configured leverage ceiling
reduce-only direction
```

Return precise domain errors. Do not add a rule registry or generic risk
engine.

- [ ] **Step 3: Persist intention before Exchange submission**

Implement:

```go
func (s *Service) Place(
    ctx context.Context,
    spec order.OrderSpec,
) (order.Order, error)
```

The transaction creates Order and reservation with state PENDING. A local
worker moves it to SUBMITTING and calls ExchangeAdapter. A duplicate
`ExchangeAccountID + ClientOrderID` returns the existing order when the full
spec matches and returns an idempotency conflict when it differs.

- [ ] **Step 4: Handle uncertain submission without duplicate placement**

Map `ErrorTransportUnknown` to SUBMIT_UNKNOWN and retain the reservation.
Never retry PlaceOrder directly. Resolution queries Exchange open orders and
RecentFills by client ID:

```text
found order/fill -> attach Exchange IDs and continue
explicitly not found after bounded lookup window -> return to PENDING
ambiguous result -> remain SUBMIT_UNKNOWN
```

- [ ] **Step 5: Make cancel Fill-safe**

Cancel transitions OPEN/PARTIALLY_FILLED to CANCELING. A successful Exchange
cancel response triggers account synchronization; it does not immediately
release funds. The reducer applies final Fills first, then records CANCELED or
PARTIALLY_CANCELED and releases only unused reservation.

Keep terminal orders eligible for bounded late-Fill ingestion. Applying a new
unique Fill to CANCELED, PARTIALLY_CANCELED, or REJECTED must repair the Order
instead of returning an invalid transition.

- [ ] **Step 6: Implement one idempotent reducer**

The same reducer handles private stream and REST synchronization:

```go
func (r *Reducer) ApplyFill(
    context.Context,
    exchange.Fill,
    Source,
) (bool, error)
```

For SPOT, post the asset and fee ledger transaction. For SWAP, record fee and
realized PnL facts, update the estimated Position, and leave authoritative
margin/equity to snapshots. Fill insertion, Order update, reservation update,
ledger posting, and Position projection occur in one SQLite transaction.

- [ ] **Step 7: Run focused Order and Fill tests**

```bash
cd modules/trade
go test -count=1 ./internal/application/order \
  ./internal/application/consumer ./internal/domain/order \
  ./internal/infra/store
go test -race -count=1 ./internal/application/order \
  ./internal/application/consumer
```

Expected: PASS for MARKET, SWAP reservation, unknown submission, cancel with a
delayed partial Fill, and Fill idempotency.

- [ ] **Step 8: Commit the execution kernel**

```bash
git add modules/trade/internal/application/order \
  modules/trade/internal/application/consumer \
  modules/trade/internal/application/command \
  modules/trade/internal/domain/order \
  modules/trade/internal/infra/store
git commit -m "feat(trade): implement durable order execution"
```

### Task 9: Implement ExchangeSession and SyncAccount

**Files:**
- Create: `modules/trade/internal/application/accountsync/service.go`
- Create: `modules/trade/internal/application/accountsync/service_test.go`
- Create: `modules/trade/internal/runtime/session.go`
- Create: `modules/trade/internal/runtime/session_test.go`
- Create: `modules/trade/internal/runtime/manager.go`
- Create: `modules/trade/internal/runtime/manager_test.go`
- Modify: `modules/trade/proto/trade_service.proto`
- Regenerate: `modules/trade/proto/tradegen/trade_service.pb.go`
- Modify: `modules/trade/internal/domain/exchangeaccount/account.go`
- Modify: `modules/trade/internal/exchange/adapter.go`
- Modify: Binance and OKX adapters and private-stream tests
- Modify: `modules/trade/internal/infra/store/account.go`
- Modify: `modules/trade/internal/infra/store/execution.go`
- Modify: `modules/trade/internal/infra/store/fact.go`
- Modify: `modules/trade/internal/health/state.go`
- Modify: `modules/trade/internal/health/server.go`
- Modify: corresponding health tests
- Delete: `modules/trade/internal/application/reconciliation/`
- Delete: `modules/trade/internal/bootstrap/kernel_timers.go`
- Delete: `modules/trade/internal/bootstrap/kernel_timers_test.go`

- [ ] **Step 1: Write the ExchangeSession readiness test**

Use a scripted adapter to prove this exact order:

```text
SubscribePrivate starts and buffers events
LoadInstruments
SetMarginMode(CROSS) for SWAP
SetLeverage for every enabled SWAP symbol
GetAccountSnapshot
ListPositionSnapshots
ListOpenOrders
ListRecentFills
Apply snapshots and buffered events
READY
```

For an adapter such as OKX that needs instrument metadata to normalize SWAP
contract quantities, subscribe and receive the acknowledgement first, then
hold normalization behind a small adapter metadata gate until
`LoadInstruments` completes. Do not move the subscription after the metadata
request and do not issue the metadata request twice.

Also prove that a private-stream disconnect immediately clears readiness and
blocks Order submission.

Run:

```bash
cd modules/trade
go test ./internal/runtime
```

Expected: FAIL because ExchangeSession does not exist.

- [ ] **Step 2: Implement account-level synchronization**

Expose:

```go
type Result struct {
    FillsIngested         int
    OrdersUpdated         int
    PositionsUpdated      int
    AccountSnapshotUpdated bool
    UnknownOrdersResolved int
    Ready                 bool
    Warnings              []string
}

func (s *Service) SyncAccount(
    context.Context,
    string,
) (Result, error)
```

Apply data in this order:

```text
RecentFills
OpenOrders and terminal order lookups
Position snapshots
Account snapshot
unknown-order resolution
sync cursor and readiness metadata
```

Import unmanaged Exchange orders with source `EXTERNAL`. Do not cancel them.
For SPOT, query every explicitly configured sync symbol, not only symbols
inferred from current non-zero balances. Advance a fill cursor only after all
pages in the adapter's documented recovery window have been ingested. V1 uses
the Exchange's default bounded history where the API cannot page arbitrarily:
Binance USD-M first-start recovery is the most recent seven days. Older
one-off backfill is explicitly outside V1 rather than silently claimed as
complete.

Only a complete REST account snapshot advances the local full-snapshot
watermark used to rebase reservations. Partial private account events merge
their present fields but must not advance that watermark. Serialize full
account synchronization with order submission through the Store's per-account
lock.

- [ ] **Step 3: Implement one session per enabled account**

`runtime.Manager` watches enabled accounts, creates one session per ID, and
owns cancellation and shutdown. Each session performs a 30-second account
sync after it becomes ready. Use bounded retry delay for connection failure;
do not create a generic scheduler.

- [ ] **Step 4: Replace readiness calculation**

Health is ready only when:

```text
database is ready
EventBus is ready when enabled
every enabled live ExchangeAccount has a ready ExchangeSession
there are no unresolved configuration errors
```

Open-order count alone must not make private-stream readiness optional.

- [ ] **Step 5: Delete old order-only reconciliation and timers**

Remove the old `reconciliation` package and tRPC timer services. Session loops
own periodic account sync and unknown-state recovery.

- [ ] **Step 6: Run runtime and health tests**

```bash
cd modules/trade
go test -count=1 ./internal/application/accountsync \
  ./internal/runtime ./internal/health
go test -race -count=1 ./internal/runtime \
  ./internal/application/accountsync
```

Expected: PASS for initial snapshot, buffered private event, disconnect,
reconnect, manual SyncAccount, external order import, and readiness gating.

- [ ] **Step 7: Commit session lifecycle**

```bash
git add modules/trade/internal/application/accountsync \
  modules/trade/internal/application/account \
  modules/trade/internal/application/order \
  modules/trade/internal/domain/exchangeaccount \
  modules/trade/internal/domain/order \
  modules/trade/internal/exchange \
  modules/trade/internal/infra/store \
  modules/trade/internal/runtime \
  modules/trade/internal/health \
  modules/trade/proto/trade_service.proto \
  modules/trade/proto/tradegen/trade_service.pb.go \
  modules/trade/schema/account.sql \
  modules/trade/go.mod \
  modules/trade/internal/application/reconciliation \
  modules/trade/internal/bootstrap/bootstrap.go \
  modules/trade/internal/bootstrap/kernel_timers.go \
  modules/trade/internal/bootstrap/kernel_timers_test.go
git commit -m "feat(trade): add Exchange account synchronization"
```

### Task 10: Replace Rebalance Legs with Target Convergence

**Files:**
- Create: `modules/trade/internal/application/target/executor.go`
- Create: `modules/trade/internal/application/target/executor_test.go`
- Create: `modules/trade/internal/runtime/target_worker.go`
- Create: `modules/trade/internal/runtime/target_worker_test.go`
- Create: `modules/trade/internal/eventconsumer/target.go`
- Create: `modules/trade/internal/eventconsumer/target_test.go`
- Modify: `modules/trade/internal/eventconsumer/jetstream.go`
- Modify: `modules/trade/internal/domain/execution/target.go`
- Modify: `modules/trade/internal/domain/order/order.go`
- Modify: `modules/trade/internal/application/order/service.go`
- Modify: `modules/trade/internal/infra/store/target.go`
- Modify: `modules/trade/internal/infra/store/fact.go`
- Modify: `modules/trade/internal/exchange/adapter.go`
- Modify: `modules/trade/internal/exchange/binance/binance.go`
- Modify: `modules/trade/internal/exchange/okx/okx.go`
- Delete: `modules/trade/internal/eventconsumer/rebalance.go`
- Delete: `modules/trade/internal/eventconsumer/rebalance_test.go`
- Delete: `modules/trade/internal/eventconsumer/resolver.go`
- Delete: `modules/trade/internal/application/rebalance/`
- Modify: `modules/trade/internal/telemetry/metrics.go`

- [ ] **Step 1: Write convergence tests**

Cover:

```text
lower or duplicate sequence creates no TargetExecution
newer target replaces the desired quantity of an active execution
same-direction open remaining quantity is included in effective position
opposite active order is canceled before a new child order
same-direction active order prevents duplicate child order
expired target creates no child order
below-minimum residual completes with residual recorded
MARKET is the default child order type
```

For SWAP, prove:

```text
long to larger long -> one BUY child
long to smaller long -> reduce-only SELL
long to short -> reduce-only SELL to zero, wait, then SELL open
short to long -> reduce-only BUY to zero, wait, then BUY open
```

Run:

```bash
cd modules/trade
go test ./internal/application/target
```

Expected: FAIL because TargetExecutor does not exist.

- [ ] **Step 2: Persist only the latest desired target**

Record the event identity, command sequence, and target-position JSON in one
TargetExecution row:

```go
func (s *Store) AcceptTarget(
    ctx context.Context,
    envelope events.EventMessage,
    intent execution.TargetIntent,
) (accepted bool, err error)
```

The SQL compare-and-set updates a binding only when the new sequence is
greater. The unique event ID supplies inbox idempotency, and the greatest
accepted command sequence supplies the binding offset. It never creates
multiple independently advancing runs for one binding.

- [ ] **Step 3: Implement deterministic convergence**

Use:

```text
effective = confirmed_position + same_direction_open_remaining
remaining = desired_target - effective
```

The worker serializes by `space_id + exchange_account_id + symbol`. It
recomputes after Store wakeups and a small fallback timer. It submits at most
one child Order per lane at a time.

Apply optional `max_child_notional` as a deterministic quantity cap. This is
not a pluggable algorithm.

MARKET validation and reservation use a fresh reference price. Add one narrow
optional `ReferencePriceSource` capability to the Exchange adapters:
Binance uses the SPOT or USD-M ticker-price endpoint and OKX uses the market
ticker endpoint. TargetExecutor must not substitute the instrument price tick
for a market price.

- [ ] **Step 4: Handle timeout and terminal status**

When `not_after` passes:

- Stop creating child orders.
- Keep processing already submitted Orders and Fills.
- Mark the execution EXPIRED with remaining residual when no active child
  remains.

Expose RUNNING, COMPLETED, EXPIRED, FAILED, and PAUSED status plus target,
confirmed, effective, and residual quantities.

- [ ] **Step 5: Replace the EventBus consumer**

Consume `events.TradeTargetRequested`, validate envelope/payload identity,
call `AcceptTarget`, ACK accepted and stale commands, RETRY transient Store
errors, and TERM malformed or permanently unsupported commands.

- [ ] **Step 6: Run convergence and consumer tests**

```bash
cd modules/trade
go test -count=1 ./internal/application/target \
  ./internal/runtime ./internal/eventconsumer
go test -race -count=1 ./internal/application/target \
  ./internal/runtime ./internal/eventconsumer
```

Expected: PASS with no Rebalance Run, Leg, planner, or stale-target conflict.

- [ ] **Step 7: Commit TargetExecutor**

```bash
git add modules/trade/internal/application/target \
  modules/trade/internal/application/rebalance \
  modules/trade/internal/application/order \
  modules/trade/internal/domain/execution \
  modules/trade/internal/domain/order \
  modules/trade/internal/runtime \
  modules/trade/internal/eventconsumer \
  modules/trade/internal/exchange \
  modules/trade/internal/infra/store \
  modules/trade/internal/telemetry \
  docs/superpowers/plans/2026-07-28-trade-execution-module-rewrite.md
git commit -m "feat(trade): converge target positions"
```

### Task 11: Implement the Two RPC Services and Bootstrap

**Files:**
- Create: `modules/trade/internal/rpc/account.go`
- Create: `modules/trade/internal/rpc/account_test.go`
- Create: `modules/trade/internal/rpc/execution.go`
- Create: `modules/trade/internal/rpc/execution_test.go`
- Rewrite: `modules/trade/internal/rpc/convert.go`
- Rewrite: `modules/trade/internal/rpc/register.go`
- Delete: `modules/trade/internal/rpc/server.go`
- Delete: `modules/trade/internal/rpc/server_test.go`
- Rewrite: `modules/trade/internal/bootstrap/bootstrap.go`
- Rewrite: `modules/trade/internal/bootstrap/bootstrap_test.go`
- Delete: `modules/trade/internal/bootstrap/kernel_workers.go`
- Delete: obsolete kernel worker tests and wakeup files
- Modify: `modules/trade/config/trpc_go.yaml`
- Modify: `modules/trade/cmd/server/main.go`

- [ ] **Step 1: Write service-registration tests**

Assert that bootstrap registers only:

```text
trpc.moox.trade.ExchangeAccountService
trpc.moox.trade.TradeExecutionService
trpc.moox.trade.Health
trpc.moox.trade.metrics.timer
```

Reject old Account, Balance, Fund, API key, Channel, TradeOp, Order,
TradeQuery, Position, Rebalance, TradeOps, fill-reconcile timer, and
order-recovery timer names.

Run:

```bash
cd modules/trade
go test ./internal/rpc ./internal/bootstrap
```

Expected: FAIL on the old registrations.

- [ ] **Step 2: Implement concise account RPC methods**

Map:

```text
CreateAccount -> account.Service.Create
UpdateAccount -> account.Service.Update
GetAccount    -> Store.GetExchangeAccount
ListAccounts  -> Store.ListExchangeAccounts
SetLeverage   -> account.Service.SetLeverage
PauseAccount  -> account.Service.Pause
SyncAccount   -> accountsync.Service.SyncAccount
```

Return full `ExchangeAccount` entities. Preserve Space identity from
`spacecontext`; never trust a request SpaceID.

- [ ] **Step 3: Implement execution RPC methods**

`PlaceOrder` constructs one `OrderSpec`. It does not parse or default
Exchange/MarketType from caller values. `SubmitTarget` uses the same
TargetExecutor path as EventBus. Query methods read the single Store.

Cancel by local OrderID. Resolve the account, symbol, and client order ID from
the stored Order rather than accepting a caller-supplied Exchange route.

- [ ] **Step 4: Wire one Store and the runtime Manager**

Bootstrap order:

```text
load strict config
open one Store
create SecretClient
create Exchange Registry
create account/order/target/sync services
start runtime.Manager
start EventBus consumer
register RPC and health
register one shutdown function
```

Do not open the SQLite path twice.

- [ ] **Step 5: Reduce tRPC configuration**

Keep two RPC service entries on ports 11200 and 11201, Health on 11210, and
metrics. Remove the old service and recovery timer ports. Update comments to
the exact new service names.

- [ ] **Step 6: Run RPC and bootstrap tests**

```bash
cd modules/trade
go test -count=1 ./internal/rpc ./internal/bootstrap \
  ./cmd/server
go test -race -count=1 ./internal/rpc ./internal/bootstrap
```

Expected: PASS with two services, one Store handle, runtime shutdown, and
correct MARKET/SWAP field round trips.

- [ ] **Step 7: Commit service consolidation**

```bash
git add modules/trade/internal/rpc \
  modules/trade/internal/bootstrap \
  modules/trade/config/trpc_go.yaml \
  modules/trade/cmd/server
git commit -m "refactor(trade): consolidate RPC and bootstrap"
```

### Task 12: Make Strategy Publish Final Target Quantities

**Files:**
- Modify: `modules/strategy/internal/domain/types.go`
- Modify: `modules/strategy/internal/engine/engine.go`
- Modify: `modules/strategy/internal/engine/engine_test.go`
- Modify: `modules/strategy/internal/store/commit.go`
- Modify: `modules/strategy/internal/store/commit_test.go`
- Modify: `modules/strategy/internal/store/bindings.go`
- Modify: `modules/strategy/internal/store/bindings_test.go`
- Modify: `modules/strategy/schema/strategy.sql`
- Modify: `modules/strategy/proto/strategy.proto`
- Regenerate: `modules/strategy/proto/strategygen/`
- Modify: `modules/strategy/internal/rpc/frontend_service.go`
- Modify: corresponding RPC tests
- Modify: `modules/strategy/pysdk/moox_strategy/types.py`
- Modify: `modules/strategy/pysdk/moox_strategy/validate.py`
- Modify: `modules/strategy/pysdk/tests/test_validate.py`
- Modify: `modules/strategy/pyworker/worker.py`
- Modify: `modules/strategy/pyworker/test_worker.py`
- Modify: Strategy outbox tests and E2E tests

- [ ] **Step 1: Write the new Strategy output tests**

Require:

```json
{
  "action": "rebalance",
  "targets": [
    {
      "instrument_id": "BTC-USDT",
      "symbol": "BTCUSDT",
      "target_quantity": "0.01"
    }
  ],
  "next_state": {}
}
```

Reject `target_weight`, duplicate symbol, missing quantity, fraction syntax,
NaN/Inf, and a quantity with surrounding whitespace.

Run:

```bash
cd modules/strategy
go test ./internal/engine
python3 -m unittest discover -s pysdk/tests
python3 -m unittest pyworker/test_worker.py
```

Expected: FAIL because the current contract requires `target_weight`.

- [ ] **Step 2: Replace TargetWeight with TargetPosition**

Use:

```go
type TargetPosition struct {
    InstrumentID   string `json:"instrument_id"`
    Symbol         string `json:"symbol"`
    TargetQuantity string `json:"target_quantity"`
    Reason         string `json:"reason,omitempty"`
    SourceTime     string `json:"source_time,omitempty"`
    DataRevision   string `json:"data_revision,omitempty"`
}
```

Update Output, previous-target state, frontend queries, Python SDK, worker
validation, and generated Strategy protobuf. Do not keep a compatibility
`target_weight` field.

- [ ] **Step 3: Simplify execution bindings**

Replace:

```text
account_id
channel_id
capital_amount
quote_asset
```

with:

```text
exchange_account_id
```

Keep mode `observe`, `paper`, or `live` for UI capability and audit. Trade
still derives authoritative execution mode from ExchangeAccount and rejects a
mismatch.

Update `SetExecutionModeReq` to accept binding ID, mode,
ExchangeAccountID, operation ID, and reason.

- [ ] **Step 4: Publish TargetIntent atomically**

For every enabled non-observe execution binding:

```go
payload := &tradeeventpb.TargetIntent{
    ExecutionId:       eventID,
    StrategyRunId:     task.RunID,
    ExecutionBindingId: execution.ExecutionBindingID,
    ExchangeAccountId: execution.ExchangeAccountID,
    DataRevision:      task.DataRevision,
    CommandSequence:   commandSequence,
    NotAfterUnixMs:    occurredAt.Add(executionTTL).UnixMilli(),
    Targets:           targetPositions(output.Targets),
}
```

Keep sequence advancement, Strategy Run commit, state update, and Outbox
insert in one SQLite transaction.

- [ ] **Step 5: Run Strategy contract and Outbox tests**

```bash
cd modules/strategy
CGO_ENABLED=1 go test -count=1 ./internal/domain \
  ./internal/engine ./internal/store ./internal/outbox \
  ./internal/rpc ./test
CGO_ENABLED=1 go test -race -count=1 ./internal/store \
  ./internal/outbox
python3 -m unittest discover -s pysdk/tests
python3 -m unittest pyworker/test_worker.py
```

Expected: PASS with only final target quantities in Strategy output and the
public Trade event.

- [ ] **Step 6: Commit the Strategy boundary**

```bash
git add modules/strategy packages/tradeeventpb
git commit -m "refactor(strategy): publish target quantities"
```

### Task 13: Collapse Admin and Web onto the Two Public Services

**Files:**
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Modify: `modules/admin/internal/service/sysdeploy/acceptance_test.go`
- Modify: `modules/admin/README.md`
- Modify: `web/src/api/trade/http.ts`
- Modify: `web/src/api/trade/index.ts`
- Modify: `web/src/api/trade/types.ts`
- Create: `web/src/api/trade/trade.test.ts`
- Modify: `web/src/views/trading/account-overview/account-overview.vue`
- Modify: `web/src/views/trading/position-detail/position-detail.vue`
- Modify: `web/src/views/trading/trade-record/trade-record.vue`

- [ ] **Step 1: Make the deployment catalog tests require two services**

Replace the old eleven-service assertions with exactly:

```text
trade_exchange_account -> 127.0.0.1:11200
  trpc.moox.trade.ExchangeAccountService

trade_execution -> 127.0.0.1:11201
  trpc.moox.trade.TradeExecutionService
```

Both services are internal and sensitive. Assert that old IDs such as
`trade_account`, `trade_channel`, `trade_apikey`, `trade_order`,
`trade_rebalance`, and `trade_ops` are absent.

Run:

```bash
cd modules/admin
go test ./internal/service/sysdeploy
```

Expected: FAIL because the catalog still publishes the legacy service split.

- [ ] **Step 2: Replace the deployment catalog entries**

Update `defaults.go` and the Admin service documentation. Keep the process
name `trade`; only the tRPC service IDs and ports collapse. Do not expose a
local health URL for either entry.

Run:

```bash
cd modules/admin
go test ./internal/service/sysdeploy
```

Expected: PASS with exactly two Trade deployment records.

- [ ] **Step 3: Write failing Web API contract tests**

Test that:

```ts
tradeServiceMap.exchangeAccount === "trade_exchange_account";
tradeServiceMap.execution === "trade_execution";
```

Also test request construction for:

- `CreateAccount`, `SetLeverage`, `PauseAccount`, and `SyncAccount`;
- SPOT MARKET order without `price`;
- SWAP LIMIT order with `leverage` and `reduce_only`;
- `SubmitTarget`, `ListFills`, and `ListPositions`.

Assert that no API exports AccountSvc, ChannelSvc, ApiKeySvc, FundSvc,
RebalanceSvc, Saga, transfer, or dust-conversion operations.

Run:

```bash
cd web
pnpm test -- src/api/trade/trade.test.ts
```

Expected: FAIL because the Web client still maps the legacy services.

- [ ] **Step 4: Replace the Web Trade API surface**

Use only two transport groups:

```ts
export const tradeServiceMap = {
  exchangeAccount: "trade_exchange_account",
  execution: "trade_execution"
} as const;
```

Mirror the new protobuf names and enums exactly. Use strings for quantities,
prices, fees, PnL, leverage values, and timestamps crossing the JSON boundary.
Do not retain aliases for the old field or method names.

- [ ] **Step 5: Update the three Trade views**

The account view must show Exchange, account type, mode, readiness,
`last_synced_at`, and a `SyncAccount` command. The position view must show
SPOT/SWAP, side, base quantity, entry/mark/liquidation price, leverage, margin,
unrealized PnL, and source time.

The trade-record view must:

- hide and clear price for MARKET;
- require price for LIMIT;
- show leverage and `reduce_only` only for SWAP;
- disable submission when the ExchangeAccount is paused or not ready;
- list Orders and Fills as separate facts.

Do not add transfer, dust-conversion, grid, TWAP, or hedge-mode controls.

- [ ] **Step 6: Run the Admin and Web checks**

```bash
cd modules/admin
go test -count=1 ./internal/service/sysdeploy

cd ../../web
pnpm test
pnpm run lint:eslint:check
pnpm run build:prod
```

Expected: PASS. The browser has one account surface and one execution surface,
matching the public RPC contract.

- [ ] **Step 7: Commit the integration surface**

```bash
git add modules/admin/internal/service/sysdeploy modules/admin/README.md \
  web/src/api/trade web/src/views/trading
git commit -m "refactor(trade): collapse public service surface"
```

### Task 14: Remove Obsolete Paths and Make the New Vocabulary Enforceable

**Files:**
- Delete: `modules/trade/internal/service/`
- Delete: `modules/trade/internal/infra/exchangebridge/`
- Delete: `modules/trade/internal/application/rebalance/`
- Delete: `modules/trade/internal/application/reconciliation/`
- Delete: obsolete files under `modules/trade/internal/application/command/`
- Delete: obsolete files under `modules/trade/internal/domain/rebalance/`
- Delete: obsolete execution-plan, slice, Saga, transfer, and dust code
- Modify: `modules/trade/config/app.yaml`
- Modify: `modules/trade/config/trpc_go.yaml`
- Modify: `modules/trade/README.md`
- Modify: `modules/trade/DESIGN.md`
- Modify: `modules/trade/docs/exchange-apis.md`
- Modify: `docs/架构总览.md`
- Modify: `docs/策略模块架构设计.md`
- Modify: `scripts/check-package-boundaries.sh`
- Modify: `scripts/verify-event-contracts.sh`

- [ ] **Step 1: Write failing static-boundary assertions**

Extend the repository checks to reject:

```text
TradeChannel
RebalanceRequested
RebalanceRun
RebalanceLeg
execution_plan
execution_slice
reconciliation
Saga
Provider/Broker/Venue/Platform used as Exchange synonyms
```

The only permitted external `provider` field is the Admin Secret payload
inside `modules/trade/internal/secretclient`.

Run:

```bash
bash scripts/test-trade-exchange-terminology.sh
bash scripts/check-package-boundaries.sh
bash scripts/verify-event-contracts.sh
```

Expected: FAIL while legacy packages and declarations still exist.

- [ ] **Step 2: Delete replaced code instead of wrapping it**

Delete the old service/DAO/database layer, the second Exchange abstraction,
fixed Rebalance planner, reconciliation package, cancel-replace Saga,
execution plan/slice types, and non-execution money-management APIs.

Keep only code reachable from:

```text
ExchangeAccountService
TradeExecutionService
TargetIntent consumer
ExchangeSession
```

After every package deletion, use `rg` to update direct callers rather than
adding forwarding types.

- [ ] **Step 3: Simplify runtime configuration**

Keep only configuration actually consumed by the final process:

```yaml
database:
eventbus:
admin:
exchanges:
  binance:
  okx:
runtime:
telemetry:
```

Exchange credentials remain references to Admin secrets, never values in
YAML. Remove stale per-service configuration, unused HTTP health listeners,
old reconciliation timers, and Saga settings. Register the two tRPC services
in `trpc_go.yaml`.

- [ ] **Step 4: Rewrite documentation against the approved design**

Document:

- SPOT and USDT-linear SWAP scope;
- MARKET and LIMIT validation matrix;
- base-asset quantity semantics;
- `ExchangeAccount`, `ExchangeSession`, and `SyncAccount`;
- TargetIntent convergence and readiness gate;
- Exchange-authoritative SWAP account state;
- Binance and OKX symbol/contract conversion;
- explicit exclusions from this first version.

Do not describe old APIs as deprecated. They no longer exist.

- [ ] **Step 5: Prove the old surface is gone**

```bash
rg -n \
  'TradeChannel|RebalanceRequested|RebalanceRun|RebalanceLeg|execution_plan|execution_slice|reconciliation|Saga' \
  modules/trade packages/tradeeventpb web/src/api/trade \
  --glob '*.{go,proto,ts,vue,yaml}' \
  --glob '!**/*_test.go' --glob '!**/*.test.ts'

rg -n \
  'target_weight|capital_amount|quote_asset|channel_id' \
  modules/strategy packages/tradeeventpb modules/trade web/src/api/trade \
  --glob '*.{go,proto,py,ts,vue,yaml}' \
  --glob '!**/*_test.go' --glob '!**/test/**' --glob '!**/*.test.ts'

bash scripts/test-trade-exchange-terminology.sh
bash scripts/check-package-boundaries.sh
bash scripts/verify-event-contracts.sh
```

Expected: both `rg` commands return no matches and exit 1; all three scripts
PASS. A legitimate unrelated use must be excluded by a narrow, documented
checker rule rather than a broad directory exemption.

- [ ] **Step 6: Run module tests after deletion**

```bash
cd modules/trade
go test -count=1 ./...
go vet ./...

cd ../strategy
go test -count=1 ./...
```

Expected: PASS without compatibility packages.

- [ ] **Step 7: Commit the greenfield cleanup**

```bash
git add modules/trade modules/strategy packages/tradeeventpb \
  web/src/api/trade scripts/check-package-boundaries.sh \
  scripts/verify-event-contracts.sh \
  docs/架构总览.md docs/策略模块架构设计.md
git commit -m "refactor(trade): remove obsolete execution paths"
```

Before committing, inspect `git diff --cached --name-status` and unstage any
unrelated pre-existing document.

### Task 15: Prove the Execution Paths with E2E and Fault Tests

**Files:**
- Create: `modules/trade/test/spot_market_e2e_test.go`
- Create: `modules/trade/test/swap_execution_e2e_test.go`
- Create: `modules/trade/test/account_sync_e2e_test.go`
- Create: `modules/trade/test/strategy_target_e2e_test.go`
- Create: `modules/trade/test/uncertain_order_e2e_test.go`
- Delete: superseded `modules/trade/test/trade_e2e_test.go`
- Delete: superseded `modules/trade/test/trade_e2e_more_test.go`
- Modify: `modules/strategy/test/strategy_trade_external_e2e_test.go`
- Modify: `scripts/test-strategy-trade-event-e2e.sh`
- Create: `modules/trade/scripts/testnet-smoke.sh`

- [ ] **Step 1: Build a deterministic fake Exchange**

The fake must record normalized requests and independently emit:

- accepted orders;
- partial and full Fills;
- delayed Fill after cancel acknowledgement;
- REST EOF, timeout, HTTP 429, and HTTP 5xx;
- WebSocket disconnect and reconnect;
- Exchange snapshots containing external orders and Fills.

It must not share the Order reducer with production code; otherwise the tests
would repeat the implementation instead of testing it.

- [ ] **Step 2: Cover SPOT MARKET end to end**

Test MARKET buy and sell with base quantity and no price:

```text
RPC -> validation -> persist intent -> Exchange submit
    -> Fill -> Order reducer -> Position -> ListOrders/ListFills
```

Verify symbol normalization, quantity/step rounding, fee persistence,
`FILLED` terminal state, and restart recovery from SQLite.

- [ ] **Step 3: Cover Binance and OKX SWAP semantics**

Test:

- opening long and short in NET mode;
- partial reduction with `reduce_only`;
- full close;
- long-to-short target transition closes before opening;
- cross-margin and leverage validation;
- Binance base quantity to contract quantity;
- OKX `ctVal` conversion in both directions;
- Exchange snapshot values for equity, margin, liquidation price, and PnL.

No test may depend on locally recomputing an Exchange liquidation price.

- [ ] **Step 4: Cover uncertainty and cancel races**

Test that EOF, timeout, 429, and 5xx leave the order in
`SUBMIT_UNCERTAIN`, then query by client order ID before any retry. Verify that
no path produces two Exchange orders for one client order ID.

Test delayed Fill after cancel:

```text
NEW -> CANCELED acknowledgement -> delayed partial Fill
    -> PARTIALLY_CANCELED with executed quantity and persisted Fill
```

Also test duplicate Fill idempotency and cumulative-quantity regression
rejection.

- [ ] **Step 5: Cover readiness and SyncAccount**

Test startup, reconnect, manual `SyncAccount`, and failed synchronization.
Submission must remain disabled until Account, Position, OpenOrder, and
RecentFill snapshots all succeed. An imported external order or Fill must be
persisted and returned by the public queries.

- [ ] **Step 6: Cover TargetIntent convergence through JetStream**

Use embedded NATS JetStream and real Strategy Outbox publication. Verify:

- command sequence monotonicity;
- `not_after` rejection;
- a newer target supersedes pending work;
- active-order awareness prevents duplicate child orders;
- reference price freshness and pre-trade deviation checks;
- post-Fill slippage recording;
- reconnect resumes toward the latest target rather than an old plan.

- [ ] **Step 7: Add an opt-in Exchange testnet smoke script**

The script must refuse to run unless:

```text
MOOX_TRADE_TESTNET=1
MOOX_TRADE_TESTNET_EXCHANGE=binance|okx
MOOX_TRADE_TESTNET_API_KEY
MOOX_TRADE_TESTNET_API_SECRET
MOOX_TRADE_TESTNET_SYMBOL
```

Require `MOOX_TRADE_TESTNET_PASSPHRASE` for OKX and
`MOOX_TRADE_TESTNET_SWAP_SYMBOL` for the SWAP case. Use the smallest valid
quantity, tag every order with a unique client order ID, and install a trap
that cancels open orders and closes any position created by the smoke test.

The smoke sequence is:

```text
sync account -> SPOT MARKET -> query Fill
-> set SWAP leverage -> SWAP MARKET open -> reduce-only close
-> sync account -> assert ready
```

- [ ] **Step 8: Run E2E and race suites**

```bash
cd modules/trade
go test -count=1 ./test
go test -race -count=1 ./...

cd ../..
bash scripts/test-strategy-trade-event-e2e.sh
```

Expected: PASS without real Exchange credentials. Run
`modules/trade/scripts/testnet-smoke.sh` only with explicit testnet
credentials; never point it at production keys.

- [ ] **Step 9: Commit the acceptance suite**

```bash
git add modules/trade/test modules/trade/scripts \
  modules/strategy/test/strategy_trade_external_e2e_test.go \
  scripts/test-strategy-trade-event-e2e.sh
git commit -m "test(trade): cover generic execution workflows"
```

### Task 16: Generate, Independently Review, Verify, Package, and Publish

**Files:**
- Modify: generated protobuf files under `modules/trade/proto/tradegen/`
- Modify: generated protobuf files under `packages/tradeeventpb/`
- Modify: any file required by a confirmed review finding

- [ ] **Step 1: Regenerate protobuf deterministically**

```bash
make proto
git diff -- modules/trade/proto/tradegen packages/tradeeventpb \
  > /tmp/moox-trade-proto.before
make proto
git diff -- modules/trade/proto/tradegen packages/tradeeventpb \
  > /tmp/moox-trade-proto.after
cmp /tmp/moox-trade-proto.before /tmp/moox-trade-proto.after
git status --short
```

Expected: the first run updates generated outputs when necessary; the second
run produces no additional diff. Review generated service names and JSON field
names instead of assuming generation succeeded correctly.

- [ ] **Step 2: Run focused verification**

```bash
cd modules/trade
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...

cd ../strategy
go test -count=1 ./...
go test -race -count=1 ./internal/store ./internal/outbox
python3 -m unittest discover -s pysdk/tests
python3 -m unittest pyworker/test_worker.py

cd ../admin
go test -count=1 ./internal/service/sysdeploy

cd ../../web
pnpm test
pnpm run lint:eslint:check
pnpm run build:prod
```

Expected: PASS.

- [ ] **Step 3: Run repository contract and workspace verification**

```bash
cd ..
bash scripts/test-trade-exchange-terminology.sh
bash scripts/verify-event-contracts.sh
bash scripts/check-package-boundaries.sh
bash scripts/test-strategy-trade-event-e2e.sh
bash scripts/test-go-workspace.sh
bash scripts/test-release-contract.sh
make verify-pr
git diff --check
```

Expected: PASS. `make verify-pr` may repeat earlier tests; do not skip it.

- [ ] **Step 4: Request an independent `codeCR` review**

Ask the configured `codeCR` subagent to review the final diff for:

- duplicate or missing order submission after uncertain POST results;
- cancel/Fill races and illegal terminal transitions;
- ExchangeSession serialization and reconnect behavior;
- quantity, contract-size, rounding, and `reduce_only` correctness;
- Strategy Outbox sequence/expiry semantics;
- secret leakage and Trade-local Exchange vocabulary;
- schema/protobuf/runtime registration drift;
- missing race, E2E, or restart coverage.

Require every finding to include file, symbol, and line evidence. Independently
verify each finding against the current diff, fix confirmed issues with a
failing regression test first, and rerun Steps 2 and 3. If no issues remain,
record the residual risk: real-Exchange behavior is only as strong as the
testnet coverage performed.

- [ ] **Step 5: Build the Trade artifacts and release contract**

```bash
bash scripts/build.sh trade
bash scripts/test-release-contract.sh
VERSION=trade-execution-verify SKIP_WEB_ASSETS=1 bash scripts/release.sh
tar -tzf release/moox-trade-execution-verify-$(go env GOOS)-$(go env GOARCH).tar.gz \
  | rg 'trade/(bin|config)'
```

Expected: `bin/moox-trade`, `bin/moox-trade-cli`, and the final Trade config
are present in the archive.

- [ ] **Step 6: Perform the opt-in live boundary checks**

When testnet credentials are available:

```bash
MOOX_TRADE_TESTNET=1 modules/trade/scripts/testnet-smoke.sh
```

For a configured local integration deployment:

```bash
scripts/deploy-moox.sh \
  --target localhost \
  --dir /tmp/moox-trade-execution-verify \
  --reset-data
```

Then call both tRPC services through the normal Gateway route and verify:

```text
CreateAccount -> SyncAccount -> GetAccount.ready=true
PlaceOrder(MARKET) -> GetOrder -> ListFills
SubmitTarget -> GetExecution -> ListPositions
```

Do not treat a process-only health response as execution acceptance. If
credentials are unavailable, record this as an unexecuted opt-in gate, not as
a passing test.

- [ ] **Step 7: Commit generated or review fixes**

```bash
git status --short
git diff --check
git add modules/trade modules/strategy modules/admin packages/tradeeventpb \
  web/src/api/trade web/src/views/trading \
  scripts/check-package-boundaries.sh scripts/verify-event-contracts.sh \
  scripts/test-strategy-trade-event-e2e.sh \
  docs/架构总览.md docs/策略模块架构设计.md
git diff --cached --name-status
git commit -m "refactor(trade): complete generic execution kernel"
```

Skip the commit when there are no remaining changes. Never stage unrelated
pre-existing files.

- [ ] **Step 8: Push and prove the exact remote revision**

```bash
git push origin feature/mooyang
local_sha="$(git rev-parse HEAD)"
remote_sha="$(git ls-remote --heads origin feature/mooyang | awk '{print $1}')"
test "${local_sha}" = "${remote_sha}"
git status --short
```

Expected: local and remote SHA match. The worktree may still show unrelated
pre-existing changes, but none may be part of the Trade commits.

## Completion Checklist

- [ ] `ExchangeAccountService` has only `CreateAccount`, `UpdateAccount`,
  `GetAccount`, `ListAccounts`, `SetLeverage`, `PauseAccount`, and
  `SyncAccount`.
- [ ] `TradeExecutionService` has only `PlaceOrder`, `CancelOrder`,
  `CancelAllOrders`, `SubmitTarget`, `GetExecution`, `ListExecutions`,
  `GetOrder`, `ListOrders`, `ListFills`, and `ListPositions`.
- [ ] Binance and OKX both support SPOT/SWAP MARKET and LIMIT within the
  approved V1 scope.
- [ ] Strategy publishes final base quantities; Trade does not consume
  weights, capital, or quote assets.
- [ ] One ExchangeSession serializes command, Fill, snapshot, reconnect, and
  `SyncAccount` transitions per ExchangeAccount.
- [ ] Uncertain submission is queried by client order ID before retry.
- [ ] A delayed Fill can refine a canceled Order into
  `PARTIALLY_CANCELED`.
- [ ] Startup and reconnect keep the account not ready until the full
  authoritative snapshot succeeds.
- [ ] Schema inventory contains exactly nine tables, uses `t_order_fills`,
  keeps the three-table double-entry ledger, and contains none of the six
  merged account, target, reservation, or event-state tables.
- [ ] Obsolete services, Rebalance Legs, Saga, transfer, dust, and duplicate
  Exchange abstractions are deleted rather than deprecated.
- [ ] Focused tests, race tests, E2E, workspace checks, Web build, protobuf
  regeneration, release packaging, and independent `codeCR` review pass.
- [ ] Testnet smoke and local deployment checks are either passed or explicitly
  recorded as opt-in gates not run because credentials/environment are absent.
- [ ] Remote `feature/mooyang` points to the verified local HEAD.
