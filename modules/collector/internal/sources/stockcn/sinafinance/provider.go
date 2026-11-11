// Package sinafinance 从新浪取 A 股的财务三表（利润表、资产负债表、现金流量表），作为日线的附加字段 fin_*。
package sinafinance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
)

// FieldReportPeriod 是这根 K 线上用的财务报告期（yyyymmdd）。
const FieldReportPeriod = "fin_report_period"

// ttmSuffix 是滚动十二个月值的字段后缀。
const ttmSuffix = "_ttm"

// metric 是一个财务科目字段。不同行业的报表模板里同一科目的代码不同，Codes 按顺序取第一个有值的。
type metric struct {
	ID   string
	Name string
	// Table 是科目所在的报表：lrb 利润表、fzb 资产负债表、llb 现金流量表。
	Table string
	Codes []string
	// Flow 表示年初至今的累计值（利润表、现金流量表），另出滚动十二个月值。
	Flow bool
}

// metrics 是第一批财务字段。科目代码取自 2026-11-11 的实际响应：一般工商业（贵州茅台、宁德时代）、
// 银行（平安银行、工商银行、招商银行）、保险（中国平安、中国人寿）、券商（中信证券）。
// 金融类公司没有“营业总收入”，留空。
var metrics = []metric{
	{ID: "fin_total_revenue", Name: "营业总收入", Table: "lrb", Codes: []string{"BIZTOTINCO"}, Flow: true},
	{ID: "fin_revenue", Name: "营业收入", Table: "lrb", Codes: []string{"BIZINCO"}, Flow: true},
	{ID: "fin_operating_profit", Name: "营业利润", Table: "lrb", Codes: []string{"PERPROFIT", "OPERPROFIT"}, Flow: true},
	{ID: "fin_net_profit", Name: "净利润", Table: "lrb", Codes: []string{"NETPROFIT"}, Flow: true},
	{ID: "fin_net_profit_parent", Name: "归母净利润", Table: "lrb", Codes: []string{"PARENETP", "NETPARECOMPPROF", "NETPARESHARPROF"}, Flow: true},
	{ID: "fin_eps_basic", Name: "基本每股收益", Table: "lrb", Codes: []string{"BASICEPS"}, Flow: true},
	{ID: "fin_total_assets", Name: "资产总计", Table: "fzb", Codes: []string{"TOTASSET"}},
	{ID: "fin_total_liabilities", Name: "负债合计", Table: "fzb", Codes: []string{"TOTLIAB"}},
	{ID: "fin_equity_parent", Name: "归母股东权益", Table: "fzb", Codes: []string{"PARESHARRIGH", "PARECOMPTOTEQUI", "PARECOMPSHARRIGHT"}},
	{ID: "fin_operating_cash_flow", Name: "经营活动现金流量净额", Table: "llb", Codes: []string{"MANANETR"}, Flow: true},
}

var tables = []string{"lrb", "fzb", "llb"}

// basePeriods 是不管区间多长都要多取的期数：滚动值最远用到往前第 5 期。
const basePeriods = 6

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	RateLimit  marketdata.RateLimitPolicy
	Now        func() time.Time
}

type Provider struct {
	baseURL string
	http    *commonsrc.FieldHTTP
	now     func() time.Time
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://quotes.sina.cn"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	fallback := marketdata.RateLimitPolicy{RequestsPerSecond: 50, Burst: 20, MaxConcurrent: 16, Cooldown: time.Second, RequestTimeout: 5 * time.Second}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: commonsrc.NewFieldHTTP(cfg.HTTPClient, cfg.RateLimit, fallback, 16<<20), now: cfg.Now}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "sina_finance", SourceID: "finance_http", DisplayName: "新浪财务报表", Hosts: []string{"quotes.sina.cn"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

func (p *Provider) FieldSpecs() []marketdata.FieldSpec {
	spec := func(id, name string, kind marketdata.FieldValueType) marketdata.FieldSpec {
		return marketdata.FieldSpec{ID: id, Name: name, Type: kind, Fill: "prev", Frequencies: []string{"1d"}}
	}
	specs := []marketdata.FieldSpec{spec(FieldReportPeriod, "财务报告期", marketdata.FieldTypeInt)}
	for _, item := range metrics {
		specs = append(specs, spec(item.ID, item.Name, marketdata.FieldTypeDouble))
	}
	for _, item := range metrics {
		if item.Flow {
			specs = append(specs, spec(item.ID+ttmSuffix, item.Name+"（滚动十二个月）", marketdata.FieldTypeDouble))
		}
	}
	return specs
}

// report 是一个报告期的财务数据。
type report struct {
	// period 是报告期（yyyymmdd）。
	period int
	// disclosed 是披露日（yyyymmdd）：从它的下一个交易日起这一期可用。
	disclosed int
	// values 按字段 ID 给出科目值；这家公司的报表里没有的科目不在其中。
	values map[string]float64
}

// FetchFields 给每个交易日写“已生效的报告期里最大的一期”的财务数据。来源没有这只股票、或这一天之前没有已生效的报告期时不出值。
// 三张表任何一张取不到都返回错误。
func (p *Provider) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	symbol, ok := commonsrc.FieldSymbol(req.SubjectID)
	if !ok {
		return nil, nil
	}
	reports := map[int]*report{}
	periods := periodCount(req.Start, p.now())
	for _, table := range tables {
		query := url.Values{"paperCode": {symbol}, "source": {table}, "type": {"0"}, "page": {"1"}, "num": {strconv.Itoa(periods)}}
		body, found, err := p.http.Get(ctx, p.baseURL+"/cn/api/openapi.php/CompanyFinanceService.getFinanceReport2022?"+query.Encode())
		if err == nil && !found {
			err = fmt.Errorf("%w: status=404", marketdata.ErrHTTPStatus)
		}
		if err != nil {
			return nil, fmt.Errorf("取 %s 财务报表 %s：%w", symbol, table, err)
		}
		if err := mergeTable(reports, table, body); err != nil {
			return nil, fmt.Errorf("%w: 解析 %s 财务报表 %s：%v", marketdata.ErrProtocol, symbol, table, err)
		}
	}
	values := make(map[time.Time]map[string]marketdata.FieldValue)
	for _, day := range commonsrc.DayKeys(req.Start, req.End) {
		if row := fieldsOn(reports, commonsrc.DateInt(day)); row != nil {
			values[day] = row
		}
	}
	return values, nil
}

// periodCount 是一次请求要取的期数：接口只能从最新一期往回取，所以取“区间起点到现在跨的季度数 + 6”。
// 实时批次的起点就是最近一个交易日，取 6 期。2026-11-11 实测一次取 40 期也能返回，不翻页。
func periodCount(start, now time.Time) int {
	start, now = start.UTC(), now.UTC()
	months := (now.Year()-start.Year())*12 + int(now.Month()) - int(start.Month())
	if start.IsZero() || months < 0 {
		months = 0
	}
	return basePeriods + (months+2)/3
}

// mergeTable 把一张表的响应并进 reports。
// 披露日取三张表里最早的 publish_date：同一报告期的三张表是一起披露的，但旧的报告期被后来的报告当作对比数重新列示时，
// publish_date 会变成那一次的日期，而且三张表不同步（利润表、现金流量表的对比数是上年同期，资产负债表的是上年年末）。
func mergeTable(reports map[int]*report, table string, body []byte) error {
	var payload struct {
		Result struct {
			Status struct {
				Code int `json:"code"`
			} `json:"status"`
			Data *struct {
				ReportList json.RawMessage `json:"report_list"`
			} `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if payload.Result.Status.Code != 0 {
		return fmt.Errorf("接口返回状态码 %d", payload.Result.Status.Code)
	}
	// 来源没有这只股票时 data 是 null；没有报告期时 report_list 可能是空数组。
	if payload.Result.Data == nil {
		return nil
	}
	list := bytes.TrimSpace(payload.Result.Data.ReportList)
	if len(list) == 0 || list[0] != '{' {
		return nil
	}
	var periods map[string]struct {
		PublishDate string `json:"publish_date"`
		Data        []struct {
			Field string          `json:"item_field"`
			Value json.RawMessage `json:"item_value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list, &periods); err != nil {
		return err
	}
	for key, content := range periods {
		period, err := parseDate(key)
		if err != nil {
			return fmt.Errorf("报告期 %q 无效", key)
		}
		published := 0
		if text := strings.TrimSpace(content.PublishDate); text != "" {
			if published, err = parseDate(text); err != nil {
				return fmt.Errorf("报告期 %s 的披露日 %q 无效", key, content.PublishDate)
			}
		}
		item := reports[period]
		if item == nil {
			item = &report{period: period, disclosed: statutoryDeadline(period), values: map[string]float64{}}
			reports[period] = item
		}
		if published > 0 && (item.disclosed == 0 || published < item.disclosed) {
			item.disclosed = published
		}
		raw := make(map[string]float64, len(content.Data))
		for _, entry := range content.Data {
			text := strings.Trim(strings.TrimSpace(string(entry.Value)), `"`)
			if entry.Field == "" || text == "" || text == "null" {
				continue
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return fmt.Errorf("报告期 %s 科目 %s 的值 %q 不是数字", key, entry.Field, text)
			}
			if _, dup := raw[entry.Field]; !dup {
				raw[entry.Field] = value
			}
		}
		for _, field := range metrics {
			if field.Table != table {
				continue
			}
			for _, code := range field.Codes {
				if value, ok := raw[code]; ok {
					item.values[field.ID] = value
					break
				}
			}
		}
	}
	return nil
}

func parseDate(text string) (int, error) {
	day, err := time.Parse("20060102", text)
	if err != nil {
		return 0, err
	}
	return commonsrc.DateInt(day), nil
}

// statutoryDeadline 返回报告期的法定披露截止日：一季报 4 月 30 日、半年报 8 月 31 日、三季报 10 月 31 日、年报次年 4 月 30 日。
// 不是季度末的报告期返回 0（只按 publish_date）。
func statutoryDeadline(period int) int {
	year := period / 10000
	switch period % 10000 {
	case 331:
		return year*10000 + 430
	case 630:
		return year*10000 + 831
	case 930:
		return year*10000 + 1031
	case 1231:
		return (year+1)*10000 + 430
	}
	return 0
}

// effective 判断报告期在 date 这个交易日是否已经可用：披露日的下一个交易日起可用，即交易日晚于披露日。
func effective(item *report, date int) bool {
	return item != nil && item.disclosed > 0 && date > item.disclosed
}

// fieldsOn 返回 date 这个交易日上的财务字段：取已生效的报告期里最大的一期
// （年报与一季报常在同一天生效，按生效日排序分不出先后）。没有已生效的报告期时返回 nil。
func fieldsOn(reports map[int]*report, date int) map[string]marketdata.FieldValue {
	var current *report
	for _, item := range reports {
		if effective(item, date) && (current == nil || item.period > current.period) {
			current = item
		}
	}
	if current == nil {
		return nil
	}
	row := map[string]marketdata.FieldValue{FieldReportPeriod: marketdata.IntField(int64(current.period))}
	// 滚动十二个月 = 本期累计 + 上年年报 − 上年同期累计；本期是年报时就是年报值。三项都要已生效且有这个科目。
	annual := current.period%10000 == 1231
	lastAnnual := reports[(current.period/10000-1)*10000+1231]
	lastSame := reports[current.period-10000]
	for _, field := range metrics {
		value, ok := current.values[field.ID]
		if !ok {
			continue
		}
		row[field.ID] = marketdata.DoubleField(value)
		if !field.Flow {
			continue
		}
		if annual {
			row[field.ID+ttmSuffix] = marketdata.DoubleField(value)
			continue
		}
		if !effective(lastAnnual, date) || !effective(lastSame, date) {
			continue
		}
		yearValue, hasYear := lastAnnual.values[field.ID]
		sameValue, hasSame := lastSame.values[field.ID]
		if hasYear && hasSame {
			row[field.ID+ttmSuffix] = marketdata.DoubleField(value + yearValue - sameValue)
		}
	}
	return row
}
