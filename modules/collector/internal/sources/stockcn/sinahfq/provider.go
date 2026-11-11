// Package sinahfq 从新浪取 A 股的后复权因子，作为日线的附加字段 adj_factor。
package sinahfq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

// FieldAdjFactor 是后复权因子的字段 ID。
const FieldAdjFactor = "adj_factor"

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
		cfg.BaseURL = "https://finance.sina.com.cn"
	}
	fallback := marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: commonsrc.NewFieldHTTP(cfg.HTTPClient, cfg.RateLimit, fallback, 1<<20)}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "sina_hfq", SourceID: "hfq_http", DisplayName: "新浪复权因子", Hosts: []string{"finance.sina.com.cn"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

// FieldSpecs：后复权因子一旦写下就不再变（前复权每次除权都会改写全部历史），前复权价由因子表达式算。
func (p *Provider) FieldSpecs() []marketdata.FieldSpec {
	return []marketdata.FieldSpec{{ID: FieldAdjFactor, Name: "后复权因子", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: []string{"1d"}}}
}

type factor struct {
	date  int
	value float64
}

// FetchFields 给每个交易日写“除权日期不晚于这一天的最后一条”的后复权因子。来源没有这只股票时不出值。
func (p *Provider) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	symbol, ok := commonsrc.FieldSymbol(req.SubjectID)
	if !ok {
		return nil, nil
	}
	body, found, err := p.http.Get(ctx, p.baseURL+"/realstock/company/"+symbol+"/hfq.js")
	if err != nil {
		return nil, fmt.Errorf("取 %s 复权因子：%w", symbol, err)
	}
	if !found {
		return nil, nil
	}
	factors, err := parseFactors(body)
	if err != nil {
		return nil, fmt.Errorf("%w: 解析 %s 复权因子：%v", marketdata.ErrProtocol, symbol, err)
	}
	values := make(map[time.Time]map[string]marketdata.FieldValue)
	for _, day := range commonsrc.DayKeys(req.Start, req.End) {
		date := commonsrc.DateInt(day)
		// 第一条除权日期晚于这一天的位置，前一条就是要用的。
		index := sort.Search(len(factors), func(i int) bool { return factors[i].date > date })
		if index > 0 {
			values[day] = map[string]marketdata.FieldValue{FieldAdjFactor: marketdata.DoubleField(factors[index-1].value)}
		}
	}
	return values, nil
}

// parseFactors 解析 `var sh600519hfq={"total":33,"data":[{"d":"2026-06-26","f":"8.88"},...]} /* ... */`，按除权日期升序返回。
func parseFactors(body []byte) ([]factor, error) {
	start := bytes.IndexByte(body, '{')
	if start < 0 {
		return nil, fmt.Errorf("响应里没有 JSON")
	}
	var payload struct {
		Data []struct {
			Date   string `json:"d"`
			Factor string `json:"f"`
		} `json:"data"`
	}
	// 只解码第一个 JSON 值，后面还跟着一段注释。
	if err := json.NewDecoder(bytes.NewReader(body[start:])).Decode(&payload); err != nil {
		return nil, err
	}
	factors := make([]factor, 0, len(payload.Data))
	for _, item := range payload.Data {
		day, err := time.Parse("2006-01-02", strings.TrimSpace(item.Date))
		if err != nil {
			return nil, fmt.Errorf("除权日期 %q 无效", item.Date)
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(item.Factor), 64)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("%s 的因子 %q 无效", item.Date, item.Factor)
		}
		factors = append(factors, factor{date: commonsrc.DateInt(day), value: value})
	}
	sort.SliceStable(factors, func(i, j int) bool { return factors[i].date < factors[j].date })
	return factors, nil
}
