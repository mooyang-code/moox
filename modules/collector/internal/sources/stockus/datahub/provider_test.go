package datahub

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockus"
	"github.com/stretchr/testify/require"
)

// testdata/sp500.csv 是 2026-11-12 取到的 datasets/s-and-p-500-companies 的 constituents.csv 原文：503 行加表头。
func recorded(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "sp500.csv"))
	require.NoError(t, err)
	return body
}

// records 把真实 CSV 读成二维表（含表头）。
func records(t *testing.T) [][]string {
	t.Helper()
	all, err := csv.NewReader(bytes.NewReader(recorded(t))).ReadAll()
	require.NoError(t, err)
	return all
}

func encode(t *testing.T, all [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, csv.NewWriter(&buf).WriteAll(all))
	return buf.Bytes()
}

// stubExchanges 是假的交易所对照：带点的代码和 C 开头的代码记为纽交所，B 开头的记为 107，其余纳斯达克；
// skip 里的代码对不上；记下收到的代码。
type stubExchanges struct {
	mu    sync.Mutex
	skip  map[string]bool
	err   error
	calls [][]string
}

func (s *stubExchanges) lookup(_ context.Context, codes []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string(nil), codes...))
	if s.err != nil {
		return nil, s.err
	}
	result := map[string]string{}
	for _, code := range codes {
		switch {
		case s.skip[code]:
		case strings.Contains(code, ".") || strings.HasPrefix(code, "C"):
			result[code] = "XNYS"
		case strings.HasPrefix(code, "B"):
			result[code] = "XASE"
		default:
			result[code] = "XNAS"
		}
	}
	return result, nil
}

func serve(t *testing.T, status int, body []byte, stub *stubExchanges, requests *[]*http.Request) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests = append(*requests, r.Clone(context.Background()))
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	now := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	cfg := Config{BaseURL: server.URL, Now: func() time.Time { return now }}
	if stub != nil {
		cfg.Exchanges = stub.lookup
	}
	return New(cfg)
}

func TestDescriptorAndIndexCodes(t *testing.T) {
	provider := New(Config{})
	require.NoError(t, provider.Descriptor().Validate())
	require.Equal(t, "datahub/index_constituent_http", provider.Descriptor().SourceKey().String())
	require.Equal(t, []string{"SPX"}, provider.IndexCodes())
	require.NotNil(t, provider.exchanges, "默认接东方财富的交易所对照")
	require.Equal(t, 30*time.Second, provider.client.Timeout)
	// 只取指数成分：不是 K 线来源，也不是全部标的来源。
	var value marketdata.MarketProvider = provider
	_, isKline := value.(marketdata.KlineFetcher)
	_, isInstrument := value.(marketdata.InstrumentFetcher)
	require.False(t, isKline)
	require.False(t, isInstrument)
	var _ marketdata.IndexConstituentFetcher = provider
}

func TestFetchIndexConstituentsReadsRecordedSP500(t *testing.T) {
	stub := &stubExchanges{}
	var requests []*http.Request
	snapshot, err := serve(t, http.StatusOK, recorded(t), stub, &requests).FetchIndexConstituents(context.Background(), "SPX")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, "/gh/datasets/s-and-p-500-companies@main/data/constituents.csv", requests[0].URL.Path)

	require.Len(t, snapshot.Instruments, 503)
	require.Equal(t, 1, snapshot.PageCount)
	require.True(t, snapshot.Complete)
	require.Equal(t, "stockus", snapshot.MarketID)
	require.Equal(t, "datahub", snapshot.SourceProvider)
	total := 0
	for _, count := range snapshot.ExchangeCounts {
		total += count
	}
	require.Equal(t, 503, total)

	// 对照函数收到全部 503 个代码，只调一次。
	require.Len(t, stub.calls, 1)
	require.Len(t, stub.calls[0], 503)

	byID := map[string]marketdata.Instrument{}
	for _, item := range snapshot.Instruments {
		// 每个标的 ID 能被美股规则解析，往返一致，数据源代码保留点。
		subject, err := stockus.Rules{}.ParseSubject(marketdata.InstrumentEquity, item.SubjectID)
		require.NoError(t, err, item.SubjectID)
		require.Equal(t, subject.Code+"."+subject.Exchange, item.SubjectID)
		require.Equal(t, subject.Code, item.ProviderSymbol)
		require.Equal(t, subject.Exchange, item.Exchange)
		require.NotEmpty(t, item.Name, item.SubjectID)
		require.Equal(t, "active", item.Status)
		byID[item.SubjectID] = item
	}
	require.Len(t, byID, 503, "没有重复")
	require.Equal(t, "Berkshire Hathaway", byID["BRK.B.XNYS"].Name)
	require.Equal(t, "BRK.B", byID["BRK.B.XNYS"].ProviderSymbol)
	require.Equal(t, "Brown–Forman", byID["BF.B.XNYS"].Name)
	require.Equal(t, "3M", byID["MMM.XNAS"].Name, "交易所取自对照函数")
}

func TestFetchIndexConstituentsRejectsUnknownIndex(t *testing.T) {
	_, err := serve(t, http.StatusOK, recorded(t), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "NDX")
	require.ErrorIs(t, err, marketdata.ErrInvalidRequest)
}

// 行数范围：494、516 行报错；495、515 行通过；报错时不去对交易所。
func TestFetchIndexConstituentsChecksRowCount(t *testing.T) {
	all := records(t)
	header, rows := all[0], all[1:]
	resize := func(n int) [][]string {
		out := [][]string{header}
		out = append(out, rows[:min(n, len(rows))]...)
		for i := len(rows); i < n; i++ {
			out = append(out, []string{fmt.Sprintf("XX%d", i), "补充行", "", "", "", "", "", ""})
		}
		return out
	}
	for _, n := range []int{494, 516, 0} {
		stub := &stubExchanges{}
		_, err := serve(t, http.StatusOK, encode(t, resize(n)), stub, nil).FetchIndexConstituents(context.Background(), "SPX")
		require.ErrorIs(t, err, marketdata.ErrProtocol, n)
		require.ErrorContains(t, err, "合理范围", n)
		require.Empty(t, stub.calls, "名单不合格时不用去查东方财富")
	}
	for _, n := range []int{495, 515} {
		snapshot, err := serve(t, http.StatusOK, encode(t, resize(n)), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
		require.NoError(t, err, n)
		require.Len(t, snapshot.Instruments, n)
	}
}

func TestFetchIndexConstituentsRejectsDuplicateAndBadCodes(t *testing.T) {
	all := records(t)
	edit := func(row int, code string) []byte {
		out := make([][]string, len(all))
		for i, record := range all {
			out[i] = append([]string(nil), record...)
		}
		out[row][0] = code
		return encode(t, out)
	}
	stub := &stubExchanges{}
	_, err := serve(t, http.StatusOK, edit(10, all[1][0]), stub, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, all[1][0]+" 重复出现")
	require.Empty(t, stub.calls)

	_, err = serve(t, http.StatusOK, edit(10, ""), stub, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "代码为空")

	for _, code := range []string{"aapl", "BRK/B", "A B", "TOOLONGCODE1", "1ABC", "A..B"} {
		_, err = serve(t, http.StatusOK, edit(10, code), stub, nil).FetchIndexConstituents(context.Background(), "SPX")
		require.ErrorIs(t, err, marketdata.ErrProtocol, code)
		require.ErrorContains(t, err, "不合法的代码", code)
	}
	require.Empty(t, stub.calls)
}

// 列缺失：表头里没有 Symbol 或 Security 列，或某行列数不够。按表头名找列，列换了位置照样能读。
func TestFetchIndexConstituentsChecksColumns(t *testing.T) {
	all := records(t)
	drop := func(column string) []byte {
		var out [][]string
		for _, record := range all {
			var kept []string
			for i, value := range record {
				if all[0][i] != column {
					kept = append(kept, value)
				}
			}
			out = append(out, kept)
		}
		return encode(t, out)
	}
	for _, column := range []string{"Symbol", "Security"} {
		_, err := serve(t, http.StatusOK, drop(column), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
		require.ErrorIs(t, err, marketdata.ErrProtocol, column)
		require.ErrorContains(t, err, "表头缺少", column)
	}

	// 某行只有一列。
	short := make([][]string, len(all))
	copy(short, all)
	short[20] = []string{"ZZZ"}
	_, err := serve(t, http.StatusOK, encode(t, short), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "第 21 行列数不够")

	// 列换了位置（Security 在前、Symbol 在后）照样能读。
	swapped := make([][]string, len(all))
	for i, record := range all {
		swapped[i] = append([]string{record[1], record[0]}, record[2:]...)
	}
	snapshot, err := serve(t, http.StatusOK, encode(t, swapped), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.NoError(t, err)
	require.Len(t, snapshot.Instruments, 503)

	// 带 BOM 的 CSV 也能读。
	snapshot, err = serve(t, http.StatusOK, append([]byte("\ufeff"), recorded(t)...), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.NoError(t, err)
	require.Len(t, snapshot.Instruments, 503)

	// 不是 CSV。
	_, err = serve(t, http.StatusOK, []byte("<html>blocked</html>"), &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	_, err = serve(t, http.StatusOK, nil, &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
}

// 任何一只对不上交易所：整次失败，错误里列出全部对不上的代码（不是只列第一个）。
func TestFetchIndexConstituentsFailsWhenAnyExchangeUnmatched(t *testing.T) {
	all := records(t)
	missing := []string{all[3][0], all[100][0], all[400][0]}
	stub := &stubExchanges{skip: map[string]bool{missing[0]: true, missing[1]: true, missing[2]: true}}
	_, err := serve(t, http.StatusOK, recorded(t), stub, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "3 只成分")
	require.ErrorContains(t, err, strings.Join(missing, "、"), "按 CSV 顺序列出全部对不上的代码")
}

// 交易所对照本身出错（页失败、超时等）：整次失败，错误链保留原因。
func TestFetchIndexConstituentsPropagatesExchangeLookupError(t *testing.T) {
	cause := errors.New("东方财富不可用")
	stub := &stubExchanges{err: cause}
	_, err := serve(t, http.StatusOK, recorded(t), stub, nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, cause)
	require.ErrorContains(t, err, "对交易所")
}

// 对照函数给出不是三个 MIC 之一的交易所：拼出的标的 ID 过不了美股规则，整次失败。
func TestFetchIndexConstituentsRejectsInvalidExchange(t *testing.T) {
	lookup := func(_ context.Context, codes []string) (map[string]string, error) {
		result := map[string]string{}
		for _, code := range codes {
			result[code] = "XNAS"
		}
		result[codes[7]] = "XHKG"
		return result, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(recorded(t)) }))
	defer server.Close()
	_, err := New(Config{BaseURL: server.URL, Exchanges: lookup}).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "标的 ID 不合法")
	require.ErrorContains(t, err, ".XHKG")
}

func TestFetchIndexConstituentsMapsTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusTooManyRequests, marketdata.ErrRateLimited},
		{http.StatusInternalServerError, marketdata.ErrHTTPStatus},
		{http.StatusNotFound, marketdata.ErrHTTPStatus},
	} {
		_, err := serve(t, tc.status, nil, &stubExchanges{}, nil).FetchIndexConstituents(context.Background(), "SPX")
		require.ErrorIs(t, err, tc.want, "%d", tc.status)
	}
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	_, err := New(Config{BaseURL: server.URL, Exchanges: (&stubExchanges{}).lookup}).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrTimeout)
}

func TestFetchIndexConstituentsTimesOutAndStopsOnCancel(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	started := time.Now()
	_, err := New(Config{BaseURL: server.URL, RequestTimeout: 50 * time.Millisecond, Exchanges: (&stubExchanges{}).lookup}).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrTimeout)
	require.Less(t, time.Since(started), 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = serve(t, http.StatusOK, recorded(t), &stubExchanges{}, nil).FetchIndexConstituents(ctx, "SPX")
	require.ErrorIs(t, err, context.Canceled)
}
