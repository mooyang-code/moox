// Package swsresearch 从申万宏源研究的行业分类文件取 A 股的申万行业，作为日线的附加字段 sw_l1、sw_l2、sw_l3。
package swsresearch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	commonsrc "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"github.com/pbnjay/grate"
	"github.com/pbnjay/grate/xls"
)

const (
	FieldSWLevel1 = "sw_l1"
	FieldSWLevel2 = "sw_l2"
	FieldSWLevel3 = "sw_l3"
)

// intermediatePEM 是 DigiCert 公开发布的中间证书 “GeoTrust G2 TLS CN RSA4096 SHA256 2022 CA1”（有效期到 2032-12-14）。
// www.swsresearch.com 的服务器握手时不发这张证书，Linux 上只凭系统根证书拼不出完整的链。
//
//go:embed geotrust_g2_tls_cn_rsa4096_sha256_2022_ca1.pem
var intermediatePEM []byte

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	RateLimit  marketdata.RateLimitPolicy
}

type Provider struct {
	baseURL string
	http    *commonsrc.FieldHTTP

	// 一个实例（一次函数调用）内文件只下载一次，供这次调用的全部标的使用；下载失败也记下来，不再重试。
	mu      sync.Mutex
	loaded  bool
	table   map[string][]change
	loadErr error
}

func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://www.swsresearch.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = newHTTPClient()
	}
	fallback := marketdata.RateLimitPolicy{RequestsPerSecond: 1, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 20 * time.Second}
	return &Provider{baseURL: strings.TrimRight(cfg.BaseURL, "/"), http: commonsrc.NewFieldHTTP(cfg.HTTPClient, cfg.RateLimit, fallback, 16<<20)}
}

// newHTTPClient 返回校验证书链时带上内嵌中间证书的客户端。
func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig()
	return &http.Client{Transport: transport}
}

// tlsConfig 仍按系统根证书校验整条链和主机名，只是把内嵌的中间证书加进中间证书池。
// 标准库的默认校验不能追加中间证书，所以按 crypto/tls 文档的做法：关掉默认校验，在 VerifyConnection 里做同样的校验。
func tlsConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // 校验没有关：由下面的 VerifyConnection 完成。
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("服务器没有发证书")
			}
			intermediates := x509.NewCertPool()
			if !intermediates.AppendCertsFromPEM(intermediatePEM) {
				return errors.New("内嵌的中间证书无效")
			}
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: state.ServerName, Intermediates: intermediates})
			return err
		},
	}
}

func (p *Provider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "swsresearch", SourceID: "sw_class_http", DisplayName: "申万行业分类", Hosts: []string{"www.swsresearch.com"}, ProtocolVariant: "http", Transport: "https", Port: 443, Status: marketdata.SourceEnabled}
}

// FieldSpecs：申万只发布行业代码，不发布中文名。
func (p *Provider) FieldSpecs() []marketdata.FieldSpec {
	spec := func(id, name string) marketdata.FieldSpec {
		return marketdata.FieldSpec{ID: id, Name: name, Type: marketdata.FieldTypeInt, Fill: "prev", Frequencies: []string{"1d"}}
	}
	return []marketdata.FieldSpec{spec(FieldSWLevel1, "申万一级行业"), spec(FieldSWLevel2, "申万二级行业"), spec(FieldSWLevel3, "申万三级行业")}
}

// change 是一只股票的一次行业调整：从 date（yyyymmdd）起属于 industry（6 位行业代码）。
type change struct {
	date     int
	industry int64
}

// FetchFields 给每个交易日写“计入日期不晚于这一天的最后一条”的行业；文件里没有这只股票时不出值。
func (p *Provider) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	symbol, ok := commonsrc.FieldSymbol(req.SubjectID)
	if !ok {
		return nil, nil
	}
	table, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	changes := table[symbol[2:]]
	values := make(map[time.Time]map[string]marketdata.FieldValue)
	for _, day := range commonsrc.DayKeys(req.Start, req.End) {
		date := commonsrc.DateInt(day)
		index := sort.Search(len(changes), func(i int) bool { return changes[i].date > date })
		if index == 0 {
			continue
		}
		industry := changes[index-1].industry
		values[day] = map[string]marketdata.FieldValue{
			FieldSWLevel1: marketdata.IntField(industry / 10000 * 10000),
			FieldSWLevel2: marketdata.IntField(industry / 100 * 100),
			FieldSWLevel3: marketdata.IntField(industry),
		}
	}
	return values, nil
}

func (p *Provider) load(ctx context.Context) (map[string][]change, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return p.table, p.loadErr
	}
	table, err := p.download(ctx)
	if err != nil && ctx.Err() != nil {
		// 这一项自己被撤销（它的 K 线失败了），不算文件取不到，留给下一个标的再取。
		return nil, err
	}
	p.loaded, p.table, p.loadErr = true, table, err
	return table, err
}

func (p *Provider) download(ctx context.Context) (map[string][]change, error) {
	body, found, err := p.http.Get(ctx, p.baseURL+"/swindex/pdf/SwClass2021/StockClassifyUse_stock.xls")
	if err != nil {
		return nil, fmt.Errorf("下载申万行业分类文件：%w", err)
	}
	if !found {
		return nil, fmt.Errorf("%w: 申万行业分类文件不存在（404）", marketdata.ErrHTTPStatus)
	}
	table, err := parseClassification(body)
	if err != nil {
		return nil, fmt.Errorf("%w: 解析申万行业分类文件：%v", marketdata.ErrProtocol, err)
	}
	return table, nil
}

// parseClassification 解析行业分类文件（旧版 .xls，第一张表四列：股票代码、计入日期、行业代码、更新日期，每次调整一行），
// 返回每只股票按计入日期升序的调整记录。任何一行格式不对都报错，不出部分结果。
func parseClassification(body []byte) (table map[string][]change, err error) {
	// 读取库只接受文件路径。
	file, err := os.CreateTemp("", "sw-class-*.xls")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	// 文件损坏时读取库可能越界。
	defer func() {
		if recovered := recover(); recovered != nil {
			table, err = nil, fmt.Errorf("文件损坏：%v", recovered)
		}
	}()
	book, err := xls.Open(file.Name())
	if err != nil {
		return nil, err
	}
	defer book.Close()
	sheets, err := book.List()
	if err != nil || len(sheets) == 0 {
		return nil, fmt.Errorf("文件里没有工作表：%v", err)
	}
	sheet, err := book.Get(sheets[0])
	if err != nil {
		return nil, err
	}
	if !sheet.Next() {
		return nil, fmt.Errorf("工作表是空的")
	}
	if header := sheet.Strings(); len(header) < 3 || strings.TrimSpace(header[0]) != "股票代码" || strings.TrimSpace(header[2]) != "行业代码" {
		return nil, fmt.Errorf("表头不是预期的格式：%q", header)
	}
	table = make(map[string][]change)
	for row := 2; sheet.Next(); row++ {
		if blankRow(sheet) {
			continue
		}
		var code, industryText string
		var joined time.Time
		if err := sheet.Scan(&code, &joined, &industryText); err != nil {
			return nil, fmt.Errorf("第 %d 行：%v", row, err)
		}
		code, industryText = strings.TrimSpace(code), strings.TrimSpace(industryText)
		industry, parseErr := strconv.ParseInt(industryText, 10, 64)
		if len(code) != 6 || len(industryText) != 6 || parseErr != nil || joined.IsZero() {
			return nil, fmt.Errorf("第 %d 行：股票代码 %q、行业代码 %q 或计入日期无效", row, code, industryText)
		}
		table[code] = append(table[code], change{date: joined.Year()*10000 + int(joined.Month())*100 + joined.Day(), industry: industry})
	}
	if err := sheet.Err(); err != nil {
		return nil, err
	}
	if len(table) == 0 {
		return nil, fmt.Errorf("文件里没有行业记录")
	}
	for _, changes := range table {
		sort.SliceStable(changes, func(i, j int) bool { return changes[i].date < changes[j].date })
	}
	return table, nil
}

func blankRow(sheet grate.Collection) bool {
	for _, kind := range sheet.Types() {
		if kind != "blank" {
			return false
		}
	}
	return true
}
