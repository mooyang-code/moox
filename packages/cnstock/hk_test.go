package cnstock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHKSymbolRoundTrip(t *testing.T) {
	symbol, err := HKTencentSymbol(" 00700.xhkg ")
	if err != nil || symbol != "r_hk00700" {
		t.Fatalf("HKTencentSymbol = %q, %v", symbol, err)
	}
	if code, err := HKCode("00005.XHKG"); err != nil || code != "00005" {
		t.Fatalf("HKCode = %q, %v", code, err)
	}
	for _, bad := range []string{"700.XHKG", "00700.XSHG", "00700", "0070A.XHKG"} {
		if _, err := HKTencentSymbol(bad); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("HKTencentSymbol(%q) error = %v", bad, err)
		}
	}
	if id, err := HKInstrumentID("09988"); err != nil || id != "09988.XHKG" {
		t.Fatalf("HKInstrumentID = %q, %v", id, err)
	}
}

// 2026-11-17 收市后的真实响应：每手股数逐只不同（00700 是 100、00005 是 400、02800 是 500、01810 是 200、00001 是 500），
// 停牌的 00204、00232 状态是 S，长期停牌的 00232 行情时间停在最后一个交易日。
func TestParseTencentHKSample(t *testing.T) {
	body, err := os.ReadFile("testdata/tencent_hk_20261117.txt")
	if err != nil {
		t.Fatal(err)
	}
	quotes := map[string]Quote{}
	for _, quote := range ParseTencentHK(body) {
		quotes[quote.InstrumentID] = quote
	}
	if len(quotes) != 8 {
		t.Fatalf("样例里有 8 只：%d", len(quotes))
	}
	for id, lot := range map[string]int64{"00700.XHKG": 100, "00005.XHKG": 400, "09988.XHKG": 100, "02800.XHKG": 500, "01810.XHKG": 200, "00001.XHKG": 500, "00204.XHKG": 5000, "00232.XHKG": 2000} {
		if quotes[id].LotSize != lot {
			t.Fatalf("%s 每手股数 = %d，期望 %d", id, quotes[id].LotSize, lot)
		}
	}
	tencent := quotes["00700.XHKG"]
	if tencent.Last != "376.000" || tencent.PrevClose != "390.400" || tencent.Bid != "376.000" || tencent.Ask != "376.000" || tencent.Halted || tencent.Delisted {
		t.Fatalf("00700 = %+v", tencent)
	}
	if want := time.Date(2026, 11, 17, 8, 8, 19, 0, time.UTC); !tencent.Time.Equal(want) {
		t.Fatalf("00700 行情时间 = %s，期望 %s（香港时间 16:08:19）", tencent.Time, want)
	}
	if tencent.LimitUp != "" || tencent.LimitDown != "" {
		t.Fatalf("港股没有涨跌停价：%+v", tencent)
	}
	if !quotes["00204.XHKG"].Halted || !quotes["00232.XHKG"].Halted {
		t.Fatalf("00204、00232 停牌：%+v %+v", quotes["00204.XHKG"], quotes["00232.XHKG"])
	}
	if want := time.Date(2026, 10, 12, 8, 8, 24, 0, time.UTC); !quotes["00232.XHKG"].Time.Equal(want) {
		t.Fatalf("长期停牌的行情时间停在最后一个交易日：%s", quotes["00232.XHKG"].Time)
	}
}

func TestParseTencentHKSkipsBadLines(t *testing.T) {
	fields := make([]string, 78)
	for i := range fields {
		fields[i] = "0"
	}
	fields[1], fields[2], fields[3], fields[4], fields[9], fields[19] = "X", "00700", "376.000", "390.400", "375.800", "376.000"
	fields[30], fields[40], fields[60] = "2026/11/17 10:00:00", "", "abc"
	good := `v_r_hk00700="` + strings.Join(fields, "~") + `";`
	short := `v_r_hk00005="100~X~00005~1";`
	wrongPrefix := `v_sh600519="` + strings.Join(fields, "~") + `";`
	quotes := ParseTencentHK([]byte(good + short + wrongPrefix + `v_pv_none_match="1";`))
	if len(quotes) != 1 || quotes[0].InstrumentID != "00700.XHKG" || quotes[0].Bid != "375.800" {
		t.Fatalf("只应解析出 00700：%+v", quotes)
	}
	if quotes[0].LotSize != 0 {
		t.Fatalf("每手股数读不出来时为 0，不猜：%d", quotes[0].LotSize)
	}
}

func TestSnapshotHKUsesRealtimePrefix(t *testing.T) {
	body, err := os.ReadFile("testdata/tencent_hk_20261117.txt")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	quotes, err := TencentClient{BaseURL: server.URL}.SnapshotHK(context.Background(), []string{"00700.XHKG", "00005.XHKG"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/q=r_hk00700,r_hk00005" {
		t.Fatalf("要用带 r_ 的实时代码：%v", paths)
	}
	if quotes["00700.XHKG"].LotSize != 100 {
		t.Fatalf("快照 = %+v", quotes["00700.XHKG"])
	}
}

func TestHSIConstituentsPagesUntilTotal(t *testing.T) {
	const total = 45
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("board_type") != "hkHSI" || query.Get("count") != "20" {
			t.Errorf("请求参数不对：%s", r.URL.RawQuery)
		}
		var offset int
		_, _ = fmt.Sscan(query.Get("offset"), &offset)
		var items []string
		for i := offset; i < min(offset+20, total); i++ {
			items = append(items, fmt.Sprintf(`{"code":"hk%05d","name":"x"}`, i+1))
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"rank_list":[%s],"total":%d}}`, strings.Join(items, ","), total)
	}))
	defer server.Close()
	ids, err := HSIConstituents(context.Background(), nil, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != total || ids[0] != "00001.XHKG" || ids[total-1] != "00045.XHKG" {
		t.Fatalf("成分 = %d 只，首尾 %s、%s", len(ids), ids[0], ids[len(ids)-1])
	}

	single := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"rank_list":[{"code":"hk00700"}],"total":20}}`))
	}))
	defer single.Close()
	if _, err := HSIConstituents(context.Background(), nil, single.URL); !errors.Is(err, ErrProtocol) {
		t.Fatalf("总数不超过一页说明翻页没生效：%v", err)
	}
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }))
	defer limited.Close()
	if _, err := HSIConstituents(context.Background(), nil, limited.URL); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("429 是限流：%v", err)
	}
}
