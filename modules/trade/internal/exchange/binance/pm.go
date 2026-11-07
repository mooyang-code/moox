package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// pmStreamURL 是统一账户私有流的基址（官方 SDK 默认 wsURL 为 wss://fstream.binance.com/pm，流路径 /ws/<listenKey>）。
var pmStreamURL = "wss://fstream.binance.com/pm/ws/"

// pmHeartbeatTimeout 是统一账户私有流的心跳超时：沿用 U 本位合约流的 4 分钟（服务端约每 3 分钟 ping 一次）。
// 未核对：统一账户流的 ping 间隔官方文档页没有抓到。
const pmHeartbeatTimeout = 4 * time.Minute

// PortfolioMargin 是币安统一账户（组合保证金）的适配器。统一账户只有一把账户级的 papi：
// 一个余额接口、一个账户接口、一条私有流，现货（杠杆账户）和合约（UM）共用，所以它是整个账户一个适配器，
// 不能按市场拆成两个：拆开会重复订阅同一条流，任何一路关闭 listenKey 另一路也跟着断，余额还会重复计算。
// 下单、撤单、查单、挂单、成交仍按市场走各自的 papi 接口，复用单市场适配器的实现（papi 模式）。
type PortfolioMargin struct {
	config     exchange.AccountConfig
	credential exchange.Credential
	papi       *httpclient.Client
	// core 只用来发账户级的签名请求（余额、账户、持仓）。
	core    *Adapter
	markets map[exchange.MarketType]*Adapter
	order   []exchange.MarketType

	modeChecked atomic.Bool
	// borrowed 记着最近一次完整同步里每个资产的借款（含利息）：现货私有流事件只带 free、locked，
	// 合并进快照时借款要沿用，否则净额会被事件算成没有借款。
	borrowMu sync.Mutex
	borrowed map[string]shared.Decimal
}

// NewPortfolioMargin 创建统一账户适配器；config.Markets 是账户开通的全部市场。
func NewPortfolioMargin(config exchange.AccountConfig, credential exchange.Credential) *PortfolioMargin {
	papi := httpclient.New(productionPapiURL)
	papi.Classify = classifyRateLimit
	papi.SetWeightLimit(papiWeightLimit)
	return newPortfolioMargin(config, credential, papi)
}

func newPortfolioMargin(config exchange.AccountConfig, credential exchange.Credential, papi *httpclient.Client) *PortfolioMargin {
	pm := &PortfolioMargin{
		config: config, credential: credential, papi: papi,
		markets:  make(map[exchange.MarketType]*Adapter, len(config.Markets)),
		order:    append([]exchange.MarketType(nil), config.Markets...),
		borrowed: map[string]shared.Decimal{},
	}
	pm.core = New(config, credential)
	pm.core.papi = papi
	for _, market := range config.Markets {
		single := config
		single.Markets = []exchange.MarketType{market}
		sub := New(single, credential)
		sub.papi = papi
		pm.markets[market] = sub
	}
	return pm
}

func (p *PortfolioMargin) Exchange() exchange.Exchange { return exchange.ExchangeBinance }

func (p *PortfolioMargin) hasMarket(market exchange.MarketType) bool { return p.markets[market] != nil }

func (p *PortfolioMargin) route(market exchange.MarketType) (*Adapter, error) {
	if sub := p.markets[market]; sub != nil {
		return sub, nil
	}
	return nil, typedRejected(fmt.Sprintf("账户没有开通市场 %q", market), nil)
}

func (p *PortfolioMargin) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	var all []exchange.Instrument
	for _, market := range p.order {
		items, err := p.markets[market].LoadInstruments(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func (p *PortfolioMargin) GetQuote(ctx context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	sub, err := p.route(key.Market)
	if err != nil {
		return execution.MarketQuote{}, err
	}
	return sub.GetQuote(ctx, key)
}

func (p *PortfolioMargin) GetReferencePrice(ctx context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	sub, err := p.route(key.Market)
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	return sub.GetReferencePrice(ctx, key)
}

func (p *PortfolioMargin) GetOrderByExchangeID(ctx context.Context, key exchange.SymbolKey, exchangeOrderID string) (exchange.Order, error) {
	sub, err := p.route(key.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return sub.GetOrderByExchangeID(ctx, key, exchangeOrderID)
}

func (p *PortfolioMargin) GetOrder(ctx context.Context, key exchange.SymbolKey, clientOrderID string) (exchange.Order, error) {
	sub, err := p.route(key.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return sub.GetOrder(ctx, key, clientOrderID)
}

func (p *PortfolioMargin) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	sub, err := p.route(request.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return sub.PlaceOrder(ctx, request)
}

func (p *PortfolioMargin) CancelOrder(ctx context.Context, key exchange.SymbolKey, clientOrderID string) (exchange.Order, error) {
	sub, err := p.route(key.Market)
	if err != nil {
		return exchange.Order{}, err
	}
	return sub.CancelOrder(ctx, key, clientOrderID)
}

func (p *PortfolioMargin) ListRecentFills(ctx context.Context, key exchange.SymbolKey, cursor string) ([]exchange.Fill, string, error) {
	sub, err := p.route(key.Market)
	if err != nil {
		return nil, "", err
	}
	return sub.ListRecentFills(ctx, key, cursor)
}

func (p *PortfolioMargin) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	var all []exchange.Order
	for _, market := range p.order {
		items, err := p.markets[market].ListOpenOrders(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

// ListOpenOrdersForSymbols 是周期对账用的挂单查询：现货（杠杆账户）只查给定的标的，每个标的权重 5；
// 不传 symbol 的全量查询按“当前交易中的标的数量”计权重，每 30 秒对账一次会占掉共用 IP 的 papi 额度。
// 合约（UM）照常列全部挂单。没有出现在 symbols 里的现货标的上的外部挂单，要等会话启动或手动全量同步才发现。
func (p *PortfolioMargin) ListOpenOrdersForSymbols(ctx context.Context, symbols []exchange.SymbolKey) ([]exchange.Order, error) {
	var all []exchange.Order
	for _, market := range p.order {
		if market != exchange.MarketTypeSpot {
			items, err := p.markets[market].ListOpenOrders(ctx)
			if err != nil {
				return nil, err
			}
			all = append(all, items...)
			continue
		}
		seen := map[string]bool{}
		for _, key := range symbols {
			if key.Market != exchange.MarketTypeSpot || seen[key.Symbol] {
				continue
			}
			seen[key.Symbol] = true
			items, err := p.markets[market].ListOpenOrdersForSymbol(ctx, key)
			if exchange.IsInstrumentNotFound(err) {
				// 交易所确认标的不存在（已下架）：上面不可能有挂单，跳过它，不让一个下架标的让整次挂单查询失败。
				continue
			}
			if err != nil {
				return nil, err
			}
			all = append(all, items...)
		}
	}
	return all, nil
}

// SetLeverage、SetMarginMode 只有合约有；统一账户的合约一律全仓，设置保证金模式是空操作。
func (p *PortfolioMargin) SetLeverage(ctx context.Context, key exchange.SymbolKey, leverage shared.Decimal) error {
	sub, err := p.route(key.Market)
	if err != nil {
		return err
	}
	return sub.SetLeverage(ctx, key, leverage)
}

func (p *PortfolioMargin) SetMarginMode(ctx context.Context, key exchange.SymbolKey, mode exchange.MarginMode) error {
	sub, err := p.route(key.Market)
	if err != nil {
		return err
	}
	return sub.SetMarginMode(ctx, key, mode)
}

// RateStatus 是 papi 域名的限流状态。
func (p *PortfolioMargin) RateStatus() exchange.RateStatus {
	status := p.papi.Status()
	return exchange.RateStatus{
		UsedWeight: status.UsedWeight, WeightLimit: status.WeightLimit,
		PausedUntil: status.PausedUntil, PauseReason: status.PauseReason,
		OrdersPausedUntil: status.OrdersPausedUntil,
	}
}

// LedgerSupported：只有合约（UM）有 /papi/v1/um/income。
func (p *PortfolioMargin) LedgerSupported() bool { return p.hasMarket(exchange.MarketTypeSwap) }

func (p *PortfolioMargin) ListLedger(ctx context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	if !p.hasMarket(exchange.MarketTypeSwap) || (query.Market != exchange.MarketTypeUnspecified && query.Market != exchange.MarketTypeSwap) {
		return exchange.LedgerPage{}, typedRejected("该账户不支持这个市场的资金流水", nil)
	}
	page, err := p.markets[exchange.MarketTypeSwap].ListLedger(ctx, query)
	if err != nil {
		return exchange.LedgerPage{}, err
	}
	for i := range page.Entries {
		if page.Entries[i].Market == exchange.MarketTypeUnspecified && page.Entries[i].Type.MarketScoped() {
			page.Entries[i].Market = exchange.MarketTypeSwap
		}
	}
	return page, nil
}

// pmBalance 是 GET /papi/v1/balance 的一行（官方样例见 testdata/papi_balance.json）。
type pmBalance struct {
	Asset               string `json:"asset"`
	TotalWalletBalance  string `json:"totalWalletBalance"`
	CrossMarginAsset    string `json:"crossMarginAsset"`
	CrossMarginBorrowed string `json:"crossMarginBorrowed"`
	CrossMarginFree     string `json:"crossMarginFree"`
	CrossMarginInterest string `json:"crossMarginInterest"`
	CrossMarginLocked   string `json:"crossMarginLocked"`
	UMWalletBalance     string `json:"umWalletBalance"`
	UMUnrealizedPNL     string `json:"umUnrealizedPNL"`
	UpdateTime          int64  `json:"updateTime"`
}

// pmAccount 是 GET /papi/v1/account 的响应（官方样例见 testdata/papi_account.json）。
type pmAccount struct {
	UniMMR                string `json:"uniMMR"`
	AccountEquity         string `json:"accountEquity"`
	AccountInitialMargin  string `json:"accountInitialMargin"`
	AccountMaintMargin    string `json:"accountMaintMargin"`
	AccountStatus         string `json:"accountStatus"`
	TotalAvailableBalance string `json:"totalAvailableBalance"`
	UpdateTime            int64  `json:"updateTime"`
}

// GetAccountSnapshot 读取统一账户余额。结算资产的净额由两个钱包组成，各记一条：
//
//	MARGIN 钱包（杠杆账户）：Total = crossMarginAsset，Borrowed = crossMarginBorrowed + crossMarginInterest，
//	  净额 = Total − Borrowed（统一权益公式里“杠杆钱包资产 − 借款 − 利息”）；
//	UNIFIED 钱包（UM 合约）：Total = umWalletBalance，不含未实现盈亏。
//
// 未实现盈亏只取 UM 持仓接口的 unRealizedProfit 之和，不取余额行里的 umUnrealizedPNL。
// UM 钱包的余额不能直接给杠杆账户的现货买单用（需要先归集；未核对，实盘前 V3 实测确认：官方文档只有
// POST /papi/v1/asset-collection “从期货账户转到杠杆账户”，没有写明杠杆下单不能用 UM 钱包），所以它的 Available 记 0，
// 账户级可用资金放在快照的 AvailableFunds 里，合约保证金按它校验。
func (p *PortfolioMargin) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	if p.hasMarket(exchange.MarketTypeSwap) && !p.modeChecked.Load() {
		if err := p.ensureOneWayMode(ctx); err != nil {
			return exchange.AccountSnapshot{}, err
		}
		p.modeChecked.Store(true)
	}
	rawBalance, err := p.core.request(ctx, http.MethodGet, "/papi/v1/balance", nil)
	if err != nil {
		return exchange.AccountSnapshot{}, classifyAPIError(err, rawBalance)
	}
	var balances []pmBalance
	if err := json.Unmarshal(rawBalance, &balances); err != nil {
		return exchange.AccountSnapshot{}, typedRejected("decode portfolio margin balance", err)
	}
	rawAccount, err := p.core.request(ctx, http.MethodGet, "/papi/v1/account", nil)
	if err != nil {
		return exchange.AccountSnapshot{}, classifyAPIError(err, rawAccount)
	}
	var account pmAccount
	if err := json.Unmarshal(rawAccount, &account); err != nil {
		return exchange.AccountSnapshot{}, typedRejected("decode portfolio margin account", err)
	}
	snapshot := exchange.AccountSnapshot{
		Present: exchange.AccountSnapshotPresence{
			Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, MaintenanceMargin: true, UnrealizedPnL: true,
		},
	}
	if p.hasMarket(exchange.MarketTypeSwap) {
		positions, err := p.ListPositionSnapshots(ctx)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		snapshot.UnrealizedPnL = sumUnrealizedPnL(positions)
	}
	updatedAt := account.UpdateTime
	borrowed := make(map[string]shared.Decimal, len(balances))
	settlementFree, settlementUM := shared.Zero(), shared.Zero()
	for _, row := range balances {
		margin, err := pmMarginBalance(row)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		unified, err := decimalOrZero(row.UMWalletBalance)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		if row.UpdateTime > updatedAt {
			updatedAt = row.UpdateTime
		}
		if !margin.Total.IsZero() || !margin.Borrowed.IsZero() || !margin.Locked.IsZero() {
			snapshot.Balances = append(snapshot.Balances, margin)
			borrowed[row.Asset] = margin.Borrowed
		}
		if !unified.IsZero() {
			snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
				Wallet: exchange.WalletUnified, Asset: row.Asset, Total: unified,
			})
		}
		if row.Asset == p.config.SettlementAsset {
			settlementFree, settlementUM = margin.Available, unified
		}
	}
	if updatedAt <= 0 {
		updatedAt = time.Now().UTC().UnixMilli()
	}
	snapshot.ExchangeUpdatedAt = millis(updatedAt)
	snapshot.WalletUpdatedAt = map[exchange.Wallet]time.Time{
		exchange.WalletMargin: snapshot.ExchangeUpdatedAt, exchange.WalletUnified: snapshot.ExchangeUpdatedAt,
	}
	if snapshot.ExchangeEquity, err = decimalOrZero(account.AccountEquity); err != nil {
		return exchange.AccountSnapshot{}, err
	}
	snapshot.Equity = snapshot.ExchangeEquity
	if snapshot.UsedMargin, err = decimalOrZero(account.AccountInitialMargin); err != nil {
		return exchange.AccountSnapshot{}, err
	}
	if snapshot.MaintenanceMargin, err = decimalOrZero(account.AccountMaintMargin); err != nil {
		return exchange.AccountSnapshot{}, err
	}
	if snapshot.UniMMR, err = decimalOrZero(account.UniMMR); err != nil {
		return exchange.AccountSnapshot{}, err
	}
	// 账户级可用资金：优先用接口给的 totalAvailableBalance；官方样例里它是空串（未核对真实取值），
	// 取不到时用“杠杆钱包可用 + UM 钱包余额 − 账户已用初始保证金”估算，不低于 0。
	if account.TotalAvailableBalance != "" {
		snapshot.AvailableFunds, err = decimalOrZero(account.TotalAvailableBalance)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
	} else {
		snapshot.AvailableFunds = settlementFree.Add(settlementUM).Sub(snapshot.UsedMargin)
		if snapshot.AvailableFunds.Cmp(shared.Zero()) < 0 {
			snapshot.AvailableFunds = shared.Zero()
		}
	}
	p.borrowMu.Lock()
	p.borrowed = borrowed
	p.borrowMu.Unlock()
	return snapshot, nil
}

// pmMarginBalance 把余额行里杠杆账户的部分转成 MARGIN 钱包的余额：Total 是 crossMarginAsset（可用 + 冻结），
// 借款和利息合并记在 Borrowed。
func pmMarginBalance(row pmBalance) (exchange.AssetBalance, error) {
	var out exchange.AssetBalance
	var err error
	out.Wallet, out.Asset = exchange.WalletMargin, row.Asset
	if out.Total, err = decimalOrZero(row.CrossMarginAsset); err != nil {
		return out, err
	}
	if out.Available, err = decimalOrZero(row.CrossMarginFree); err != nil {
		return out, err
	}
	if out.Locked, err = decimalOrZero(row.CrossMarginLocked); err != nil {
		return out, err
	}
	loan, err := decimalOrZero(row.CrossMarginBorrowed)
	if err != nil {
		return out, err
	}
	interest, err := decimalOrZero(row.CrossMarginInterest)
	if err != nil {
		return out, err
	}
	out.Borrowed = loan.Add(interest)
	return out, nil
}

func (p *PortfolioMargin) ensureOneWayMode(ctx context.Context) error {
	raw, err := p.core.request(ctx, http.MethodGet, "/papi/v1/um/positionSide/dual", nil)
	if err != nil {
		return classifyAPIError(err, raw)
	}
	var payload struct {
		DualSidePosition bool `json:"dualSidePosition"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return typedRejected("decode position mode", err)
	}
	if payload.DualSidePosition {
		return typedRejected("hedge position mode is unsupported", nil)
	}
	return nil
}

// pmPosition 是 GET /papi/v1/um/positionRisk 的一行（官方样例见 testdata/papi_um_position_risk.json）。
type pmPosition struct {
	Symbol           string `json:"symbol"`
	PositionSide     string `json:"positionSide"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	Notional         string `json:"notional"`
	Leverage         string `json:"leverage"`
	LiquidationPrice string `json:"liquidationPrice"`
	UnRealizedProfit string `json:"unRealizedProfit"`
	UpdateTime       int64  `json:"updateTime"`
}

func (p *PortfolioMargin) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if !p.hasMarket(exchange.MarketTypeSwap) {
		return nil, nil
	}
	raw, err := p.core.request(ctx, http.MethodGet, "/papi/v1/um/positionRisk", nil)
	if err != nil {
		return nil, classifyAPIError(err, raw)
	}
	var rows []pmPosition
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, typedRejected("decode portfolio margin positions", err)
	}
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return nil, typedRejected("hedge position mode is unsupported", nil)
		}
		quantity, err := decimalOrZero(row.PositionAmt)
		if err != nil || quantity.IsZero() {
			continue
		}
		position := exchange.Position{
			Market: exchange.MarketTypeSwap, TradingAccountID: p.config.TradingAccountID,
			ExchangeSymbol: row.Symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millis(row.UpdateTime),
		}
		if position.EntryPrice, err = decimalOrZero(row.EntryPrice); err != nil {
			return nil, err
		}
		if position.MarkPrice, err = decimalOrZero(row.MarkPrice); err != nil {
			return nil, err
		}
		if position.LiquidationPrice, err = decimalOrZero(row.LiquidationPrice); err != nil {
			return nil, err
		}
		if position.UnrealizedPnL, err = decimalOrZero(row.UnRealizedProfit); err != nil {
			return nil, err
		}
		position.Leverage = shared.MustDecimal("1")
		if leverage, parseErr := decimalOrZero(row.Leverage); parseErr == nil && leverage.Cmp(shared.Zero()) > 0 {
			position.Leverage = leverage
		}
		if notional, parseErr := decimalOrZero(row.Notional); parseErr == nil {
			position.UsedMargin = notional.Abs().Div(position.Leverage)
		}
		out = append(out, position)
	}
	return out, nil
}

// Subscribe 订阅统一账户的私有流：一个 listenKey、一条连接，现货（杠杆账户）和合约（UM）事件都来自这条流。
func (p *PortfolioMargin) Subscribe(ctx context.Context, handler execution.AccountEventHandler) error {
	if handler == nil {
		return typedRejected("private event handler is required", nil)
	}
	const path = "/papi/v1/listenKey"
	headers := map[string]string{"X-MBX-APIKEY": p.credential.APIKey}
	raw, err := p.papi.Do(ctx, &httpclient.Request{Method: http.MethodPost, Path: path, Headers: headers})
	if err != nil {
		return fmt.Errorf("binance: create portfolio margin private stream: %w", classifyAPIError(err, raw))
	}
	var key listenKeyResponse
	if err := json.Unmarshal(raw, &key); err != nil || key.ListenKey == "" {
		return typedRejected("invalid private stream listen key", err)
	}
	connection, _, err := websocket.DefaultDialer.DialContext(
		ctx, pmStreamURL+key.ListenKey, http.Header{"Origin": []string{"https://moox.local"}},
	)
	if err != nil {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer connection.Close()

	done := make(chan struct{})
	defer close(done)
	go p.keepListenKeyAlive(ctx, path, connection, done)

	handler.OnSubscribed()
	return receiveStream(ctx, connection, handler, pmHeartbeatTimeout, p.dispatchPrivate)
}

// keepListenKeyAlive 每 30 分钟续期一次（统一账户的 listenKey 60 分钟过期，续期不带参数）。
func (p *PortfolioMargin) keepListenKeyAlive(ctx context.Context, path string, connection *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if !p.papi.Status().PausedUntil.IsZero() {
				ticker.Reset(time.Minute)
				continue
			}
			_, err := p.papi.Do(ctx, &httpclient.Request{
				Method: http.MethodPut, Path: path, Headers: map[string]string{"X-MBX-APIKEY": p.credential.APIKey},
			})
			if exchange.NotAccepted(err) || exchange.IsKind(err, exchange.ErrorTransportUnknown) {
				ticker.Reset(time.Minute)
				continue
			}
			if err != nil {
				_ = connection.Close()
				return
			}
			ticker.Reset(30 * time.Minute)
		}
	}
}

// dispatchPrivate 按事件类型分派统一账户私有流。事件里没有市场字段，靠类型区分：
//
//	executionReport、outboundAccountPosition → 杠杆账户（现货）；
//	ORDER_TRADE_UPDATE、ACCOUNT_UPDATE（fs 为 UM）→ U 本位合约。
//
// fs 是 CM（币本位）的事件不在 MooX 的交易范围，直接忽略。
func (p *PortfolioMargin) dispatchPrivate(ctx context.Context, payload []byte, handler execution.AccountEventHandler) error {
	root, err := exactFields(payload)
	if err != nil {
		return typedRejected("decode private event", err)
	}
	// listenKey 流直接是事件本身；兼容 WebSocket API 订阅把事件包在 event 里的写法。
	if _, wrapped := root.value("event"); wrapped {
		if root, err = root.object("event"); err != nil {
			return typedRejected("decode private event wrapper", err)
		}
	}
	event, err := root.str("e")
	if err != nil {
		return typedRejected("decode private event", err)
	}
	switch event {
	case "listenKeyExpired", "eventStreamTerminated":
		return errListenKeyExpired
	case "outboundAccountPosition":
		return p.dispatchMarginAccount(ctx, root, handler)
	case "ACCOUNT_UPDATE":
		if scope, _ := root.str("fs"); scope != "" && scope != "UM" {
			return nil
		}
		return p.dispatchUMAccount(ctx, root, handler)
	case "ORDER_TRADE_UPDATE":
		if scope, _ := root.str("fs"); scope != "" && scope != "UM" {
			return nil
		}
		body, err := root.object("o")
		if err != nil {
			return typedRejected("decode futures private order", err)
		}
		order, err := futuresPrivateOrder(body)
		if err != nil {
			return typedRejected("decode futures private order", err)
		}
		return p.dispatchOrder(ctx, exchange.MarketTypeSwap, order, handler)
	case "executionReport":
		order, err := spotPrivateOrder(root)
		if err != nil {
			return typedRejected("decode margin private order", err)
		}
		return p.dispatchOrder(ctx, exchange.MarketTypeSpot, order, handler)
	}
	return nil
}

func (p *PortfolioMargin) dispatchOrder(ctx context.Context, market exchange.MarketType, order privateOrder, handler execution.AccountEventHandler) error {
	current, err := orderFromRow(market, order.payload)
	if err != nil {
		return err
	}
	if err := handler.OnOrder(ctx, current); err != nil {
		return err
	}
	if order.execution != "TRADE" || order.tradeID == "" || order.tradeID == "-1" {
		return nil
	}
	fill, err := fillFromPrivateOrder(market, p.config.SettlementAsset, order)
	if err != nil {
		return err
	}
	return handler.OnFill(ctx, fill)
}

// dispatchMarginAccount 处理杠杆账户的余额推送（outboundAccountPosition，只带 free、locked）。
// 借款沿用最近一次完整同步的值；推送只覆盖部分资产，所以要求会话尽快做一次完整同步（RequiresSync）。
func (p *PortfolioMargin) dispatchMarginAccount(ctx context.Context, root fields, handler execution.AccountEventHandler) error {
	updatedAt, err := root.int64("u")
	if err != nil {
		return typedRejected("decode margin account event", err)
	}
	rows, err := root.objects("B")
	if err != nil {
		return typedRejected("decode margin account event", err)
	}
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millis(updatedAt),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
		RequiresSync:      true,
	}
	// 只在锁内取出借款表的引用（GetAccountSnapshot 整表替换、不原地修改），解锁后再组装快照、回调会话：
	// 会话的 OnAccountSnapshot 要拿 opMu，而同步线程持着 opMu 调 GetAccountSnapshot 时末尾要拿 borrowMu，
	// 持 borrowMu 回调会和同步线程互相等死。
	p.borrowMu.Lock()
	borrowed := p.borrowed
	p.borrowMu.Unlock()
	for _, row := range rows {
		var asset, free, locked string
		for key, target := range map[string]*string{"a": &asset, "f": &free, "l": &locked} {
			if *target, err = row.str(key); err != nil {
				return typedRejected("decode margin account event", err)
			}
		}
		available, err := decimalOrZero(free)
		if err != nil {
			return err
		}
		lockedValue, err := decimalOrZero(locked)
		if err != nil {
			return err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Wallet: exchange.WalletMargin, Asset: asset, Available: available, Locked: lockedValue,
			Total: available.Add(lockedValue), Borrowed: borrowed[asset],
		})
	}
	return handler.OnAccountSnapshot(ctx, snapshot)
}

// dispatchUMAccount 处理合约（UM）的 ACCOUNT_UPDATE：钱包余额记入 UNIFIED 钱包，持仓变化逐个上报。
// 统一账户的持仓行没有保证金模式字段（mt），统一账户的合约一律全仓。
func (p *PortfolioMargin) dispatchUMAccount(ctx context.Context, root fields, handler execution.AccountEventHandler) error {
	eventTime, err := root.int64("T")
	if err == nil && eventTime <= 0 {
		eventTime, err = root.int64("E")
	}
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	body, err := root.object("a")
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	balances, err := body.objects("B")
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	positions, err := body.objects("P")
	if err != nil {
		return typedRejected("decode futures account event", err)
	}
	snapshot := exchange.AccountSnapshot{
		ExchangeUpdatedAt: millis(eventTime),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
		RequiresSync:      true,
	}
	for _, row := range balances {
		asset, err := row.str("a")
		if err != nil {
			return typedRejected("decode futures account event", err)
		}
		wallet, err := row.str("wb")
		if err != nil {
			return typedRejected("decode futures account event", err)
		}
		total, err := decimalOrZero(wallet)
		if err != nil {
			return err
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{Wallet: exchange.WalletUnified, Asset: asset, Total: total})
	}
	if err := handler.OnAccountSnapshot(ctx, snapshot); err != nil {
		return err
	}
	for _, row := range positions {
		var symbol, amount, entry, unrealized, side string
		for key, target := range map[string]*string{"s": &symbol, "pa": &amount, "ep": &entry, "up": &unrealized, "ps": &side} {
			if *target, err = row.str(key); err != nil {
				return typedRejected("decode futures account event", err)
			}
		}
		if side != "" && side != "BOTH" {
			return typedRejected("hedge position mode is unsupported", nil)
		}
		quantity, err := decimalOrZero(amount)
		if err != nil {
			return err
		}
		position := exchange.Position{
			Market: exchange.MarketTypeSwap, TradingAccountID: p.config.TradingAccountID,
			ExchangeSymbol: symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross, ExchangeUpdatedAt: millis(eventTime),
		}
		if position.EntryPrice, err = decimalOrZero(entry); err != nil {
			return err
		}
		if position.UnrealizedPnL, err = decimalOrZero(unrealized); err != nil {
			return err
		}
		if err := handler.OnPosition(ctx, position); err != nil {
			return err
		}
	}
	return nil
}

// Supports：统一账户不划转（现货和合约保证金同属一个账户），BNB 抵扣和小额兑换可用。
func (p *PortfolioMargin) Supports(capability string) bool {
	switch capability {
	case execution.CapabilityFeeDiscount:
		return true
	case execution.CapabilityDust:
		return p.hasMarket(exchange.MarketTypeSpot)
	}
	return false
}

// SetBNBFeeDiscount 现货与利息抵扣走 /sapi/v1/bnbBurn，合约走 /papi/v1/um/feeBurn；账户开通了哪个市场就设哪部分。
func (p *PortfolioMargin) SetBNBFeeDiscount(ctx context.Context, enabled bool) error {
	for _, market := range p.order {
		if err := p.markets[market].SetBNBFeeDiscount(ctx, enabled); err != nil {
			return err
		}
	}
	return nil
}

// GetBNBFeeDiscount 报告账户开通的各部分抵扣是否全部开启。
func (p *PortfolioMargin) GetBNBFeeDiscount(ctx context.Context) (bool, error) {
	for _, market := range p.order {
		on, err := p.markets[market].GetBNBFeeDiscount(ctx)
		if err != nil || !on {
			return false, err
		}
	}
	return true, nil
}

// ListDust 列出杠杆账户里可以兑换成 BNB 的小额资产（accountType=MARGIN）。
func (p *PortfolioMargin) ListDust(ctx context.Context) ([]exchange.DustAsset, error) {
	if sub := p.markets[exchange.MarketTypeSpot]; sub != nil {
		return sub.ListDust(ctx)
	}
	return nil, nil
}

func (p *PortfolioMargin) ConvertDust(ctx context.Context, assets []string) (exchange.DustConversion, error) {
	if sub := p.markets[exchange.MarketTypeSpot]; sub != nil {
		return sub.ConvertDust(ctx, assets)
	}
	return exchange.DustConversion{}, nil
}
