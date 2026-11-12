// Package eastmoneyselect 从东方财富的选股器接口取 A 股的股本、每股股利和是否 ST，作为日线的附加字段。
//
// 接口只有当前这一天的值，没有历史。接口里的价格、市值、市盈率、股息率都按盘中实时价计算，而标注的交易日
// （MAX_TRADE_DATE）在盘中还是上一个交易日，所以这里只取和价格无关的量：股本、每股股利（股息率 × 最新价，
// 两个值来自同一条响应，价格的影响相互抵消）、名称里的 ST 标记。市值和估值由因子用收盘价计算。
package eastmoneyselect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

const (
	FieldTotalShares      = "total_shares"
	FieldFloatShares      = "float_shares"
	FieldDividendPerShare = "dividend_per_share"
	FieldIsST             = "is_st"
)

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	RateLimit  marketdata.RateLimitPolicy
}

type Provider struct {
	baseURL string
	http    *commonsrc.FieldHTTP
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://data.eastmoney.com"
	}
	// 2026-11-12 国内实测：单次 0.24 秒；八个函数共用一个出口、并发 64 时最慢 6.5 秒，所以单次超时取 10 秒。
	fallback := marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 10 * time.Second}
	client := commonsrc.NewFieldHTTP(cfg.HTTPClient, cfg.RateLimit, fallback, 1<<20).WithHeaders(commonsrc.EastMoneyHeaders())
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: client}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "eastmoney_select", SourceID: "select_http", DisplayName: "东方财富选股器", Hosts: []string{"data.eastmoney.com"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) FieldSpecs() []marketdata.FieldSpec {
	daily := []string{"1d"}
	return []marketdata.FieldSpec{
		{ID: FieldTotalShares, Name: "总股本", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: daily},
		{ID: FieldFloatShares, Name: "流通股本", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: daily},
		{ID: FieldDividendPerShare, Name: "每股股利", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: daily},
		{ID: FieldIsST, Name: "是否 ST", Type: marketdata.FieldTypeInt, Fill: "prev", Frequencies: daily},
	}
}

type record struct {
	Name        string   `json:"SECURITY_NAME_ABBR"`
	Price       *float64 `json:"NEW_PRICE"`
	TotalShares *float64 `json:"TOTAL_SHARES"`
	FloatShares *float64 `json:"FREE_SHARES"`
	Yield       *float64 `json:"ZXGXL"`
	TradeDate   string   `json:"MAX_TRADE_DATE"`
}

// FetchFields 取这只股票当前的一条记录，按接口标注的交易日与请求区间 [Start, End) 的关系处理：
// 落在区间内，写到那一个交易日上；晚于区间（补历史、隔天补采），没有值；早于区间起点，接口还没更新到这一天，返回错误等下次重试。
func (p *Provider) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	symbol, ok := commonsrc.FieldSymbol(req.SubjectID)
	if !ok || !req.End.After(req.Start) {
		return nil, nil
	}
	query := url.Values{
		"sty":    {"SECURITY_CODE,SECURITY_NAME_ABBR,NEW_PRICE,TOTAL_SHARES,FREE_SHARES,ZXGXL,MAX_TRADE_DATE"},
		"filter": {`(SECURITY_CODE="` + symbol[2:] + `")`},
		"p":      {"1"}, "ps": {"50"}, "source": {"SELECT_SECURITIES"}, "client": {"WEB"},
	}
	body, found, err := p.http.Get(ctx, p.baseURL+"/dataapi/xuangu/list?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("取 %s 股本：%w", req.SubjectID, err)
	}
	if !found {
		return nil, nil
	}
	item, err := parseRecord(body)
	if err != nil {
		return nil, fmt.Errorf("%w: 解析 %s 股本：%v", marketdata.ErrProtocol, req.SubjectID, err)
	}
	if item == nil {
		return nil, nil
	}
	day, err := time.Parse("2006-01-02", firstN(strings.TrimSpace(item.TradeDate), 10))
	if err != nil {
		return nil, fmt.Errorf("%w: %s 的交易日期 %q 无效", marketdata.ErrProtocol, req.SubjectID, item.TradeDate)
	}
	if day.Before(req.Start.UTC().Truncate(24 * time.Hour)) {
		return nil, fmt.Errorf("%w: 选股器的数据还停在 %s，没有更新到 %s", marketdata.ErrNoClosedBar, day.Format("2006-01-02"), req.Start.UTC().Format("2006-01-02"))
	}
	if !day.Before(req.End) {
		return nil, nil
	}
	values := map[string]marketdata.FieldValue{}
	if item.TotalShares != nil && *item.TotalShares > 0 {
		values[FieldTotalShares] = marketdata.DoubleField(*item.TotalShares)
	}
	if item.FloatShares != nil && *item.FloatShares > 0 {
		values[FieldFloatShares] = marketdata.DoubleField(*item.FloatShares)
	}
	if item.Yield != nil && *item.Yield >= 0 && item.Price != nil && *item.Price > 0 {
		values[FieldDividendPerShare] = marketdata.DoubleField(*item.Yield / 100 * *item.Price)
	}
	if name := strings.TrimSpace(item.Name); name != "" {
		st := int64(0)
		if strings.Contains(strings.ToUpper(name), "ST") {
			st = 1
		}
		values[FieldIsST] = marketdata.IntField(st)
	}
	if len(values) == 0 {
		return nil, nil
	}
	return map[time.Time]map[string]marketdata.FieldValue{day: values}, nil
}

// parseRecord 解析选股器响应里的第一条记录；查不到这只股票（data 为空）返回 nil。success 不为 true 是结构性错误。
func parseRecord(body []byte) (*record, error) {
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Result  *struct {
			Data []record `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if !payload.Success {
		return nil, fmt.Errorf("接口返回失败：%s", payload.Message)
	}
	if payload.Result == nil || len(payload.Result.Data) == 0 {
		return nil, nil
	}
	return &payload.Result.Data[0], nil
}

func firstN(value string, n int) string {
	if len(value) > n {
		return value[:n]
	}
	return value
}
