package delist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
)

// Source 是一个下架信息来源。
type Source interface {
	// Name 是来源的名字，用于日志和关注项。
	Name() string
	// Fetch 返回来源当前报告的、下架时间在未来的下架通知（USDT 计价的现货和 U 本位永续）。
	Fetch(ctx context.Context) ([]Notice, error)
}

// ErrReadonlyKeyMissing 表示没有配置只读 Key（币安现货下架计划需要）。来源本轮跳过，下一轮继续尝试。
var ErrReadonlyKeyMissing = errors.New("没有配置币安只读 Key")

// 币安接口地址。
const (
	binanceSpotDomain   = "api.binance.com"
	binanceSwapDomain   = "fapi.binance.com"
	binanceDelistPath   = "/sapi/v1/spot/delist-schedule"
	binanceSwapInfoPath = "/fapi/v1/exchangeInfo"
	okxDomain           = "openapi.okx.com"
	okxInstrumentsPath  = "/api/v5/public/instruments"
	// binanceFarFuture 之后的交割时间是币安给永续合约填的占位值（官方样例是 4133404800000，约 2100 年底），不是真实的下架时间。
	binanceFarFuture = int64(4102444800000) // 2100-01-01
)

// KeyProvider 提供币安只读 Key 的公开标识（API Key，放进 X-MBX-APIKEY 请求头）。
type KeyProvider interface {
	ReadonlyKey(ctx context.Context) (string, error)
}

// Getter 是币安客户端带请求头的 GET（binance.Client 实现它）。
type Getter interface {
	GetDirectWithHeaders(ctx context.Context, domain, path string, query url.Values, headers http.Header, result interface{}) error
}

// PlainGetter 是不带额外请求头的 JSON GET（httpclient.HTTPClient 实现它）。
type PlainGetter interface {
	Get(ctx context.Context, domain, path string, query url.Values, result interface{}) error
}

func usdtOnly(symbol string) bool { return strings.HasSuffix(symbol, "USDT") }

// BinanceSpot 读币安现货下架计划：行情数据类接口，请求头带只读 Key，不需要签名。
type BinanceSpot struct {
	Client Getter
	Keys   KeyProvider
	Now    func() time.Time
}

// Name 实现 Source。
func (s *BinanceSpot) Name() string { return "币安现货下架计划" }

type binanceDelistSchedule []struct {
	DelistTime int64    `json:"delistTime"`
	Symbols    []string `json:"symbols"`
}

// Fetch 实现 Source。
func (s *BinanceSpot) Fetch(ctx context.Context) ([]Notice, error) {
	key, err := s.Keys.ReadonlyKey(ctx)
	if err != nil {
		return nil, err
	}
	now := nowOf(s.Now)
	var schedule binanceDelistSchedule
	headers := http.Header{}
	headers.Set("X-MBX-APIKEY", key)
	query := url.Values{"timestamp": []string{strconv.FormatInt(now.UnixMilli(), 10)}}
	if err := s.Client.GetDirectWithHeaders(ctx, binanceSpotDomain, binanceDelistPath, query, headers, &schedule); err != nil {
		return nil, fmt.Errorf("请求币安现货下架计划失败: %w", err)
	}
	return parseBinanceSpotSchedule(schedule, now), nil
}

func parseBinanceSpotSchedule(schedule binanceDelistSchedule, now time.Time) []Notice {
	var notices []Notice
	for _, item := range schedule {
		delistAt := time.UnixMilli(item.DelistTime).UTC()
		if item.DelistTime <= 0 || !delistAt.After(now) {
			continue
		}
		for _, symbol := range item.Symbols {
			if usdtOnly(symbol) {
				notices = append(notices, Notice{Exchange: "binance", Market: events.DelistMarketSpot, Symbol: symbol, DelistAt: delistAt, Source: events.DelistSourceBinanceSpotSchedule})
			}
		}
	}
	return notices
}

// BinanceSwap 读币安 U 本位合约信息。单独解码，不复用过滤掉非 TRADING 合约的 GetExchangeInfo。
// 永续合约的交割时间正常是占位的远期值，出现真实的近期交割时间就是即将下架。
type BinanceSwap struct {
	Client PlainGetter
	Now    func() time.Time
}

// Name 实现 Source。
func (s *BinanceSwap) Name() string { return "币安 U 本位合约信息" }

type binanceSwapExchangeInfo struct {
	Symbols []struct {
		Symbol       string `json:"symbol"`
		ContractType string `json:"contractType"`
		QuoteAsset   string `json:"quoteAsset"`
		DeliveryDate int64  `json:"deliveryDate"`
		Status       string `json:"status"`
	} `json:"symbols"`
}

// Fetch 实现 Source。
func (s *BinanceSwap) Fetch(ctx context.Context) ([]Notice, error) {
	var info binanceSwapExchangeInfo
	if err := s.Client.Get(ctx, binanceSwapDomain, binanceSwapInfoPath, nil, &info); err != nil {
		return nil, fmt.Errorf("请求币安 U 本位合约信息失败: %w", err)
	}
	return parseBinanceSwapExchangeInfo(info, nowOf(s.Now)), nil
}

func parseBinanceSwapExchangeInfo(info binanceSwapExchangeInfo, now time.Time) []Notice {
	var notices []Notice
	for _, item := range info.Symbols {
		if item.ContractType != "PERPETUAL" || item.QuoteAsset != "USDT" {
			continue
		}
		delistAt := time.UnixMilli(item.DeliveryDate).UTC()
		if item.DeliveryDate <= 0 || item.DeliveryDate >= binanceFarFuture || !delistAt.After(now) {
			continue
		}
		notices = append(notices, Notice{Exchange: "binance", Market: events.DelistMarketSwap, Symbol: item.Symbol, DelistAt: delistAt, Source: events.DelistSourceBinanceSwapExchangeInfo})
	}
	return notices
}

// OKXSwap 读 OKX 永续合约信息：公开接口，只看 expTime。永续合约的 expTime 正常是空的，有值就是计划下架。
type OKXSwap struct {
	Client PlainGetter
	Now    func() time.Time
}

// Name 实现 Source。
func (s *OKXSwap) Name() string { return "OKX 永续合约信息" }

type okxInstruments struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID    string `json:"instId"`
		SettleCcy string `json:"settleCcy"`
		State     string `json:"state"`
		ExpTime   string `json:"expTime"`
	} `json:"data"`
}

// Fetch 实现 Source。
func (s *OKXSwap) Fetch(ctx context.Context) ([]Notice, error) {
	var info okxInstruments
	if err := s.Client.Get(ctx, okxDomain, okxInstrumentsPath, url.Values{"instType": []string{"SWAP"}}, &info); err != nil {
		return nil, fmt.Errorf("请求 OKX 永续合约信息失败: %w", err)
	}
	if info.Code != "0" {
		return nil, fmt.Errorf("OKX 返回错误 code=%s msg=%s", info.Code, info.Msg)
	}
	return parseOKXInstruments(info, nowOf(s.Now)), nil
}

func parseOKXInstruments(info okxInstruments, now time.Time) []Notice {
	var notices []Notice
	for _, item := range info.Data {
		if !strings.HasSuffix(item.InstID, "-USDT-SWAP") || strings.TrimSpace(item.ExpTime) == "" {
			continue
		}
		expiry, err := strconv.ParseInt(item.ExpTime, 10, 64)
		delistAt := time.UnixMilli(expiry).UTC()
		if err != nil || expiry <= 0 || !delistAt.After(now) {
			continue
		}
		notices = append(notices, Notice{Exchange: "okx", Market: events.DelistMarketSwap, Symbol: item.InstID, DelistAt: delistAt, Source: events.DelistSourceOKXSwapInstruments})
	}
	return notices
}

func nowOf(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}
