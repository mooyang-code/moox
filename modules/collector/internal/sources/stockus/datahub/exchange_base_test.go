package datahub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 默认的交易所对照连 Config.ExchangeListBaseURL（测试用假服务），不是写死的东方财富主地址。
func TestDefaultExchangeLookupUsesConfiguredListBaseURL(t *testing.T) {
	all := records(t)[1:]
	var codes []string
	for _, record := range all {
		codes = append(codes, record[0])
	}
	// 105 放全部成分代码，106、107 各放一个无关代码；每页最多 100 行。
	lists := map[string][]string{"m:105": codes, "m:106": {"ZZZ1"}, "m:107": {"ZZZ2"}}
	var requests atomic.Int32
	clist := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		list := lists[r.URL.Query().Get("fs")]
		page, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		start := (page - 1) * 100
		var rows []map[string]any
		if start < len(list) {
			for _, code := range list[start:min(start+100, len(list))] {
				rows = append(rows, map[string]any{"f12": code, "f13": 105, "f14": code})
			}
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"total": len(list), "diff": rows}})
		_, _ = w.Write(body)
	}))
	defer clist.Close()
	csvServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(recorded(t))
	}))
	defer csvServer.Close()

	now := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	provider := New(Config{BaseURL: csvServer.URL, ExchangeListBaseURL: clist.URL, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchIndexConstituents(context.Background(), "SPX")
	require.NoError(t, err)
	require.Len(t, snapshot.Instruments, 503)
	require.Positive(t, requests.Load(), "交易所对照请求了配置的列表地址")
}
