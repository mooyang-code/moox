package binance

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// KlineRequest 是向币安请求 K 线的参数。
type KlineRequest struct {
	Symbol    string
	Interval  string
	Limit     int
	StartTime time.Time
	EndTime   time.Time
}

// Kline 是币安返回的单根原始 K 线（数值已解析为 float64）。
type Kline struct {
	OpenTime    time.Time
	CloseTime   time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      float64
	QuoteVolume float64
	TradeCount  int64
}

// SymbolInfo 是币安 exchangeInfo 解码出的交易对元数据。
type SymbolInfo struct {
	Symbol     string
	BaseAsset  string
	QuoteAsset string
	Status     string
	MinQty     string
	MaxQty     string
	TickSize   string
	LotSize    string
	// Pair 是永续合约的标的交易对（连续合约 K 线按它订阅），现货为空。
	Pair string
	// TradingStatus 是交易所给的原始交易状态（TRADING、BREAK、HALT、DELIVERING……）。
	TradingStatus string
}

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

// UnmarshalJSON 自定义 JSON 解析（处理数组格式）
func (c *CandleStick) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("解析K线数组失败: %w", err)
	}

	if len(raw) < 9 {
		return fmt.Errorf("K线数据字段不足，期望至少9个，实际%d个", len(raw))
	}

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

// ToKline 将原始数组行解析为强类型 Kline。
func (c *CandleStick) ToKline() (*Kline, error) {
	open, err := strconv.ParseFloat(strings.TrimSpace(c.Open), 64)
	if err != nil {
		return nil, fmt.Errorf("解析 open 数值失败: %w", err)
	}
	high, err := strconv.ParseFloat(strings.TrimSpace(c.High), 64)
	if err != nil {
		return nil, fmt.Errorf("解析 high 数值失败: %w", err)
	}
	low, err := strconv.ParseFloat(strings.TrimSpace(c.Low), 64)
	if err != nil {
		return nil, fmt.Errorf("解析 low 数值失败: %w", err)
	}
	closePrice, err := strconv.ParseFloat(strings.TrimSpace(c.Close), 64)
	if err != nil {
		return nil, fmt.Errorf("解析 close 数值失败: %w", err)
	}
	volume, err := strconv.ParseFloat(strings.TrimSpace(c.Volume), 64)
	if err != nil {
		return nil, fmt.Errorf("解析 volume 数值失败: %w", err)
	}
	quoteVolume, err := strconv.ParseFloat(strings.TrimSpace(c.QuoteVolume), 64)
	if err != nil {
		return nil, fmt.Errorf("解析 quoteVolume 数值失败: %w", err)
	}
	return &Kline{
		OpenTime:    time.UnixMilli(c.OpenTime),
		CloseTime:   time.UnixMilli(c.CloseTime),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       closePrice,
		Volume:      volume,
		QuoteVolume: quoteVolume,
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

func (s *exchangeInfoSymbolRaw) toSymbolInfo() *SymbolInfo {
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

// countTrading 数正常交易（TRADING）的交易对，用于日志。
func countTrading(symbols []*SymbolInfo) int {
	n := 0
	for _, symbol := range symbols {
		if symbol != nil && symbol.TradingStatus == "TRADING" {
			n++
		}
	}
	return n
}

// decodeExchangeInfo streams the large top-level symbols array. The callback
// decides which entries to retain; the raw entry and its filters become
// collectible before the next entry is decoded.
func decodeExchangeInfo(reader io.Reader, keep func(*exchangeInfoSymbolRaw) bool) (int, []*SymbolInfo, error) {
	decoder := json.NewDecoder(reader)
	start, err := decoder.Token()
	if err != nil {
		return 0, nil, fmt.Errorf("解析ExchangeInfo对象失败: %w", err)
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return 0, nil, fmt.Errorf("解析ExchangeInfo对象失败: 期望对象")
	}

	var total int
	var symbols []*SymbolInfo
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
	Symbol             string       `json:"symbol"`
	Status             string       `json:"status"`
	BaseAsset          string       `json:"baseAsset"`
	BaseAssetPrecision int          `json:"baseAssetPrecision"`
	QuoteAsset         string       `json:"quoteAsset"`
	QuotePrecision     int          `json:"quotePrecision"`
	OrderTypes         []string     `json:"orderTypes"`
	Filters            []FilterInfo `json:"filters"`
	Permissions        []string     `json:"permissions"`
	ContractType       string       `json:"contractType"`
	Pair               string       `json:"pair"`
}

// FilterInfo 交易规则过滤器
type FilterInfo struct {
	FilterType  string `json:"filterType"`
	MinPrice    string `json:"minPrice"`
	MaxPrice    string `json:"maxPrice"`
	TickSize    string `json:"tickSize"`
	MinQty      string `json:"minQty"`
	MaxQty      string `json:"maxQty"`
	StepSize    string `json:"stepSize"`
	MinNotional string `json:"minNotional"`
}

// ToSymbolInfo 转换为客户端 SymbolInfo 结构。
func (s *SymbolInfoRaw) ToSymbolInfo() *SymbolInfo {
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
	formattedSymbol := s.BaseAsset + "-" + s.QuoteAsset
	status := "active"
	if s.Status != "TRADING" {
		status = "inactive"
	}
	return &SymbolInfo{
		TradingStatus: strings.ToUpper(strings.TrimSpace(s.Status)),
		Symbol:        formattedSymbol,
		BaseAsset:     s.BaseAsset,
		QuoteAsset:    s.QuoteAsset,
		Status:        status,
		MinQty:        minQty,
		MaxQty:        maxQty,
		TickSize:      tickSize,
		LotSize:       lotSize,
		Pair:          strings.ToUpper(strings.TrimSpace(s.Pair)),
	}
}
