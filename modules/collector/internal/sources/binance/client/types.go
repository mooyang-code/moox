package binance

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

// CandleStick 币安K线原始数据
// 币安返回的是数组格式：[openTime, open, high, low, close, volume, closeTime, quoteVolume, tradeCount, takerBuyVolume, takerBuyQuoteVolume, ignore]
type CandleStick struct {
	OpenTime    int64  // 开盘时间（毫秒）
	Open        string // 开盘价
	High        string // 最高价
	Low         string // 最低价
	Close       string // 收盘价
	Volume      string // 成交量
	CloseTime   int64  // 收盘时间（毫秒）
	QuoteVolume string // 成交额
	TradeCount  int64  // 成交笔数
}

// AggregateTrade is the public aggregate-trade shape returned by Binance
// spot and USD-M futures. It is the cursorable public REST source used by the
// Tick collector when a WebSocket feed is unavailable.
type AggregateTrade struct {
	ID         int64  `json:"a"`
	Price      string `json:"p"`
	Quantity   string `json:"q"`
	TradeTime  int64  `json:"T"`
	BuyerMaker bool   `json:"m"`
}

func (t AggregateTrade) ToTrade() (*exchange.Trade, error) {
	price := common.NewDecimal(t.Price)
	if _, err := price.Float64(); err != nil {
		return nil, fmt.Errorf("aggregate trade price: %w", err)
	}
	quantity := common.NewDecimal(t.Quantity)
	if _, err := quantity.Float64(); err != nil {
		return nil, fmt.Errorf("aggregate trade quantity: %w", err)
	}
	if t.ID <= 0 || t.TradeTime <= 0 {
		return nil, fmt.Errorf("aggregate trade id and time are required")
	}
	return &exchange.Trade{ID: t.ID, Price: price, Quantity: quantity, TradeTime: time.UnixMilli(t.TradeTime).UTC(), BuyerMaker: t.BuyerMaker}, nil
}

// UnmarshalJSON 自定义 JSON 解析（处理数组格式）
func (c *CandleStick) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("解析K线数组失败: %w", err)
	}

	if len(raw) < 9 {
		return fmt.Errorf("K线数据字段不足，期望至少9个，实际%d个", len(raw))
	}

	// 解析各字段
	if err := json.Unmarshal(raw[0], &c.OpenTime); err != nil {
		return fmt.Errorf("解析 openTime 失败: %w", err)
	}
	if err := json.Unmarshal(raw[1], &c.Open); err != nil {
		return fmt.Errorf("解析 open 失败: %w", err)
	}
	if err := json.Unmarshal(raw[2], &c.High); err != nil {
		return fmt.Errorf("解析 high 失败: %w", err)
	}
	if err := json.Unmarshal(raw[3], &c.Low); err != nil {
		return fmt.Errorf("解析 low 失败: %w", err)
	}
	if err := json.Unmarshal(raw[4], &c.Close); err != nil {
		return fmt.Errorf("解析 close 失败: %w", err)
	}
	if err := json.Unmarshal(raw[5], &c.Volume); err != nil {
		return fmt.Errorf("解析 volume 失败: %w", err)
	}
	if err := json.Unmarshal(raw[6], &c.CloseTime); err != nil {
		return fmt.Errorf("解析 closeTime 失败: %w", err)
	}
	if err := json.Unmarshal(raw[7], &c.QuoteVolume); err != nil {
		return fmt.Errorf("解析 quoteVolume 失败: %w", err)
	}

	// tradeCount 可能是数字或字符串
	var tradeCount interface{}
	if err := json.Unmarshal(raw[8], &tradeCount); err != nil {
		return fmt.Errorf("解析 tradeCount 失败: %w", err)
	}
	switch v := tradeCount.(type) {
	case float64:
		c.TradeCount = int64(v)
	case string:
		tc, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("转换 tradeCount 失败: %w", err)
		}
		c.TradeCount = tc
	}

	return nil
}

// ToKline 转换为通用 Kline 结构
func (c *CandleStick) ToKline() (*exchange.Kline, error) {
	return &exchange.Kline{
		OpenTime:    time.UnixMilli(c.OpenTime),
		CloseTime:   time.UnixMilli(c.CloseTime),
		Open:        common.NewDecimal(c.Open),
		High:        common.NewDecimal(c.High),
		Low:         common.NewDecimal(c.Low),
		Close:       common.NewDecimal(c.Close),
		Volume:      common.NewDecimal(c.Volume),
		QuoteVolume: common.NewDecimal(c.QuoteVolume),
		TradeCount:  c.TradeCount,
	}, nil
}

// APIError 币安 API 错误响应
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
}

// ExchangeInfoResponse 交易所信息响应（现货和永续合约通用）
type ExchangeInfoResponse struct {
	Timezone   string          `json:"timezone"`
	ServerTime int64           `json:"serverTime"`
	Symbols    []SymbolInfoRaw `json:"symbols"`
}

func normalizeExchangeInfoSymbols(symbols []string) []string {
	result := make([]string, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		value := strings.ToUpper(FormatSymbol(symbol))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// exchangeInfoSymbolRaw intentionally keeps only fields used by the symbol
// dataset. Binance's exchangeInfo response also contains large order-type,
// permission and filter arrays for every market. Decoding the symbols array
// one object at a time avoids retaining the complete response in a 64MB SCF.
type exchangeInfoSymbolRaw struct {
	Symbol       string       `json:"symbol"`
	Status       string       `json:"status"`
	BaseAsset    string       `json:"baseAsset"`
	QuoteAsset   string       `json:"quoteAsset"`
	ContractType string       `json:"contractType"`
	Pair         string       `json:"pair"`
	Filters      []FilterInfo `json:"filters"`
}

func (s *exchangeInfoSymbolRaw) toSymbolInfo() *exchange.SymbolInfo {
	if s == nil {
		return nil
	}
	raw := SymbolInfoRaw{
		Symbol: s.Symbol, Status: s.Status, BaseAsset: s.BaseAsset,
		QuoteAsset: s.QuoteAsset, ContractType: s.ContractType,
		Pair: s.Pair, Filters: s.Filters,
	}
	return raw.ToSymbolInfo()
}

// decodeExchangeInfo streams the large top-level symbols array. The callback
// decides which entries to retain; the raw entry and its filters become
// collectible before the next entry is decoded.
func decodeExchangeInfo(reader io.Reader, keep func(*exchangeInfoSymbolRaw) bool) (int, []*exchange.SymbolInfo, error) {
	decoder := json.NewDecoder(reader)
	start, err := decoder.Token()
	if err != nil {
		return 0, nil, fmt.Errorf("解析ExchangeInfo对象失败: %w", err)
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return 0, nil, fmt.Errorf("解析ExchangeInfo对象失败: 期望对象")
	}

	var total int
	var symbols []*exchange.SymbolInfo
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return total, nil, fmt.Errorf("解析ExchangeInfo字段失败: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return total, nil, fmt.Errorf("解析ExchangeInfo字段失败: 字段名不是字符串")
		}
		if key != "symbols" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return total, nil, fmt.Errorf("跳过ExchangeInfo字段 %s 失败: %w", key, err)
			}
			continue
		}
		arrayStart, err := decoder.Token()
		if err != nil {
			return total, nil, fmt.Errorf("解析ExchangeInfo symbols 失败: %w", err)
		}
		if delimiter, ok := arrayStart.(json.Delim); !ok || delimiter != '[' {
			return total, nil, fmt.Errorf("解析ExchangeInfo symbols 失败: 期望数组")
		}
		for decoder.More() {
			var raw exchangeInfoSymbolRaw
			if err := decoder.Decode(&raw); err != nil {
				return total, nil, fmt.Errorf("解析ExchangeInfo symbol 失败: %w", err)
			}
			total++
			if keep != nil && !keep(&raw) {
				continue
			}
			if symbol := raw.toSymbolInfo(); symbol != nil {
				symbols = append(symbols, symbol)
			}
		}
		if _, err := decoder.Token(); err != nil {
			return total, nil, fmt.Errorf("结束ExchangeInfo symbols 失败: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return total, nil, fmt.Errorf("结束ExchangeInfo对象失败: %w", err)
	}
	return total, symbols, nil
}

// SymbolInfoRaw 交易对原始信息（币安格式）
type SymbolInfoRaw struct {
	Symbol             string       `json:"symbol"`             // 交易对符号（如 BTCUSDT）
	Status             string       `json:"status"`             // 交易状态：TRADING, HALT, BREAK
	BaseAsset          string       `json:"baseAsset"`          // 基础资产（如 BTC）
	BaseAssetPrecision int          `json:"baseAssetPrecision"` // 基础资产精度
	QuoteAsset         string       `json:"quoteAsset"`         // 计价资产（如 USDT）
	QuotePrecision     int          `json:"quotePrecision"`     // 计价资产精度
	OrderTypes         []string     `json:"orderTypes"`         // 支持的订单类型
	Filters            []FilterInfo `json:"filters"`            // 交易规则过滤器
	Permissions        []string     `json:"permissions"`        // 权限（SPOT/MARGIN等）
	ContractType       string       `json:"contractType"`       // 合约类型（仅永续合约）：PERPETUAL
	Pair               string       `json:"pair"`               // 交易对（仅永续合约）
}

// FilterInfo 交易规则过滤器
type FilterInfo struct {
	FilterType  string `json:"filterType"`  // PRICE_FILTER, LOT_SIZE, MIN_NOTIONAL等
	MinPrice    string `json:"minPrice"`    // 最小价格
	MaxPrice    string `json:"maxPrice"`    // 最大价格
	TickSize    string `json:"tickSize"`    // 价格步长
	MinQty      string `json:"minQty"`      // 最小数量
	MaxQty      string `json:"maxQty"`      // 最大数量
	StepSize    string `json:"stepSize"`    // 数量步长
	MinNotional string `json:"minNotional"` // 最小名义价值
}

// ToSymbolInfo 转换为通用交易对信息
func (s *SymbolInfoRaw) ToSymbolInfo() *exchange.SymbolInfo {
	// 提取交易规则
	var minQty, maxQty, tickSize, lotSize string
	for _, filter := range s.Filters {
		switch filter.FilterType {
		case "LOT_SIZE":
			minQty = filter.MinQty
			maxQty = filter.MaxQty
			lotSize = filter.StepSize
		case "PRICE_FILTER":
			tickSize = filter.TickSize
		}
	}

	// 将 BTCUSDT 格式转换为 BTC-USDT
	formattedSymbol := s.BaseAsset + "-" + s.QuoteAsset

	// 映射状态
	status := "active"
	if s.Status != "TRADING" {
		status = "inactive"
	}

	return &exchange.SymbolInfo{
		Symbol:     formattedSymbol,
		BaseAsset:  s.BaseAsset,
		QuoteAsset: s.QuoteAsset,
		Status:     status,
		MinQty:     minQty,
		MaxQty:     maxQty,
		TickSize:   tickSize,
		LotSize:    lotSize,
	}
}
