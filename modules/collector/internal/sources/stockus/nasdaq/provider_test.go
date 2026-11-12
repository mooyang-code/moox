package nasdaq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockus"
	"github.com/stretchr/testify/require"
)

// testdata/ndx.json 是 2026-11-12 实际请求 https://api.nasdaq.com/api/quote/list-type/nasdaq100 的原始返回：
// totalrecords 101，rows 101 行（谷歌 A、C 各一行）。
func recorded(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "ndx.json"))
	require.NoError(t, err)
	return body
}

// rewrite 改写真实返回：edit 可以改 totalrecords 与 rows，其余字段不动。
func rewrite(t *testing.T, edit func(data map[string]any, rows []any) []any) []byte {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorded(t), &payload))
	data := payload["data"].(map[string]any)
	inner := data["data"].(map[string]any)
	inner["rows"] = edit(data, inner["rows"].([]any))
	out, err := json.Marshal(payload)
	require.NoError(t, err)
	return out
}

// serve 起一个假服务，返回 provider；requests 记下每次请求。
func serve(t *testing.T, status int, body []byte, requests *[]*http.Request) *Provider {
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
	return New(Config{BaseURL: server.URL, Now: func() time.Time { return now }})
}

func TestDescriptorAndIndexCodes(t *testing.T) {
	provider := New(Config{})
	require.NoError(t, provider.Descriptor().Validate())
	require.Equal(t, "nasdaq/index_constituent_http", provider.Descriptor().SourceKey().String())
	require.Equal(t, []string{"NDX"}, provider.IndexCodes())
	// 只取指数成分：不是 K 线来源，也不是全部标的来源。
	var value marketdata.MarketProvider = provider
	_, isKline := value.(marketdata.KlineFetcher)
	_, isInstrument := value.(marketdata.InstrumentFetcher)
	require.False(t, isKline)
	require.False(t, isInstrument)
	var _ marketdata.IndexConstituentFetcher = provider
}

func TestFetchIndexConstituentsReadsRecordedNDX(t *testing.T) {
	var requests []*http.Request
	snapshot, err := serve(t, http.StatusOK, recorded(t), &requests).FetchIndexConstituents(context.Background(), "NDX")
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, "/api/quote/list-type/nasdaq100", requests[0].URL.Path)
	require.Equal(t, "Mozilla/5.0", requests[0].Header.Get("User-Agent"))
	require.Equal(t, "application/json", requests[0].Header.Get("Accept"))

	require.Len(t, snapshot.Instruments, 101)
	require.Equal(t, 1, snapshot.PageCount)
	require.True(t, snapshot.Complete)
	require.Equal(t, "stockus", snapshot.MarketID)
	require.Equal(t, "nasdaq", snapshot.SourceProvider)
	require.Equal(t, map[string]int{"XNAS": 101}, snapshot.ExchangeCounts)

	byID := map[string]marketdata.Instrument{}
	for _, item := range snapshot.Instruments {
		require.Equal(t, "XNAS", item.Exchange)
		require.Equal(t, "active", item.Status)
		require.NotEmpty(t, item.Name, item.SubjectID)
		// 标的 ID 能被美股规则解析，且往返一致。
		subject, err := stockus.Rules{}.ParseSubject(marketdata.InstrumentEquity, item.SubjectID)
		require.NoError(t, err, item.SubjectID)
		require.Equal(t, item.ProviderSymbol, subject.Code)
		require.Equal(t, "XNAS", subject.Exchange)
		require.Equal(t, subject.Code+"."+subject.Exchange, item.SubjectID)
		byID[item.SubjectID] = item
	}
	require.Len(t, byID, 101, "没有重复")
	require.Equal(t, "Apple Inc. Common Stock", byID["AAPL.XNAS"].Name)
	require.Contains(t, byID, "GOOGL.XNAS")
	require.Contains(t, byID, "GOOG.XNAS")
}

func TestFetchIndexConstituentsRejectsUnknownIndex(t *testing.T) {
	_, err := serve(t, http.StatusOK, recorded(t), nil).FetchIndexConstituents(context.Background(), "SPX")
	require.ErrorIs(t, err, marketdata.ErrInvalidRequest)
}

// 行数不等于接口自报的 totalrecords：少一行或多一行都报错。
func TestFetchIndexConstituentsRejectsCountMismatch(t *testing.T) {
	short := rewrite(t, func(_ map[string]any, rows []any) []any { return rows[:100] })
	_, err := serve(t, http.StatusOK, short, nil).FetchIndexConstituents(context.Background(), "NDX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "取到 100 行，接口报的总数是 101")

	long := rewrite(t, func(data map[string]any, rows []any) []any {
		data["totalrecords"] = 100
		return rows
	})
	_, err = serve(t, http.StatusOK, long, nil).FetchIndexConstituents(context.Background(), "NDX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "取到 101 行，接口报的总数是 100")
}

// 行数与 totalrecords 一致，但不在 95～110 之间：空列表、过少、过多都报错。
func TestFetchIndexConstituentsRejectsOutOfRangeRows(t *testing.T) {
	for name, edit := range map[string]func(map[string]any, []any) []any{
		"空列表": func(data map[string]any, _ []any) []any { data["totalrecords"] = 0; return []any{} },
		"过少":  func(data map[string]any, rows []any) []any { data["totalrecords"] = 94; return rows[:94] },
		"过多": func(data map[string]any, rows []any) []any {
			for i := 0; i < 10; i++ {
				rows = append(rows, map[string]any{"symbol": "X" + string(rune('A'+i)), "companyName": "多出来"})
			}
			data["totalrecords"] = len(rows)
			return rows
		},
	} {
		body := rewrite(t, edit)
		_, err := serve(t, http.StatusOK, body, nil).FetchIndexConstituents(context.Background(), "NDX")
		require.ErrorIs(t, err, marketdata.ErrProtocol, name)
		require.ErrorContains(t, err, "合理范围", name)
	}
}

func TestFetchIndexConstituentsRejectsDuplicate(t *testing.T) {
	body := rewrite(t, func(_ map[string]any, rows []any) []any {
		rows[5] = map[string]any{"symbol": "AAPL", "companyName": "Apple 又来一行"}
		return rows
	})
	_, err := serve(t, http.StatusOK, body, nil).FetchIndexConstituents(context.Background(), "NDX")
	require.ErrorIs(t, err, marketdata.ErrProtocol)
	require.ErrorContains(t, err, "AAPL.XNAS 重复出现")
}

// 代码不合法（小写、空、带斜杠、带空格、太长）：整份名单作废，错误里列出不合法的代码。
func TestFetchIndexConstituentsRejectsInvalidCode(t *testing.T) {
	for _, code := range []string{"aapl", "", "BRK/B", "A B", "TOOLONGCODE1", "1ABC", "A..B"} {
		body := rewrite(t, func(_ map[string]any, rows []any) []any {
			rows[3].(map[string]any)["symbol"] = code
			return rows
		})
		_, err := serve(t, http.StatusOK, body, nil).FetchIndexConstituents(context.Background(), "NDX")
		require.ErrorIs(t, err, marketdata.ErrProtocol, code)
		require.ErrorContains(t, err, "不合法的代码", code)
	}
}

func TestFetchIndexConstituentsMapsTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusTooManyRequests, "", marketdata.ErrRateLimited},
		{http.StatusInternalServerError, "", marketdata.ErrHTTPStatus},
		{http.StatusForbidden, "", marketdata.ErrHTTPStatus},
		{http.StatusOK, "<html>", marketdata.ErrProtocol},
		{http.StatusOK, `{"data":null,"message":null,"status":{"rCode":200}}`, marketdata.ErrProtocol},
		{http.StatusOK, `{"data":{"totalrecords":101,"data":{"rows":[]}},"status":{"rCode":400}}`, marketdata.ErrProtocol},
	} {
		_, err := serve(t, tc.status, []byte(tc.body), nil).FetchIndexConstituents(context.Background(), "NDX")
		require.ErrorIs(t, err, tc.want, "%d %s", tc.status, tc.body)
	}
	// 连不上：按超时类错误上报。
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	_, err := New(Config{BaseURL: server.URL}).FetchIndexConstituents(context.Background(), "NDX")
	require.ErrorIs(t, err, marketdata.ErrTimeout)
}

// 超时：服务端迟迟不返回，客户端按 RequestTimeout 放弃，报超时类错误。
func TestFetchIndexConstituentsTimesOut(t *testing.T) {
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
	_, err := New(Config{BaseURL: server.URL, RequestTimeout: 50 * time.Millisecond}).FetchIndexConstituents(context.Background(), "NDX")
	require.ErrorIs(t, err, marketdata.ErrTimeout)
	require.Less(t, time.Since(started), 5*time.Second)

	// 默认超时 30 秒。
	require.Equal(t, 30*time.Second, New(Config{}).client.Timeout)
}

func TestFetchIndexConstituentsStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := serve(t, http.StatusOK, recorded(t), nil).FetchIndexConstituents(ctx, "NDX")
	require.ErrorIs(t, err, marketdata.ErrTimeout)
	require.ErrorIs(t, err, context.Canceled)
}
