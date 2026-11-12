// Package eastmoneyturnover 从东方财富的日线接口取 A 股的换手率，作为日线的附加字段 turnover_rate。
package eastmoneyturnover

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

// FieldTurnoverRate 是换手率的字段 ID，单位是百分比。
const FieldTurnoverRate = "turnover_rate"

// turnoverColumn 是日线每行里换手率所在的列（fields2 的 f61，第 11 项）。
const turnoverColumn = 10

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
		cfg.BaseURL = "https://push2his.eastmoney.com"
	}
	fallback := marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	client := commonsrc.NewFieldHTTP(cfg.HTTPClient, cfg.RateLimit, fallback, 4<<20).WithHeaders(commonsrc.EastMoneyHeaders())
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: client}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "eastmoney_turnover", SourceID: "turnover_http", DisplayName: "东方财富换手率", Hosts: []string{"push2his.eastmoney.com"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

// FieldSpecs：停牌日补出的行换手率是 0（fill=zero），与成交量一致。
func (p *Provider) FieldSpecs() []marketdata.FieldSpec {
	return []marketdata.FieldSpec{{ID: FieldTurnoverRate, Name: "换手率", Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1d"}}}
}

// FetchFields 一次请求取 [Start, End) 内各交易日的换手率。来源没有这只股票、某天没有这一行、那一天的换手率不是数字时不出值。
func (p *Provider) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	if _, ok := commonsrc.FieldSymbol(req.SubjectID); !ok || !req.End.After(req.Start) {
		return nil, nil
	}
	secid, err := commonsrc.EastMoneySecID(req.SubjectID)
	if err != nil {
		return nil, nil
	}
	start := req.Start.UTC().Truncate(24 * time.Hour)
	// End 是开区间：最后一个可能的交易日是 End 之前的那一天。
	last := req.End.UTC().Add(-time.Nanosecond)
	query := url.Values{
		"secid": {secid}, "klt": {"101"}, "fqt": {"0"},
		"beg": {start.Format("20060102")}, "end": {last.Format("20060102")}, "lmt": {"100000"},
		"fields1": {"f1,f2,f3"}, "fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61"},
	}
	body, found, err := p.http.Get(ctx, p.baseURL+"/api/qt/stock/kline/get?"+query.Encode())
	if err != nil {
		return nil, fmt.Errorf("取 %s 换手率：%w", req.SubjectID, err)
	}
	if !found {
		return nil, nil
	}
	rates, err := parseTurnover(body)
	if err != nil {
		return nil, fmt.Errorf("%w: 解析 %s 换手率：%v", marketdata.ErrProtocol, req.SubjectID, err)
	}
	values := make(map[time.Time]map[string]marketdata.FieldValue, len(rates))
	for day, rate := range rates {
		if !day.Before(start) && day.Before(req.End) {
			values[day] = map[string]marketdata.FieldValue{FieldTurnoverRate: marketdata.DoubleField(rate)}
		}
	}
	return values, nil
}

// parseTurnover 解析日线响应，返回各交易日（UTC 零点）的换手率。
// 来源没有这只股票时 data 是 null，返回空；行的列数不足或日期无效是结构性错误；换手率不是有限的非负数的行跳过。
func parseTurnover(body []byte) (map[time.Time]float64, error) {
	var payload struct {
		Data *struct {
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Data == nil {
		return nil, nil
	}
	rates := make(map[time.Time]float64, len(payload.Data.Klines))
	for _, line := range payload.Data.Klines {
		columns := strings.Split(line, ",")
		if len(columns) <= turnoverColumn {
			return nil, fmt.Errorf("日线行只有 %d 列", len(columns))
		}
		day, err := time.Parse("2006-01-02", strings.TrimSpace(columns[0]))
		if err != nil {
			return nil, fmt.Errorf("交易日期 %q 无效", columns[0])
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(columns[turnoverColumn]), 64)
		if err != nil || rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			continue
		}
		rates[day] = rate
	}
	return rates, nil
}
