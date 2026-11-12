// Package datahub 是美股的标普 500 成分来源：开源数据集 datasets/s-and-p-500-companies 的 constituents.csv，
// 只取成分股，不取 K 线、不取全部标的。
//
// 数据集每天 UTC 00:00 由 GitHub Action 抓取维基百科的成分表更新；这里经 jsdelivr 读取
// （控制机连不上 raw.githubusercontent.com），jsdelivr 对分支引用缓存最多 12 小时，
// 所以调样后可能晚一两天生效。
//
// CSV 里没有交易所：每只成分的交易所由“交易所对照”（sources/stockus/exchangelist，查东方财富美股列表）给出，
// 任何一只对不上就整次失败并列出全部对不上的代码，标签同步会保留旧成员。
package datahub

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockus"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/stockus/exchangelist"
)

const (
	providerID = "datahub"
	marketID   = "stockus"
	// DefaultSourceID 是行情源目录里登记的 source_id。
	DefaultSourceID = "index_constituent_http"

	host        = "cdn.jsdelivr.net"
	defaultBase = "https://" + host
	listPath    = "/gh/datasets/s-and-p-500-companies@main/data/constituents.csv"
	// CSV 不报总数：标普 500 约 503 个代码，行数不在 495～515 之间说明读到的不是完整名单。
	minRows = 495
	maxRows = 515
	// 单次超时：控制机实测 jsdelivr 约 2 秒。
	defaultTimeout = 30 * time.Second
	maxBodyBytes   = 4 << 20
	// CSV 里要用到的两列，按表头名查找（不按位置）。
	symbolColumn   = "Symbol"
	securityColumn = "Security"
)

// ExchangeLookup 给一组成分代码对上交易所，返回 {代码: MIC}，对不上的代码不在结果里。
type ExchangeLookup func(ctx context.Context, codes []string) (map[string]string, error)

// Config 是数据源的参数，零值都有默认值。
type Config struct {
	// BaseURL 是 CDN 的地址（不含路径），默认 jsdelivr；测试用假服务。
	BaseURL  string
	SourceID string
	// HTTPClient 为空时自建直连客户端，超时取 RequestTimeout。
	HTTPClient *http.Client
	Now        func() time.Time
	// RequestTimeout 是读 CSV 的单次请求超时，默认 30 秒。
	RequestTimeout time.Duration
	// ExchangeListBaseURL 是默认交易所对照用的东方财富列表地址，空则用主地址（测试用假服务）；
	// 给了自定义的 Exchanges 时不用它。
	ExchangeListBaseURL string
	// Exchanges 是交易所对照，默认用 exchangelist 连东方财富；测试里换成假的。
	Exchanges ExchangeLookup
}

type Provider struct {
	baseURL   string
	sourceID  string
	client    *http.Client
	now       func() time.Time
	exchanges ExchangeLookup
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBase
	}
	if strings.TrimSpace(cfg.SourceID) == "" {
		cfg.SourceID = DefaultSourceID
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.RequestTimeout}
	}
	if cfg.Exchanges == nil {
		cfg.Exchanges = func(ctx context.Context, codes []string) (map[string]string, error) {
			return exchangelist.Lookup(ctx, exchangelist.Config{BaseURL: cfg.ExchangeListBaseURL}, codes)
		}
	}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), sourceID: strings.ToLower(strings.TrimSpace(cfg.SourceID)), client: cfg.HTTPClient, now: cfg.Now, exchanges: cfg.Exchanges}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: providerID, SourceID: p.sourceID, DisplayName: "S&P 500 (datahub)", Hosts: []string{host}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

// IndexCodes 返回能取成分股的指数代码。
func (p *Provider) IndexCodes() []string { return []string{"SPX"} }

type constituent struct {
	code string
	name string
}

// FetchIndexConstituents 取标普 500 的全部成分股。名单必须完整才返回：行数在 495～515 之间、代码无重复、
// 代码合法、每一只都对上了交易所、每个拼出的标的 ID 都能被美股规则解析；不满足就整次报错。
func (p *Provider) FetchIndexConstituents(ctx context.Context, indexCode string) (marketdata.InstrumentSnapshot, error) {
	if strings.TrimSpace(indexCode) != "SPX" {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: datahub 没有登记指数 %q 的成分列表", marketdata.ErrInvalidRequest, indexCode)
	}
	fetchedAt := p.now().UTC()
	members, err := p.fetchConstituents(ctx)
	if err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股：%w", indexCode, err)
	}
	codes := make([]string, len(members))
	for i, member := range members {
		codes[i] = member.code
	}
	exchanges, err := p.exchanges(ctx, codes)
	if err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("指数 %s 成分股对交易所：%w", indexCode, err)
	}
	var unmatched []string
	for _, code := range codes {
		if exchanges[code] == "" {
			unmatched = append(unmatched, code)
		}
	}
	if len(unmatched) > 0 {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 有 %d 只成分在东方财富美股列表里对不上交易所：%s", marketdata.ErrProtocol, indexCode, len(unmatched), strings.Join(unmatched, "、"))
	}
	instruments := make([]marketdata.Instrument, 0, len(members))
	counts := map[string]int{}
	var invalid []string
	for _, member := range members {
		exchange := exchanges[member.code]
		subjectID := member.code + "." + exchange
		subject, err := stockus.Rules{}.ParseSubject(marketdata.InstrumentEquity, subjectID)
		if err != nil || subject.Code != member.code || subject.Exchange != exchange {
			invalid = append(invalid, subjectID)
			continue
		}
		instruments = append(instruments, marketdata.Instrument{SubjectID: subjectID, ProviderSymbol: subject.Code, Exchange: subject.Exchange, Name: member.name, Status: "active"})
		counts[exchange]++
	}
	if len(invalid) > 0 {
		sort.Strings(invalid)
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股拼出的标的 ID 不合法：%s", marketdata.ErrProtocol, indexCode, strings.Join(invalid, "、"))
	}
	snapshot := marketdata.InstrumentSnapshot{
		SnapshotID:     marketdata.SnapshotID(providerID, marketID, fetchedAt),
		SourceProvider: providerID,
		MarketID:       marketID,
		FetchedAt:      fetchedAt,
		Complete:       true,
		PageCount:      1,
		ExchangeCounts: counts,
		Instruments:    instruments,
	}
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return marketdata.InstrumentSnapshot{}, fmt.Errorf("%w: 指数 %s 成分股：%v", marketdata.ErrProtocol, indexCode, err)
	}
	return snapshot, nil
}

// fetchConstituents 读 CSV 并校验：表头有 Symbol、Security 两列，行数在 495～515 之间，代码不为空、不重复。
// 代码的合法性在拼出标的 ID 之后统一用 ParseSubject 校验（这里先用 XNAS 占位校验，以便在对照交易所
// 之前就拒绝明显有问题的名单，不白白发出大量东方财富请求）。
func (p *Provider) fetchConstituents(ctx context.Context) ([]constituent, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+listPath, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := p.client.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: 请求已取消或超时：%w（%v）", marketdata.ErrTimeout, ctxErr, err)
		}
		return nil, fmt.Errorf("%w: %v", marketdata.ErrTimeout, err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, marketdata.ErrRateLimited
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: status=%d", marketdata.ErrHTTPStatus, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: 读返回内容：%v", marketdata.ErrProtocol, err)
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "\ufeff")))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: 解析 CSV：%v", marketdata.ErrProtocol, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: CSV 为空", marketdata.ErrProtocol)
	}
	symbolIdx, securityIdx := -1, -1
	for i, name := range records[0] {
		switch strings.TrimSpace(name) {
		case symbolColumn:
			symbolIdx = i
		case securityColumn:
			securityIdx = i
		}
	}
	if symbolIdx < 0 || securityIdx < 0 {
		return nil, fmt.Errorf("%w: CSV 表头缺少 %s 或 %s 列：%v", marketdata.ErrProtocol, symbolColumn, securityColumn, records[0])
	}
	rows := records[1:]
	if len(rows) < minRows || len(rows) > maxRows {
		return nil, fmt.Errorf("%w: 成分股有 %d 行，不在合理范围 %d～%d 内", marketdata.ErrProtocol, len(rows), minRows, maxRows)
	}
	seen := make(map[string]struct{}, len(rows))
	members := make([]constituent, 0, len(rows))
	var invalid []string
	for line, record := range rows {
		if symbolIdx >= len(record) || securityIdx >= len(record) {
			return nil, fmt.Errorf("%w: CSV 第 %d 行列数不够：%v", marketdata.ErrProtocol, line+2, record)
		}
		code := strings.TrimSpace(record[symbolIdx])
		if code == "" {
			return nil, fmt.Errorf("%w: CSV 第 %d 行代码为空", marketdata.ErrProtocol, line+2)
		}
		if _, err := (stockus.Rules{}).ParseSubject(marketdata.InstrumentEquity, code+"."+stockus.ExchangeNasdaq); err != nil {
			invalid = append(invalid, fmt.Sprintf("%q", code))
			continue
		}
		if _, dup := seen[code]; dup {
			return nil, fmt.Errorf("%w: 成分股里 %s 重复出现", marketdata.ErrProtocol, code)
		}
		seen[code] = struct{}{}
		members = append(members, constituent{code: code, name: strings.TrimSpace(record[securityIdx])})
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("%w: 成分股里有不合法的代码：%s", marketdata.ErrProtocol, strings.Join(invalid, "、"))
	}
	return members, nil
}
