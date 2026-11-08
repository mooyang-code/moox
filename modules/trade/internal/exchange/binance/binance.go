package binance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// sideEffectNone 是统一账户杠杆账户下单的 sideEffectType 取值：不借币、不自动还款（官方 SDK 枚举 NO_SIDE_EFFECT）。
const sideEffectNone = "NO_SIDE_EFFECT"

const (
	productionSpotURL = "https://api.binance.com"
	testnetSpotURL    = "https://testnet.binance.vision"
	productionSwapURL = "https://fapi.binance.com"
	testnetSwapURL    = "https://demo-fapi.binance.com"
	recvWindow        = 5000
	// 每分钟 IP 权重额度（exchangeInfo 实查）。
	spotWeightLimit = 6000
	swapWeightLimit = 2400
)

// 编译期确认币安适配器（普通账户与统一账户）实现了会话按类型断言使用的各个可选能力：
// 签名一旦漂移就编译失败，不会在运行时悄悄退化成“不支持”。
var (
	_ execution.ExecutionAdapter      = (*Adapter)(nil)
	_ execution.ExchangeIdentity      = (*Adapter)(nil)
	_ execution.SymbolConfigurer      = (*Adapter)(nil)
	_ execution.AccountEventSource    = (*Adapter)(nil)
	_ execution.LedgerSource          = (*Adapter)(nil)
	_ execution.CapabilityProbe       = (*Adapter)(nil)
	_ execution.WalletTransferer      = (*Adapter)(nil)
	_ execution.FeeDiscountConfigurer = (*Adapter)(nil)
	_ execution.DustConverter         = (*Adapter)(nil)
	_ execution.FundingRateSource     = (*Adapter)(nil)
	_ exchange.RateStatusSource       = (*Adapter)(nil)

	_ execution.ExecutionAdapter      = (*PortfolioMargin)(nil)
	_ execution.ExchangeIdentity      = (*PortfolioMargin)(nil)
	_ execution.SymbolConfigurer      = (*PortfolioMargin)(nil)
	_ execution.AccountEventSource    = (*PortfolioMargin)(nil)
	_ execution.LedgerSource          = (*PortfolioMargin)(nil)
	_ execution.ScopedOpenOrderSource = (*PortfolioMargin)(nil)
	_ execution.CapabilityProbe       = (*PortfolioMargin)(nil)
	_ execution.FeeDiscountConfigurer = (*PortfolioMargin)(nil)
	_ execution.DustConverter         = (*PortfolioMargin)(nil)
	_ exchange.RateStatusSource       = (*PortfolioMargin)(nil)
)

type Adapter struct {
	config     exchange.AccountConfig
	credential exchange.Credential
	spot       *httpclient.Client
	swap       *httpclient.Client
	// papi 非空表示统一账户（组合保证金）模式：公共行情仍走 spot、swap，下单、撤单、查单、挂单、成交走 papi，
	// 路径见 pmPaths；账户余额、持仓和私有流由 PortfolioMargin 在账户一级统一处理。
	papi *httpclient.Client

	mu          sync.RWMutex
	instruments map[exchange.SymbolKey]exchange.Instrument
	modeChecked atomic.Bool
	prices      exchange.PriceBoard
	books       exchange.BookBoard
}

func New(config exchange.AccountConfig, credential exchange.Credential) *Adapter {
	spotURL, swapURL := restEndpoints(config.Environment)
	spot, swap := httpclient.New(spotURL), httpclient.New(swapURL)
	spot.Classify, swap.Classify = classifyRateLimit, classifyRateLimit
	spot.SetWeightLimit(spotWeightLimit)
	swap.SetWeightLimit(swapWeightLimit)
	return &Adapter{
		config:      config,
		credential:  credential,
		spot:        spot,
		swap:        swap,
		instruments: make(map[exchange.SymbolKey]exchange.Instrument),
	}
}

// RateStatus 返回当前市场接口的限流状态。
func (a *Adapter) RateStatus() exchange.RateStatus {
	status := a.client().Status()
	return exchange.RateStatus{
		UsedWeight: status.UsedWeight, WeightLimit: status.WeightLimit,
		PausedUntil: status.PausedUntil, PauseReason: status.PauseReason,
		OrdersPausedUntil: status.OrdersPausedUntil,
	}
}

// classifyRateLimit 按响应体错误码细分限流：-1015 只限下单，-1008 是系统限流，-1003 是 IP 级。
func classifyRateLimit(status int, raw []byte) exchange.ErrorKind {
	if status == http.StatusTeapot {
		return exchange.ErrorBanned
	}
	switch apiCode(raw) {
	case "-1003":
		return exchange.ErrorRateLimited
	case "-1015":
		return exchange.ErrorOrderRateLimited
	case "-1008":
		return exchange.ErrorThrottled
	}
	return ""
}

func restEndpoints(
	environment exchange.AccountEnvironment,
) (spot string, swap string) {
	if environment == exchange.AccountEnvironmentTestnet {
		return testnetSpotURL, testnetSwapURL
	}
	return productionSpotURL, productionSwapURL
}

func privateStreamEndpoint(config exchange.AccountConfig) string {
	if config.Market() == exchange.MarketTypeSpot {
		if config.Environment == exchange.AccountEnvironmentTestnet {
			return "wss://ws-api.testnet.binance.vision/ws-api/v3"
		}
		return "wss://ws-api.binance.com:443/ws-api/v3"
	}
	if config.Environment == exchange.AccountEnvironmentTestnet {
		return "wss://demo-fstream.binance.com/ws/"
	}
	return "wss://fstream.binance.com/ws/"
}

func (a *Adapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }

// wallet 是余额所在的钱包：普通账户的现货在现货钱包，合约在合约钱包。
func (a *Adapter) wallet() exchange.Wallet {
	if a.config.Market() == exchange.MarketTypeSwap {
		return exchange.WalletFutures
	}
	return exchange.WalletSpot
}

// ownMarket 校验标的键属于本适配器服务的市场：传错市场直接报错，不会悄悄按本市场处理。
func (a *Adapter) ownMarket(key exchange.SymbolKey) error {
	if key.Market != a.config.Market() {
		return &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("标的键 %s 不属于本适配器服务的市场 %s", key, a.config.Market())}
	}
	return nil
}

// key 返回本适配器所服务市场下的标的键。
func (a *Adapter) key(symbol string) exchange.SymbolKey {
	return exchange.SymbolKey{Market: a.config.Market(), Symbol: symbol}
}

func (a *Adapter) client() *httpclient.Client {
	if a.config.Market() == exchange.MarketTypeSwap {
		return a.swap
	}
	return a.spot
}

func (a *Adapter) path(spot, swap string) string {
	path := spot
	if a.config.Market() == exchange.MarketTypeSwap {
		path = swap
	}
	if a.papi != nil {
		if mapped, ok := pmPaths[path]; ok {
			return mapped
		}
	}
	return path
}

// pmPaths 把普通账户的私有接口路径换成统一账户（papi）的对应路径；公共行情路径不在表里，保持不变。
// 统一账户现货下单走杠杆账户（margin）接口，合约走 um 接口；路径与参数按官方 SDK 核对，见 testdata/README.md。
var pmPaths = map[string]string{
	"/api/v3/order":       "/papi/v1/margin/order",
	"/api/v3/openOrders":  "/papi/v1/margin/openOrders",
	"/api/v3/myTrades":    "/papi/v1/margin/myTrades",
	"/fapi/v1/order":      "/papi/v1/um/order",
	"/fapi/v1/openOrders": "/papi/v1/um/openOrders",
	"/fapi/v1/userTrades": "/papi/v1/um/userTrades",
	"/fapi/v1/leverage":   "/papi/v1/um/leverage",
	"/fapi/v1/income":     "/papi/v1/um/income",
}

// privateClient 返回签名私有接口用的客户端：统一账户走 papi，普通账户按市场走现货或合约域名。
func (a *Adapter) privateClient() *httpclient.Client {
	if a.papi != nil {
		return a.papi
	}
	return a.client()
}

// signedQuery 返回签好名的原始查询串：签名追加在末尾，签名用的正是发送的这段字节。
func (a *Adapter) signedQuery(values url.Values) string {
	return signQuery(a.credential.APISecret, values)
}

// signQuery 给查询参数加上时间戳和接收窗口并签名；现货、合约、统一账户和检测共用。
func signQuery(secret string, values url.Values) string {
	query := cloneValues(values)
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.Itoa(recvWindow))
	encoded := query.Encode()
	return encoded + "&signature=" + hex.EncodeToString(hmacSha256(
		[]byte(secret),
		[]byte(encoded),
	))
}

func (a *Adapter) request(
	ctx context.Context,
	method string,
	path string,
	values url.Values,
) ([]byte, error) {
	return a.signedRequest(ctx, method, path, values, false)
}

// signedRequest 发签名请求；order 标记新下单，账户下单暂停时只拦这类请求。
func (a *Adapter) signedRequest(
	ctx context.Context,
	method string,
	path string,
	values url.Values,
	order bool,
) ([]byte, error) {
	return a.privateClient().Do(ctx, &httpclient.Request{
		Method:   method,
		Path:     path,
		RawQuery: a.signedQuery(values),
		Headers: map[string]string{
			"X-MBX-APIKEY": a.credential.APIKey,
		},
		Order: order,
	})
}

type exchangeInfo struct {
	Symbols []struct {
		Symbol string `json:"symbol"`
		// IsMarginTradingAllowed 只有现货的 exchangeInfo 有；统一账户的现货走杠杆账户下单，不允许杠杆交易的标的下不了单。
		IsMarginTradingAllowed *bool  `json:"isMarginTradingAllowed"`
		Pair                   string `json:"pair"`
		ContractType           string `json:"contractType"`
		Status                 string `json:"status"`
		BaseAsset              string `json:"baseAsset"`
		QuoteAsset             string `json:"quoteAsset"`
		MarginAsset            string `json:"marginAsset"`
		Filters                []struct {
			FilterType  string `json:"filterType"`
			TickSize    string `json:"tickSize"`
			StepSize    string `json:"stepSize"`
			MinQty      string `json:"minQty"`
			Notional    string `json:"notional"`
			MinNotional string `json:"minNotional"`
			// PERCENT_PRICE（合约、现货）与 PERCENT_PRICE_BY_SIDE（现货）的偏离倍数。
			MultiplierUp      string `json:"multiplierUp"`
			MultiplierDown    string `json:"multiplierDown"`
			BidMultiplierUp   string `json:"bidMultiplierUp"`
			AskMultiplierDown string `json:"askMultiplierDown"`
		} `json:"filters"`
	} `json:"symbols"`
}

func (a *Adapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	path := a.path("/api/v3/exchangeInfo", "/fapi/v1/exchangeInfo")
	raw, err := a.client().Do(ctx, &httpclient.Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return nil, err
	}
	var payload exchangeInfo
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode exchange info", err)
	}
	out := make([]exchange.Instrument, 0, len(payload.Symbols))
	for _, item := range payload.Symbols {
		if a.config.Market() == exchange.MarketTypeSwap {
			if item.ContractType != "PERPETUAL" || item.MarginAsset != "USDT" {
				continue
			}
		}
		// 统一账户的现货走杠杆账户（margin）下单：exchangeInfo 里明确不允许杠杆交易的标的不上架，避免下单被交易所拒绝。
		if a.papi != nil && a.config.Market() == exchange.MarketTypeSpot &&
			item.IsMarginTradingAllowed != nil && !*item.IsMarginTradingAllowed {
			continue
		}
		instrumentID, idErr := exchange.CanonicalInstrumentID(
			item.BaseAsset,
			item.QuoteAsset,
			a.config.Market(),
		)
		if idErr != nil {
			return nil, typedRejected("canonical instrument ID", idErr)
		}
		instrument := exchange.Instrument{
			Exchange:           exchange.ExchangeBinance,
			MarketType:         a.config.Market(),
			ExchangeSymbol:     item.Symbol,
			InstrumentID:       instrumentID,
			BaseAsset:          item.BaseAsset,
			QuoteAsset:         item.QuoteAsset,
			SettlementAsset:    item.QuoteAsset,
			Linear:             a.config.Market() == exchange.MarketTypeSwap,
			ContractValue:      shared.MustDecimal("1"),
			ContractValueAsset: item.BaseAsset,
			Status:             item.Status,
			ExchangeUpdatedAt:  time.Now().UTC(),
		}
		if a.config.Market() == exchange.MarketTypeSwap {
			instrument.SettlementAsset = item.MarginAsset
		}
		for _, filter := range item.Filters {
			switch filter.FilterType {
			case "PRICE_FILTER":
				instrument.PriceTick, err = decimalOrZero(filter.TickSize)
			case "LOT_SIZE", "MARKET_LOT_SIZE":
				// LOT_SIZE wins when both filters are present.
				if instrument.ExchangeQuantityStep.IsZero() || filter.FilterType == "LOT_SIZE" {
					instrument.ExchangeQuantityStep, err = decimalOrZero(filter.StepSize)
					if err == nil {
						instrument.MinExchangeQuantity, err = decimalOrZero(filter.MinQty)
					}
				}
			case "NOTIONAL", "MIN_NOTIONAL":
				rawNotional := filter.Notional
				if rawNotional == "" {
					rawNotional = filter.MinNotional
				}
				instrument.MinNotional, err = decimalOrZero(rawNotional)
			case "PERCENT_PRICE":
				err = tightenPriceBand(&instrument, filter.MultiplierUp, filter.MultiplierDown)
			case "PERCENT_PRICE_BY_SIDE":
				// 买单受 bid 一侧的上限约束，卖单受 ask 一侧的下限约束。
				err = tightenPriceBand(&instrument, filter.BidMultiplierUp, filter.AskMultiplierDown)
			}
			if err != nil {
				return nil, typedRejected("decode instrument decimal", err)
			}
		}
		if instrument.ExchangeQuantityStep.Cmp(shared.Zero()) <= 0 ||
			instrument.MinExchangeQuantity.Cmp(shared.Zero()) <= 0 ||
			instrument.PriceTick.Cmp(shared.Zero()) <= 0 {
			return nil, typedRejected("incomplete instrument filters", nil)
		}
		out = append(out, instrument)
	}
	a.mu.Lock()
	a.instruments = make(map[exchange.SymbolKey]exchange.Instrument, len(out))
	for _, instrument := range out {
		a.instruments[a.key(instrument.ExchangeSymbol)] = instrument
	}
	a.mu.Unlock()
	return out, nil
}

// tightenPriceBand 记下价格偏离限制；两个过滤器都有时取更严的一个。
func tightenPriceBand(instrument *exchange.Instrument, up, down string) error {
	upValue, err := decimalOrZero(up)
	if err != nil {
		return err
	}
	downValue, err := decimalOrZero(down)
	if err != nil {
		return err
	}
	if upValue.Cmp(shared.Zero()) > 0 && (instrument.MaxBuyPriceRatio.IsZero() || upValue.Cmp(instrument.MaxBuyPriceRatio) < 0) {
		instrument.MaxBuyPriceRatio = upValue
	}
	if downValue.Cmp(shared.Zero()) > 0 && downValue.Cmp(instrument.MinSellPriceRatio) > 0 {
		instrument.MinSellPriceRatio = downValue
	}
	return nil
}

func (a *Adapter) GetReferencePrice(
	ctx context.Context,
	key exchange.SymbolKey,
) (exchange.ReferencePrice, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.ReferencePrice{}, err
	}
	symbol := key.Symbol
	if price, at, ok := a.prices.Get(ctx, a.key(symbol), a.allPrices); ok {
		return exchange.ReferencePrice{Price: price, UpdatedAt: at}, nil
	}
	raw, err := a.client().Do(ctx, &httpclient.Request{
		Method: http.MethodGet,
		Path:   a.path("/api/v3/ticker/price", "/fapi/v1/ticker/price"),
		Query:  url.Values{"symbol": []string{symbol}},
	})
	if err != nil {
		// 带上错误码：标的已下架时是 -1121，估值据此识别（exchange.IsInstrumentNotFound）。
		return exchange.ReferencePrice{}, classifyAPIError(err, raw)
	}
	var payload struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.ReferencePrice{}, typedRejected("decode reference price", err)
	}
	price, err := shared.ParseDecimal(payload.Price)
	if err != nil || price.Cmp(shared.Zero()) <= 0 {
		return exchange.ReferencePrice{}, typedRejected("invalid reference price", err)
	}
	// 时效检查判断的是这次报价是否刚取到，用本地收到时间，不依赖交易所时钟和最后成交时间。
	return exchange.ReferencePrice{Price: price, UpdatedAt: time.Now().UTC()}, nil
}

func (a *Adapter) allPrices(ctx context.Context) (map[exchange.SymbolKey]shared.Decimal, error) {
	raw, err := a.client().Do(ctx, &httpclient.Request{
		Method: http.MethodGet,
		Path:   a.path("/api/v3/ticker/price", "/fapi/v1/ticker/price"),
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, typedRejected("decode all prices", err)
	}
	prices := make(map[exchange.SymbolKey]shared.Decimal, len(rows))
	for _, row := range rows {
		if price, err := shared.ParseDecimal(row.Price); err == nil {
			prices[a.key(row.Symbol)] = price
		}
	}
	return prices, nil
}

func (a *Adapter) GetQuote(ctx context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	if err := a.ownMarket(key); err != nil {
		return execution.MarketQuote{}, err
	}
	last, err := a.GetReferencePrice(ctx, key)
	if err != nil {
		return execution.MarketQuote{}, err
	}
	if book, at, ok := a.books.Get(ctx, key, a.allBooks); ok {
		return execution.MarketQuote{Bid: book.Bid, Ask: book.Ask, Last: last.Price, SourceTime: at}, nil
	}
	raw, err := a.client().Do(ctx, &httpclient.Request{Method: http.MethodGet, Path: a.path("/api/v3/ticker/bookTicker", "/fapi/v1/ticker/bookTicker"), Query: url.Values{"symbol": []string{key.Symbol}}})
	if err != nil {
		return execution.MarketQuote{}, err
	}
	var payload struct {
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return execution.MarketQuote{}, typedRejected("decode quote", err)
	}
	return execution.MarketQuote{Bid: parseQuote(payload.BidPrice), Ask: parseQuote(payload.AskPrice), Last: last.Price, SourceTime: time.Now().UTC()}, nil
}

// allBooks 一次取全市场买一卖一（永续权重 5、现货 4）。
func (a *Adapter) allBooks(ctx context.Context) (map[exchange.SymbolKey]exchange.Book, error) {
	raw, err := a.client().Do(ctx, &httpclient.Request{
		Method: http.MethodGet,
		Path:   a.path("/api/v3/ticker/bookTicker", "/fapi/v1/ticker/bookTicker"),
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Symbol   string `json:"symbol"`
		BidPrice string `json:"bidPrice"`
		AskPrice string `json:"askPrice"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, typedRejected("decode all books", err)
	}
	books := make(map[exchange.SymbolKey]exchange.Book, len(rows))
	for _, row := range rows {
		books[a.key(row.Symbol)] = exchange.Book{Bid: parseQuote(row.BidPrice), Ask: parseQuote(row.AskPrice)}
	}
	return books, nil
}

func parseQuote(raw string) shared.Decimal {
	v, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return v
}

type spotAccount struct {
	UpdateTime int64 `json:"updateTime"`
	Balances   []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	} `json:"balances"`
}

type swapAccount struct {
	TotalWalletBalance    string `json:"totalWalletBalance"`
	TotalMarginBalance    string `json:"totalMarginBalance"`
	AvailableBalance      string `json:"availableBalance"`
	TotalInitialMargin    string `json:"totalInitialMargin"`
	TotalMaintMargin      string `json:"totalMaintMargin"`
	TotalUnrealizedProfit string `json:"totalUnrealizedProfit"`
	Assets                []struct {
		Asset             string `json:"asset"`
		WalletBalance     string `json:"walletBalance"`
		AvailableBalance  string `json:"availableBalance"`
		MaxWithdrawAmount string `json:"maxWithdrawAmount"`
		InitialMargin     string `json:"initialMargin"`
		UnrealizedProfit  string `json:"unrealizedProfit"`
		UpdateTime        int64  `json:"updateTime"`
	} `json:"assets"`
}

func (a *Adapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	// 持仓模式只在会话启动时检查一次，适配器随会话重建。
	if a.config.Market() == exchange.MarketTypeSwap && !a.modeChecked.Load() {
		// 普通账户允许多资产模式（保证金币需要），不再拒绝；模式写在检测结果里。
		if err := a.ensureOneWayMode(ctx); err != nil {
			return exchange.AccountSnapshot{}, err
		}
		a.modeChecked.Store(true)
	}
	path := a.path("/api/v3/account", "/fapi/v3/account")
	raw, err := a.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	if a.config.Market() == exchange.MarketTypeSpot {
		var payload spotAccount
		if err := json.Unmarshal(raw, &payload); err != nil {
			return exchange.AccountSnapshot{}, typedRejected("decode account", err)
		}
		snapshotTime := payload.UpdateTime
		if snapshotTime <= 0 {
			snapshotTime = time.Now().UTC().UnixMilli()
		}
		snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millis(snapshotTime)}
		for _, balance := range payload.Balances {
			available, err := decimalOrZero(balance.Free)
			if err != nil {
				return exchange.AccountSnapshot{}, err
			}
			locked, err := decimalOrZero(balance.Locked)
			if err != nil {
				return exchange.AccountSnapshot{}, err
			}
			snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
				Wallet: a.wallet(),
				Asset:  balance.Asset, Available: available, Locked: locked, Total: available.Add(locked),
			})
		}
		return snapshot, nil
	}
	var payload swapAccount
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.AccountSnapshot{}, typedRejected("decode futures account", err)
	}
	snapshotTime := int64(0)
	for _, asset := range payload.Assets {
		if asset.UpdateTime > snapshotTime {
			snapshotTime = asset.UpdateTime
		}
	}
	if snapshotTime == 0 {
		// `/fapi/v3/account` has no top-level updateTime and may return no
		// timestamp for an empty account. In that case the successful fetch
		// completion is the authoritative observation boundary.
		snapshotTime = time.Now().UTC().UnixMilli()
	}
	snapshot := exchange.AccountSnapshot{ExchangeUpdatedAt: millis(snapshotTime)}
	snapshot.Equity, err = decimalOrZero(payload.TotalMarginBalance)
	snapshot.ExchangeEquity = snapshot.Equity
	if err == nil {
		snapshot.AvailableFunds, err = decimalOrZero(payload.AvailableBalance)
	}
	if err == nil {
		snapshot.UsedMargin, err = decimalOrZero(payload.TotalInitialMargin)
	}
	if err == nil {
		snapshot.MaintenanceMargin, err = decimalOrZero(payload.TotalMaintMargin)
	}
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	// 未实现盈亏只从持仓接口取一次：账户接口里的 totalUnrealizedProfit 不用，walletBalance 本身不含未实现盈亏，
	// 这样统一权益公式（结算资产净额 + 永续未实现盈亏 + …）不会重复计入。
	positions, err := a.ListPositionSnapshots(ctx)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	snapshot.UnrealizedPnL = sumUnrealizedPnL(positions)
	for _, balance := range payload.Assets {
		total, parseErr := decimalOrZero(balance.WalletBalance)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		available, parseErr := decimalOrZero(balance.AvailableBalance)
		if parseErr != nil {
			return exchange.AccountSnapshot{}, parseErr
		}
		// 可划转额度取 maxWithdrawAmount（官方：Maximum transferable/withdrawable amount），不是 availableBalance；
		// 字段缺失时才退回可用余额。
		transferable := available
		if balance.MaxWithdrawAmount != "" {
			if transferable, parseErr = decimalOrZero(balance.MaxWithdrawAmount); parseErr != nil {
				return exchange.AccountSnapshot{}, parseErr
			}
		}
		snapshot.Balances = append(snapshot.Balances, exchange.AssetBalance{
			Wallet: a.wallet(),
			Asset:  balance.Asset, Available: available, Locked: total.Sub(available), Total: total, Transferable: transferable,
		})
	}
	return snapshot, nil
}

// sumUnrealizedPnL 汇总持仓的未实现盈亏。
func sumUnrealizedPnL(positions []exchange.Position) shared.Decimal {
	total := shared.Zero()
	for _, position := range positions {
		total = total.Add(position.UnrealizedPnL)
	}
	return total
}

func (a *Adapter) ensureOneWayMode(ctx context.Context) error {
	raw, err := a.request(ctx, http.MethodGet, "/fapi/v1/positionSide/dual", nil)
	if err != nil {
		return err
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

// positionRisk 是 /fapi/v3/positionRisk 的响应，v3 不再返回 marginType 和 leverage。
type positionRisk struct {
	Symbol                string `json:"symbol"`
	PositionSide          string `json:"positionSide"`
	PositionAmt           string `json:"positionAmt"`
	EntryPrice            string `json:"entryPrice"`
	MarkPrice             string `json:"markPrice"`
	Notional              string `json:"notional"`
	IsolatedWallet        string `json:"isolatedWallet"`
	InitialMargin         string `json:"initialMargin"`
	PositionInitialMargin string `json:"positionInitialMargin"`
	LiquidationPrice      string `json:"liquidationPrice"`
	UnRealizedProfit      string `json:"unRealizedProfit"`
	UpdateTime            int64  `json:"updateTime"`
}

func (a *Adapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if a.config.Market() == exchange.MarketTypeSpot {
		return nil, nil
	}
	raw, err := a.request(ctx, http.MethodGet, "/fapi/v3/positionRisk", nil)
	if err != nil {
		return nil, err
	}
	var payload []positionRisk
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode positions", err)
	}
	out := make([]exchange.Position, 0, len(payload))
	for _, row := range payload {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return nil, typedRejected("hedge position mode is unsupported", nil)
		}
		quantity, err := decimalOrZero(row.PositionAmt)
		if err != nil || quantity.IsZero() {
			continue
		}
		position := exchange.Position{
			Market:           a.config.Market(),
			TradingAccountID: a.config.TradingAccountID,
			ExchangeSymbol:   row.Symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: quantity, MarginMode: exchange.MarginModeCross,
			ExchangeUpdatedAt: millis(row.UpdateTime),
		}
		position.EntryPrice, err = decimalOrZero(row.EntryPrice)
		if err == nil {
			position.MarkPrice, err = decimalOrZero(row.MarkPrice)
		}
		if err == nil {
			position.LiquidationPrice, err = decimalOrZero(row.LiquidationPrice)
		}
		if err == nil {
			position.UnrealizedPnL, err = decimalOrZero(row.UnRealizedProfit)
		}
		if err != nil {
			return nil, err
		}
		isolatedWallet, err := decimalOrZero(row.IsolatedWallet)
		if err != nil {
			return nil, err
		}
		if !isolatedWallet.IsZero() {
			return nil, typedRejected("isolated margin is unsupported", nil)
		}
		position.UsedMargin, err = positionMargin(row)
		if err != nil {
			return nil, err
		}
		position.Leverage, err = positionLeverage(row.Notional, position.UsedMargin)
		if err != nil {
			return nil, err
		}
		out = append(out, position)
	}
	return out, nil
}

func positionMargin(row positionRisk) (shared.Decimal, error) {
	margin, err := decimalOrZero(row.PositionInitialMargin)
	if err != nil || !margin.IsZero() {
		return margin, err
	}
	return decimalOrZero(row.InitialMargin)
}

// positionLeverage 由名义价值和保证金反推杠杆，只用于展示和估算；取不到时按 1 倍。
func positionLeverage(rawNotional string, margin shared.Decimal) (shared.Decimal, error) {
	notional, err := decimalOrZero(rawNotional)
	if err != nil {
		return shared.Decimal{}, err
	}
	one := shared.MustDecimal("1")
	if notional.IsZero() || margin.Cmp(shared.Zero()) <= 0 {
		return one, nil
	}
	leverage := notional.Abs().Div(margin).Add(shared.MustDecimal("0.5")).RoundDown(0)
	if leverage.Cmp(one) < 0 {
		return one, nil
	}
	return leverage, nil
}

type orderPayload struct {
	OrderID            json.Number `json:"orderId"`
	ClientOrderID      string      `json:"clientOrderId"`
	Symbol             string      `json:"symbol"`
	Type               string      `json:"type"`
	TimeInForce        string      `json:"timeInForce"`
	Side               string      `json:"side"`
	PositionSide       string      `json:"positionSide"`
	OrigQty            string      `json:"origQty"`
	ExecutedQty        string      `json:"executedQty"`
	AvgPrice           string      `json:"avgPrice"`
	CumulativeQuoteQty string      `json:"cummulativeQuoteQty"`
	Price              string      `json:"price"`
	ReduceOnly         bool        `json:"reduceOnly"`
	Status             string      `json:"status"`
	Time               int64       `json:"time"`
	UpdateTime         int64       `json:"updateTime"`
	// TransactTime 是现货下单回执里唯一的时间字段（回执没有 time、updateTime）。
	TransactTime int64 `json:"transactTime"`
}

// ListOpenOrders 列出本市场全部挂单。统一账户现货走 /papi/v1/margin/openOrders 且不传 symbol：
// 官方文档（SDK 里转录的原文）写明 “If symbol is not sent, order records for all symbols are returned”，
// 同时说明不传时限频按“当前交易中的标的数量”计权重（传 symbol 时权重 5）。SDK 的参数校验把 symbol 标成必填，
// 与文档说明不一致，这里以文档说明为准；真实行为未实测，V3 小额实测要核对这个接口，必填的话改为按相关标的逐个查询。
// 不传 symbol 的全量查询只在会话启动和手动全量同步时用；周期对账走 PortfolioMargin.ListOpenOrdersForSymbols，按标的逐个查。
func (a *Adapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	return a.listOpenOrders(ctx, nil)
}

// ListOpenOrdersForSymbol 列出本市场某个标的的挂单（传 symbol，统一账户现货权重 5）。
func (a *Adapter) ListOpenOrdersForSymbol(ctx context.Context, key exchange.SymbolKey) ([]exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return nil, err
	}
	if strings.TrimSpace(key.Symbol) == "" {
		return nil, typedRejected("symbol is required", nil)
	}
	return a.listOpenOrders(ctx, url.Values{"symbol": []string{key.Symbol}})
}

func (a *Adapter) listOpenOrders(ctx context.Context, values url.Values) ([]exchange.Order, error) {
	raw, err := a.request(
		ctx,
		http.MethodGet,
		a.path("/api/v3/openOrders", "/fapi/v1/openOrders"),
		values,
	)
	if err != nil {
		// 带上错误码：按标的查时标的已下架是 -1121（exchange.IsInstrumentNotFound），同步据此跳过这个标的。
		return nil, classifyAPIError(err, raw)
	}
	var payload []orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode open orders", err)
	}
	return a.ordersFromPayload(payload)
}

func (a *Adapter) GetOrder(
	ctx context.Context,
	key exchange.SymbolKey,
	clientOrderID string,
) (exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.Order{}, err
	}
	symbolValue := key.Symbol
	if strings.TrimSpace(symbolValue) == "" || strings.TrimSpace(clientOrderID) == "" {
		return exchange.Order{}, typedRejected("symbol and client order id are required", nil)
	}
	raw, err := a.request(ctx, http.MethodGet, a.path("/api/v3/order", "/fapi/v1/order"), url.Values{
		"symbol":            []string{symbolValue},
		"origClientOrderId": []string{clientOrderID},
	})
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, typedRejected("decode order", err)
	}
	return a.orderFromPayload(payload)
}

func (a *Adapter) GetOrderByExchangeID(
	ctx context.Context,
	key exchange.SymbolKey,
	exchangeOrderID string,
) (exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.Order{}, err
	}
	symbol := key.Symbol
	if strings.TrimSpace(symbol) == "" || strings.TrimSpace(exchangeOrderID) == "" {
		return exchange.Order{}, typedRejected("symbol and Exchange order id are required", nil)
	}
	raw, err := a.request(
		ctx,
		http.MethodGet,
		a.path("/api/v3/order", "/fapi/v1/order"),
		url.Values{
			"symbol":  []string{symbol},
			"orderId": []string{exchangeOrderID},
		},
	)
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, typedRejected("decode order", err)
	}
	return a.orderFromPayload(payload)
}

func (a *Adapter) PlaceOrder(
	ctx context.Context,
	request exchange.OrderRequest,
) (exchange.Order, error) {
	if err := validateOrderRequest(request, a.config.Market()); err != nil {
		return exchange.Order{}, err
	}
	values := url.Values{
		"symbol":           []string{request.ExchangeSymbol},
		"newClientOrderId": []string{request.ClientOrderID},
		"side":             []string{string(request.Side)},
		"type":             []string{string(request.OrderType)},
		"quantity":         []string{request.Quantity.String()},
		"newOrderRespType": []string{"RESULT"},
	}
	if request.OrderType == exchange.OrderTypeLimit {
		values.Set("timeInForce", string(request.EffectiveFillPolicy()))
		values.Set("price", request.LimitPrice.String())
	}
	if a.config.Market() == exchange.MarketTypeSwap {
		values.Set("positionSide", "BOTH")
		if request.ReduceOnly {
			values.Set("reduceOnly", "true")
		}
	} else if a.papi != nil {
		// 统一账户的现货下单走杠杆账户：必须显式声明不借币、不自动还款。
		// 官方默认值也是 NO_SIDE_EFFECT，但 MooX 不依赖默认值：任何一次现货下单请求都显式带上它。
		values.Set("sideEffectType", sideEffectNone)
	}
	raw, err := a.signedRequest(
		ctx,
		http.MethodPost,
		a.path("/api/v3/order", "/fapi/v1/order"),
		values,
		true,
	)
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, transportUnknown("decode placed order", err)
	}
	order, err := a.orderFromPayload(payload)
	if err != nil {
		// HTTP 200 说明交易所已经受理：回执字段解析不了不能当确定拒单，按结果不明走查询恢复。
		return exchange.Order{}, transportUnknown("decode placed order fields", err)
	}
	// Some Binance acknowledgements omit these echoed fields.
	order.ClientOrderID = request.ClientOrderID
	order.ExchangeSymbol = request.ExchangeSymbol
	order.OrderType = request.OrderType
	order.TimeInForce = request.NativeTimeInForce()
	order.Side = request.Side
	order.PositionSide = request.PositionSide
	order.Quantity = request.Quantity
	order.ReduceOnly = request.ReduceOnly
	return order, nil
}

func (a *Adapter) CancelOrder(
	ctx context.Context,
	key exchange.SymbolKey,
	clientOrderID string,
) (exchange.Order, error) {
	if err := a.ownMarket(key); err != nil {
		return exchange.Order{}, err
	}
	symbolValue := key.Symbol
	raw, err := a.request(ctx, http.MethodDelete, a.path("/api/v3/order", "/fapi/v1/order"), url.Values{
		"symbol":            []string{symbolValue},
		"origClientOrderId": []string{clientOrderID},
	})
	if err != nil {
		return exchange.Order{}, classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.Order{}, transportUnknown("decode canceled order", err)
	}
	// 现货撤单回执的 clientOrderId 是撤单请求自己的单号（原单号在 origClientOrderId），统一用被撤订单的单号。
	payload.ClientOrderID = clientOrderID
	order, err := a.orderFromPayload(payload)
	if err != nil {
		// 撤单已被受理（HTTP 200），回执字段解析不了按结果不明处理，由查询确认订单状态。
		return exchange.Order{}, transportUnknown("decode canceled order fields", err)
	}
	return order, nil
}

type tradePayload struct {
	ID              json.Number `json:"id"`
	OrderID         json.Number `json:"orderId"`
	Symbol          string      `json:"symbol"`
	Price           string      `json:"price"`
	Qty             string      `json:"qty"`
	Commission      string      `json:"commission"`
	CommissionAsset string      `json:"commissionAsset"`
	RealizedPnl     string      `json:"realizedPnl"`
	Time            int64       `json:"time"`
	// 现货 myTrades 用 isBuyer/isMaker；U 本位 userTrades 方向取 side，挂单取 maker。
	IsBuyer      bool   `json:"isBuyer"`
	IsMaker      bool   `json:"isMaker"`
	Maker        bool   `json:"maker"`
	Side         string `json:"side"`
	PositionSide string `json:"positionSide"`
}

func (a *Adapter) ListRecentFills(
	ctx context.Context,
	key exchange.SymbolKey,
	cursor string,
) ([]exchange.Fill, string, error) {
	if err := a.ownMarket(key); err != nil {
		return nil, "", err
	}
	symbolValue := key.Symbol
	if strings.TrimSpace(symbolValue) == "" {
		return nil, cursor, typedRejected("symbol is required to list Binance fills", nil)
	}
	const pageSize = 1000
	values := url.Values{
		"symbol": []string{symbolValue},
		"limit":  []string{strconv.Itoa(pageSize)},
	}
	catchUp := true
	if cursor != "" {
		tradeID, err := strconv.ParseUint(cursor, 10, 64)
		if err != nil || tradeID == ^uint64(0) {
			return nil, cursor, typedRejected("invalid Binance fill cursor", err)
		}
		// Binance fromId is inclusive. Advance past the last applied trade so
		// an idle account does not return the same Fill on every sync.
		values.Set("fromId", strconv.FormatUint(tradeID+1, 10))
	} else {
		// 第一次拉这个标的时从头取历史。账户创建前的成交由 accountsync 过滤，只推进游标。
		values.Set("fromId", "0")
	}
	out := make([]exchange.Fill, 0)
	next := cursor
	clientOrderIDs := make(map[string]string)
	for {
		raw, err := a.request(
			ctx,
			http.MethodGet,
			a.path("/api/v3/myTrades", "/fapi/v1/userTrades"),
			values,
		)
		if err != nil {
			// 带上错误码：标的已下架时是 -1121（exchange.IsInstrumentNotFound），同步据此跳过这个标的。
			return nil, cursor, classifyAPIError(err, raw)
		}
		var payload []tradePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, cursor, typedRejected("decode fills", err)
		}
		for _, row := range payload {
			fill, err := a.fillFromPayload(row)
			if err != nil {
				return nil, cursor, err
			}
			orderID := row.OrderID.String()
			// 账户创建前的成交不入账，不查订单号，历史长的账户首次拉取才不会撞限频。
			if fill.TradedAt.Before(a.config.CreatedAt) {
				out = append(out, fill)
				if row.ID.String() != "" {
					next = row.ID.String()
				}
				continue
			}
			clientOrderID, found := clientOrderIDs[orderID]
			if !found {
				clientOrderID, err = a.clientOrderIDByExchangeOrderID(ctx, symbolValue, orderID)
				if exchange.IsKind(err, exchange.ErrorOrderNotFound) {
					clientOrderID = ""
				} else if err != nil {
					return nil, cursor, err
				}
				clientOrderIDs[orderID] = clientOrderID
			}
			fill.ClientOrderID = clientOrderID
			out = append(out, fill)
			if row.ID.String() != "" {
				next = row.ID.String()
			}
		}
		if !catchUp || len(payload) < pageSize {
			break
		}
		lastID, err := strconv.ParseUint(next, 10, 64)
		if err != nil || lastID == ^uint64(0) {
			return nil, cursor, typedRejected("invalid Binance fill page cursor", err)
		}
		fromID := strconv.FormatUint(lastID+1, 10)
		if values.Get("fromId") == fromID {
			return nil, cursor, typedRejected("Binance fill page did not advance", nil)
		}
		values.Set("fromId", fromID)
	}
	return out, next, nil
}

func (a *Adapter) clientOrderIDByExchangeOrderID(
	ctx context.Context,
	symbol string,
	exchangeOrderID string,
) (string, error) {
	if exchangeOrderID == "" {
		return "", typedRejected("Binance Fill is missing order id", nil)
	}
	raw, err := a.request(
		ctx,
		http.MethodGet,
		a.path("/api/v3/order", "/fapi/v1/order"),
		url.Values{"symbol": []string{symbol}, "orderId": []string{exchangeOrderID}},
	)
	if err != nil {
		return "", classifyAPIError(err, raw)
	}
	var payload orderPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", typedRejected("decode Fill order", err)
	}
	if strings.TrimSpace(payload.ClientOrderID) == "" {
		return "", typedRejected("Binance Fill order is missing client order id", nil)
	}
	return payload.ClientOrderID, nil
}

func (a *Adapter) SetLeverage(
	ctx context.Context,
	key exchange.SymbolKey,
	leverage shared.Decimal,
) error {
	if err := a.ownMarket(key); err != nil {
		return err
	}
	symbolValue := key.Symbol
	if a.config.Market() != exchange.MarketTypeSwap ||
		strings.TrimSpace(symbolValue) == "" ||
		leverage.Cmp(shared.Zero()) <= 0 ||
		strings.ContainsAny(leverage.String(), "./") {
		return typedRejected("positive integer SWAP leverage is required", nil)
	}
	raw, err := a.request(ctx, http.MethodPost, a.path("", "/fapi/v1/leverage"), url.Values{
		"symbol":   []string{symbolValue},
		"leverage": []string{leverage.String()},
	})
	return classifyAPIError(err, raw)
}

func (a *Adapter) SetMarginMode(
	ctx context.Context,
	key exchange.SymbolKey,
	mode exchange.MarginMode,
) error {
	if err := a.ownMarket(key); err != nil {
		return err
	}
	symbolValue := key.Symbol
	if a.config.Market() != exchange.MarketTypeSwap ||
		mode != exchange.MarginModeCross ||
		strings.TrimSpace(symbolValue) == "" {
		return typedRejected("only CROSS SWAP margin mode is supported", nil)
	}
	if a.papi != nil {
		// 统一账户的合约一律全仓，没有切换保证金模式的接口。
		return nil
	}
	raw, err := a.request(ctx, http.MethodPost, "/fapi/v1/marginType", url.Values{
		"symbol":     []string{symbolValue},
		"marginType": []string{"CROSSED"},
	})
	if err != nil && apiCode(raw) == "-4046" {
		return nil
	}
	return classifyAPIError(err, raw)
}

func (a *Adapter) ordersFromPayload(payload []orderPayload) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(payload))
	for _, row := range payload {
		order, err := a.orderFromPayload(row)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, nil
}

func (a *Adapter) orderFromPayload(row orderPayload) (exchange.Order, error) {
	return orderFromRow(a.config.Market(), row)
}

// orderFromRow 把订单响应转成订单；市场由调用方指定，普通账户和统一账户共用。
func orderFromRow(market exchange.MarketType, row orderPayload) (exchange.Order, error) {
	quantity, err := decimalOrZero(row.OrigQty)
	if err != nil {
		return exchange.Order{}, err
	}
	filled, err := decimalOrZero(row.ExecutedQty)
	if err != nil {
		return exchange.Order{}, err
	}
	average, err := decimalOrZero(row.AvgPrice)
	if err != nil {
		return exchange.Order{}, err
	}
	if average.IsZero() && !filled.IsZero() {
		cumulativeQuote, parseErr := decimalOrZero(row.CumulativeQuoteQty)
		if parseErr != nil {
			return exchange.Order{}, parseErr
		}
		if cumulativeQuote.Cmp(shared.Zero()) > 0 {
			average = cumulativeQuote.Div(filled)
		}
	}
	price, err := decimalOrZero(row.Price)
	if err != nil {
		return exchange.Order{}, err
	}
	var limitPrice *shared.Decimal
	if exchange.OrderType(row.Type) == exchange.OrderTypeLimit &&
		price.Cmp(shared.Zero()) > 0 {
		limitPrice = &price
	}
	positionSide := exchange.PositionSideUnspecified
	if market == exchange.MarketTypeSwap {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return exchange.Order{}, typedRejected("hedge order is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
	}
	return exchange.Order{
		Market:          market,
		ExchangeOrderID: row.OrderID.String(),
		ClientOrderID:   row.ClientOrderID,
		ExchangeSymbol:  row.Symbol,
		OrderType:       exchange.OrderType(row.Type),
		TimeInForce:     exchange.TimeInForce(row.TimeInForce),
		Side:            exchange.Side(row.Side),
		PositionSide:    positionSide,
		Quantity:        quantity,
		LimitPrice:      limitPrice,
		FilledQuantity:  filled,
		AveragePrice:    average,
		ReduceOnly:      row.ReduceOnly,
		Status:          mapOrderStatus(row.Status, filled),
		CreatedAt:       millis(firstPositive(row.Time, row.TransactTime)),
		UpdatedAt:       millis(firstPositive(row.UpdateTime, row.TransactTime)),
	}, nil
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (a *Adapter) fillFromPayload(row tradePayload) (exchange.Fill, error) {
	return fillFromRow(a.config.Market(), a.config.SettlementAsset, row)
}

// fillFromRow 把成交查询的一行转成成交记录；普通账户和统一账户共用。
func fillFromRow(market exchange.MarketType, settlementAsset string, row tradePayload) (exchange.Fill, error) {
	quantity, err := decimalOrZero(row.Qty)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := decimalOrZero(row.Price)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := decimalOrZero(row.Commission)
	if err != nil {
		return exchange.Fill{}, err
	}
	pnl, err := decimalOrZero(row.RealizedPnl)
	if err != nil {
		return exchange.Fill{}, err
	}
	side, maker := exchange.SideSell, row.IsMaker
	if row.IsBuyer {
		side = exchange.SideBuy
	}
	positionSide := exchange.PositionSideUnspecified
	if market == exchange.MarketTypeSwap {
		if row.PositionSide != "" && row.PositionSide != "BOTH" {
			return exchange.Fill{}, typedRejected("hedge fill is unsupported", nil)
		}
		positionSide = exchange.PositionSideNet
		side, maker = exchange.Side(row.Side), row.Maker
		if !side.Valid() {
			return exchange.Fill{}, typedRejected("futures fill has no valid side", nil)
		}
	}
	role := "TAKER"
	if maker {
		role = "MAKER"
	}
	return exchange.Fill{
		Market:          market,
		ExchangeTradeID: row.ID.String(),
		ExchangeOrderID: row.OrderID.String(),
		ExchangeSymbol:  row.Symbol,
		Side:            side,
		PositionSide:    positionSide,
		Quantity:        quantity,
		Price:           price,
		Fee:             fee.Abs(),
		FeeAsset:        row.CommissionAsset,
		RealizedPnL:     pnl,
		SettlementAsset: settlementAsset,
		LiquidityRole:   role,
		TradedAt:        millis(row.Time),
	}, nil
}

func validateOrderRequest(request exchange.OrderRequest, market exchange.MarketType) error {
	if strings.TrimSpace(request.ClientOrderID) == "" ||
		strings.TrimSpace(request.ExchangeSymbol) == "" ||
		!request.Side.Valid() ||
		request.Quantity.Cmp(shared.Zero()) <= 0 {
		return typedRejected("invalid order request", nil)
	}
	switch request.OrderType {
	case exchange.OrderTypeMarket:
		if request.LimitPrice != nil ||
			request.EffectiveFillPolicy() != exchange.FillPolicyUnspecified {
			return typedRejected("MARKET order cannot carry price or TimeInForce", nil)
		}
	case exchange.OrderTypeLimit:
		if request.LimitPrice == nil ||
			request.LimitPrice.Cmp(shared.Zero()) <= 0 ||
			!request.EffectiveFillPolicy().ValidForLimit() {
			return typedRejected("invalid LIMIT order", nil)
		}
	default:
		return typedRejected("unsupported order type", nil)
	}
	if market == exchange.MarketTypeSpot {
		if request.PositionSide != exchange.PositionSideUnspecified || request.ReduceOnly {
			return typedRejected("SPOT order cannot set position side or reduce-only", nil)
		}
	} else if request.PositionSide != exchange.PositionSideNet {
		return typedRejected("SWAP order requires NET position side", nil)
	}
	return nil
}

func classifyAPIError(err error, raw []byte) error {
	if err == nil {
		return nil
	}
	var typed *exchange.Error
	if !errors.As(err, &typed) {
		return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	code := apiCode(raw)
	if code == "" {
		// 本地拦截或传输错误没有响应体，保留 httpclient 的分类。
		return typed
	}
	typed.Code = code
	switch code {
	case "-2010", "-2019":
		typed.Kind = exchange.ErrorInsufficientBalance
	case "-2011", "-2013":
		typed.Kind = exchange.ErrorOrderNotFound
	case "-2015":
		typed.Kind = exchange.ErrorAuthentication
	case "-1003":
		typed.Kind = exchange.ErrorRateLimited
	case "-1015":
		typed.Kind = exchange.ErrorOrderRateLimited
	case "-1008":
		typed.Kind = exchange.ErrorThrottled
	case "-1006", "-1007":
		// 官方说明：后端响应异常或超时，执行状态未知。
		typed.Kind = exchange.ErrorTransportUnknown
	case "-1021":
		// 时间戳超窗时交易所没有受理，但本地无法确认，按结果未知走查询恢复。
		typed.Kind = exchange.ErrorTransportUnknown
	}
	return typed
}

func apiCode(raw []byte) string {
	var payload struct {
		Code json.Number `json:"code"`
	}
	_ = json.Unmarshal(raw, &payload)
	return payload.Code.String()
}

func mapOrderStatus(status string, filled shared.Decimal) exchange.OrderStatus {
	switch status {
	case "NEW":
		return exchange.OrderStatusOpen
	case "PARTIALLY_FILLED":
		return exchange.OrderStatusPartiallyFilled
	case "FILLED":
		return exchange.OrderStatusFilled
	case "CANCELED":
		if filled.Cmp(shared.Zero()) > 0 {
			return exchange.OrderStatusPartiallyCanceled
		}
		return exchange.OrderStatusCanceled
	case "EXPIRED", "EXPIRED_IN_MATCH":
		// EXPIRED_IN_MATCH 是自成交保护让订单过期，已成交部分由成交入账补齐。
		return exchange.OrderStatusExpired
	case "REJECTED":
		return exchange.OrderStatusRejected
	default:
		return exchange.OrderStatusSubmitUnknown
	}
}

func typedRejected(message string, err error) error {
	if err == nil {
		err = errors.New(message)
	} else {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return &exchange.Error{Kind: exchange.ErrorRejected, Err: err}
}

func transportUnknown(message string, err error) error {
	if err == nil {
		err = errors.New(message)
	} else {
		err = fmt.Errorf("%s: %w", message, err)
	}
	return &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if raw == "" {
		return shared.Zero(), nil
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Decimal{}, typedRejected("invalid Exchange decimal", err)
	}
	return value, nil
}

func cloneValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func millis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
