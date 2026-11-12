package tencent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// testdata 里是 2026-11-12 实际请求
// proxy.finance.qq.com/cgi/cgi-bin/rank/hk/getList?board_type=hkHSI&sort_type=price&direct=down 的原始返回：
//   - hsi_offset_<n>.json：带 offset=<n>&count=20，total=95，五页共 95 只，offset=100 是一页空结果；
//   - hsi_count_100_offset_0.json：offset=0&count=100，只返回前 20 条并把 total 报成 20；
//   - hsi_no_count_offset_20.json：offset=20、不带 count，返回空列表并把 total 报成 20。
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return body
}

// recordedPages 是按 offset 索引的五页真实返回。
func recordedPages(t *testing.T) map[string][]byte {
	pages := map[string][]byte{}
	for _, offset := range []string{"0", "20", "40", "60", "80"} {
		pages[offset] = fixture(t, "hsi_offset_"+offset+".json")
	}
	return pages
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// newProvider 构造一个不出网的数据源：请求按 offset 返回 pages 里的内容；requests 记下每次请求的 offset。
func newProvider(t *testing.T, pages map[string][]byte, requests *[]string) *Provider {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		require.Equal(t, "proxy.finance.qq.com", r.URL.Host)
		require.Equal(t, "/cgi/cgi-bin/rank/hk/getList", r.URL.Path)
		require.Equal(t, "hkHSI", query.Get("board_type"))
		require.Equal(t, "20", query.Get("count"), "必须同时带 offset 和 count=20 才能翻页")
		require.True(t, query.Has("offset"))
		if requests != nil {
			*requests = append(*requests, query.Get("offset"))
		}
		body, ok := pages[query.Get("offset")]
		if !ok {
			http.Error(w, "unexpected offset", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Result(), nil
	})}
	now := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	return New(Config{HTTPClient: client, Now: func() time.Time { return now }})
}

func TestDescriptorAndIndexCodes(t *testing.T) {
	provider := New(Config{})
	require.NoError(t, provider.Descriptor().Validate())
	require.Equal(t, "tencent/index_constituent_http", provider.Descriptor().SourceKey().String())
	require.Equal(t, []string{"HSI"}, provider.IndexCodes())
	// 只取指数成分：不是 K 线来源，也不是全部标的来源。
	var value marketdata.MarketProvider = provider
	_, isKline := value.(marketdata.KlineFetcher)
	_, isInstrument := value.(marketdata.InstrumentFetcher)
	require.False(t, isKline)
	require.False(t, isInstrument)
	var _ marketdata.IndexConstituentFetcher = provider
}

func TestFetchIndexConstituentsReadsRecordedHSI(t *testing.T) {
	var requests []string
	provider := newProvider(t, recordedPages(t), &requests)
	snapshot, err := provider.FetchIndexConstituents(context.Background(), "HSI")
	require.NoError(t, err)
	require.Equal(t, []string{"0", "20", "40", "60", "80"}, requests, "取够 total 就停，不多请求一页")
	require.Len(t, snapshot.Instruments, 95)
	require.Equal(t, 5, snapshot.PageCount)
	require.True(t, snapshot.Complete)
	require.Equal(t, "stockhk", snapshot.MarketID)
	require.Equal(t, "tencent", snapshot.SourceProvider)
	require.Equal(t, map[string]int{"XHKG": 95}, snapshot.ExchangeCounts)

	byID := map[string]marketdata.Instrument{}
	for _, item := range snapshot.Instruments {
		require.Len(t, item.SubjectID, len("00700.XHKG"), item.SubjectID)
		require.True(t, strings.HasSuffix(item.SubjectID, ".XHKG"), item.SubjectID)
		require.Equal(t, strings.TrimSuffix(item.SubjectID, ".XHKG"), item.ProviderSymbol)
		require.Equal(t, "XHKG", item.Exchange)
		require.NotEmpty(t, item.Name, item.SubjectID)
		require.Equal(t, "active", item.Status)
		byID[item.SubjectID] = item
	}
	require.Len(t, byID, 95, "没有重复")
	// hk00700 换成 00700.XHKG，带上名称；领展房产基金的 stock_type 是 GP-FUND-REITS，也是成分。
	require.Equal(t, "腾讯控股", byID["00700.XHKG"].Name)
	require.Equal(t, "00700", byID["00700.XHKG"].ProviderSymbol)
	require.Equal(t, "香港交易所", byID["00388.XHKG"].Name)
	require.Equal(t, "领展房产基金", byID["00823.XHKG"].Name)
}

func TestFetchIndexConstituentsRejectsUnknownIndex(t *testing.T) {
	provider := newProvider(t, nil, nil)
	_, err := provider.FetchIndexConstituents(context.Background(), "000300")
	require.ErrorIs(t, err, marketdata.ErrInvalidRequest)
}

// 翻页参数没生效时接口只返回前 20 条并把 total 报成 20：不能把这 20 只当成完整名单。
func TestFetchIndexConstituentsRejectsTotalOfOnePage(t *testing.T) {
	var requests []string
	provider := newProvider(t, map[string][]byte{"0": fixture(t, "hsi_count_100_offset_0.json")}, &requests)
	_, err := provider.FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "翻页参数没有生效")
	require.Equal(t, []string{"0"}, requests)
}

// 后面某一页按“不带 count”的方式返回（空列表、total 报成 20）：总数前后不一致，报错。
func TestFetchIndexConstituentsRejectsTotalChangedToOnePage(t *testing.T) {
	pages := recordedPages(t)
	pages["20"] = fixture(t, "hsi_no_count_offset_20.json")
	_, err := newProvider(t, pages, nil).FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "从 95 变成 20")
}

// 还没取够 total 时某一页为空（用 offset=100 那页真实的空返回）：报错，不继续也不返回残缺名单。
func TestFetchIndexConstituentsRejectsEmptyPage(t *testing.T) {
	pages := recordedPages(t)
	pages["40"] = fixture(t, "hsi_offset_100.json")
	var requests []string
	_, err := newProvider(t, pages, &requests).FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "offset=40 这一页为空")
	require.Equal(t, []string{"0", "20", "40"}, requests)
}

// rewritePage 改写一页真实返回里的成分行，其余字段不动。
func rewritePage(t *testing.T, body []byte, edit func(rows []map[string]any) []map[string]any) []byte {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	data := payload["data"].(map[string]any)
	var rows []map[string]any
	for _, row := range data["rank_list"].([]any) {
		rows = append(rows, row.(map[string]any))
	}
	data["rank_list"] = edit(rows)
	out, err := json.Marshal(payload)
	require.NoError(t, err)
	return out
}

// 取到的条数不等于接口报的 total：最后一页少一只（94 对 95）报错；多出一只（96 对 95）也报错。
func TestFetchIndexConstituentsRejectsCountMismatch(t *testing.T) {
	short := recordedPages(t)
	short["80"] = rewritePage(t, short["80"], func(rows []map[string]any) []map[string]any { return rows[:len(rows)-1] })
	short["100"] = fixture(t, "hsi_offset_100.json")
	var requests []string
	_, err := newProvider(t, short, &requests).FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "已取到 94 只，接口报的总数是 95")
	require.Equal(t, []string{"0", "20", "40", "60", "80", "100"}, requests)

	long := recordedPages(t)
	long["80"] = rewritePage(t, long["80"], func(rows []map[string]any) []map[string]any {
		return append(rows, map[string]any{"code": "hk08888", "name": "多出来的一只"})
	})
	_, err = newProvider(t, long, nil).FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "取到 96 只，接口报的总数是 95")
}

// 有重复：第二页又返回了第一页的一只（翻页期间排序变了就会这样），报错。
func TestFetchIndexConstituentsRejectsDuplicate(t *testing.T) {
	pages := recordedPages(t)
	pages["20"] = rewritePage(t, pages["20"], func(rows []map[string]any) []map[string]any {
		rows[0] = map[string]any{"code": "hk00700", "name": "腾讯控股"}
		return rows
	})
	_, err := newProvider(t, pages, nil).FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "00700.XHKG 重复出现")
}

// 成分代码不是 hk 加五位数字：整份名单作废。
func TestFetchIndexConstituentsRejectsMalformedCode(t *testing.T) {
	for _, code := range []string{"00700", "hk0700", "hk000700", "sh600000", "hk0070A", ""} {
		pages := recordedPages(t)
		pages["0"] = rewritePage(t, pages["0"], func(rows []map[string]any) []map[string]any {
			rows[3]["code"] = code
			return rows
		})
		_, err := newProvider(t, pages, nil).FetchIndexConstituents(context.Background(), "HSI")
		require.ErrorIs(t, err, marketdata.ErrProtocol, code)
	}
}

func TestFetchIndexConstituentsMapsTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusTooManyRequests, "", marketdata.ErrRateLimited},
		{http.StatusBadGateway, "", marketdata.ErrHTTPStatus},
		{http.StatusOK, "<html>", marketdata.ErrProtocol},
		{http.StatusOK, `{"code":-1,"msg":"系统繁忙"}`, marketdata.ErrProtocol},
		{http.StatusOK, `{"code":0,"msg":"ok","data":{"rank_list":[]}}`, marketdata.ErrProtocol},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := New(Config{BaseURL: server.URL}).FetchIndexConstituents(context.Background(), "HSI")
		server.Close()
		require.ErrorIs(t, err, tc.want, "%d %s", tc.status, tc.body)
	}
	// 连不上：按超时类错误上报。
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	_, err := New(Config{BaseURL: server.URL}).FetchIndexConstituents(context.Background(), "HSI")
	require.ErrorIs(t, err, marketdata.ErrTimeout)
}
