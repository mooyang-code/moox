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

func TestCodeConversion(t *testing.T) {
	cases := map[string]string{
		"600519.XSHG": "sh600519",
		"688981.XSHG": "sh688981",
		"000001.XSHE": "sz000001",
		"300750.XSHE": "sz300750",
		"920045.XBSE": "bj920045",
	}
	for instrumentID, symbol := range cases {
		got, err := TencentSymbol(instrumentID)
		if err != nil || got != symbol {
			t.Fatalf("TencentSymbol(%s) = %q, %v", instrumentID, got, err)
		}
		back, err := FromTencentSymbol(symbol)
		if err != nil || back != instrumentID {
			t.Fatalf("FromTencentSymbol(%s) = %q, %v", symbol, back, err)
		}
	}
	for _, bad := range []string{"600519", "600519.XSHE", "000001.XSHG", "12345.XSHE"} {
		if _, err := TencentSymbol(bad); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("TencentSymbol(%s) 应当报错，得到 %v", bad, err)
		}
	}
	if _, err := FromTencentSymbol("sz600519"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("前缀与代码不符应当报错：%v", err)
	}
}

func TestBoardAndLotRules(t *testing.T) {
	cases := map[string]LotRule{
		"600519.XSHG": {Minimum: 100, Step: 100},
		"000001.XSHE": {Minimum: 100, Step: 100},
		"300750.XSHE": {Minimum: 100, Step: 100},
		"688981.XSHG": {Minimum: 200, Step: 1},
		"920045.XBSE": {Minimum: 100, Step: 1},
	}
	for instrumentID, want := range cases {
		board, err := BoardOf(instrumentID)
		if err != nil {
			t.Fatal(err)
		}
		if got := LotRuleFor(board); got != want {
			t.Fatalf("%s 的数量规则 = %+v，期望 %+v", instrumentID, got, want)
		}
	}
}

func loadSample(t *testing.T, name string) map[string]Quote {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	quotes := ParseTencent(body)
	result := make(map[string]Quote, len(quotes))
	for _, quote := range quotes {
		result[quote.InstrumentID] = quote
	}
	return result
}

func cst(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", value, shanghai)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// 2026-11-08（周日）录的真实样例，行情时间是 11-06（周五）收盘后：主板、科创板、ST、停牌、退市各一只。
func TestParseTencentRecordedCloseSamples(t *testing.T) {
	quotes := loadSample(t, "tencent_close_20261106_a.txt")
	for instrumentID, quote := range loadSample(t, "tencent_close_20261106_b.txt") {
		quotes[instrumentID] = quote
	}
	main := quotes["600519.XSHG"]
	want := Quote{
		InstrumentID: "600519.XSHG", Last: "1453.00", PrevClose: "1452.00",
		Bid: "1452.00", Ask: "1453.00",
		LimitUp: "1597.20", LimitDown: "1306.80", Time: cst("2026-11-06 16:14:58"),
	}
	if main != want {
		t.Fatalf("主板样例 = %+v", main)
	}
	star := quotes["688981.XSHG"]
	if star.Last != "110.90" || star.LimitUp != "132.83" || star.LimitDown != "88.55" || star.Halted {
		t.Fatalf("科创板样例 = %+v", star)
	}
	// ST 股的涨跌停取快照里的绝对价（2026 年主板 ST 是 ±10%），不按名称推算。
	st := quotes["601718.XSHG"]
	if st.Last != "1.68" || st.PrevClose != "1.70" || st.LimitUp != "1.87" || st.LimitDown != "1.53" || st.Halted {
		t.Fatalf("ST 样例 = %+v", st)
	}
	// 停牌：状态字段是 S，现价等于昨收，买一卖一为空；收盘后行情时间仍在更新。
	halted := quotes["002129.XSHE"]
	if !halted.Halted || halted.Last != halted.PrevClose || halted.Bid != "" || halted.Ask != "" ||
		halted.LimitUp != "10.32" || !halted.Time.Equal(cst("2026-11-06 16:14:54")) {
		t.Fatalf("停牌样例 = %+v", halted)
	}
	// 退市：状态字段是 D，没有涨跌停（-1）。
	delisted := quotes["002505.XSHE"]
	if !delisted.Delisted || delisted.LimitUp != "" || delisted.LimitDown != "" {
		t.Fatalf("退市样例 = %+v", delisted)
	}
	// 北交所旧代码换号后，旧代码上的状态是 C，按退市处理。
	if renamed := quotes["430047.XBSE"]; !renamed.Delisted {
		t.Fatalf("旧代码换号样例 = %+v", renamed)
	}
	if beijing := quotes["920045.XBSE"]; beijing.Last != "298.00" || beijing.LimitUp != "432.26" {
		t.Fatalf("北交所样例 = %+v", beijing)
	}
}

// GBK 双字节字符的第二个字节可以是 0x7E（~），按字节切分不能把它当分隔符。
func TestParseTencentSkipsGBKTrailByteTilde(t *testing.T) {
	fields := make([]string, tencentMinFields)
	for i := range fields {
		fields[i] = "0"
	}
	fields[tencentFieldCode], fields[tencentFieldLast], fields[tencentFieldTime] = "600000", "10.00", "20261106150000"
	fields[1] = "\x81\x7e\xb9\xf3" // 两个 GBK 双字节字符，第一个的尾字节是 ~
	body := []byte(`v_sh600000="` + strings.Join(fields, "~") + `";` + "\n" + `v_pv_none_match="1";`)
	quotes := ParseTencent(body)
	if len(quotes) != 1 || quotes[0].Last != "10.00" || !quotes[0].Time.Equal(cst("2026-11-06 15:00:00")) {
		t.Fatalf("解析结果 = %+v", quotes)
	}
}

func TestTencentSnapshotBatchesRequests(t *testing.T) {
	sample, err := os.ReadFile("testdata/tencent_close_20261106_a.txt")
	if err != nil {
		t.Fatal(err)
	}
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		_, _ = w.Write(sample)
	}))
	defer server.Close()
	ids := make([]string, 0, 61)
	for i := range 61 {
		ids = append(ids, fmt.Sprintf("6%05d.XSHG", i))
	}
	quotes, err := TencentClient{BaseURL: server.URL}.Snapshot(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || strings.Count(requests[0], ",") != TencentBatchLimit-1 {
		t.Fatalf("请求 = %v", requests)
	}
	if _, ok := quotes["600519.XSHG"]; !ok {
		t.Fatalf("快照 = %v", quotes)
	}
}

func TestTencentSnapshotMapsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := TencentClient{BaseURL: server.URL}.Snapshot(context.Background(), []string{"600519.XSHG"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchClistPaginatesAndFallsBack(t *testing.T) {
	pages := map[string]string{
		"1": `{"data":{"total":3,"diff":[{"f12":"600519","f13":1,"f14":"贵州茅台"},{"f12":"688981","f13":1,"f14":"中芯国际"}]}}`,
		"2": `{"data":{"total":3,"diff":[{"f12":"920045","f13":0,"f14":"蘅东光"}]}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fs") != EastMoneyAllAShareFilter {
			t.Errorf("fs = %q", r.URL.Query().Get("fs"))
		}
		_, _ = w.Write([]byte(pages[r.URL.Query().Get("pn")]))
	}))
	defer server.Close()
	var codes []string
	result, err := FetchClist(context.Background(), ClistQuery{BaseURL: server.URL, Filter: EastMoneyAllAShareFilter, PageSize: 2}, func(item ClistItem) error {
		codes = append(codes, item.Code)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Pages != 2 || strings.Join(codes, ",") != "600519,688981,920045" {
		t.Fatalf("result = %+v codes = %v", result, codes)
	}
	if bases := clistBases(EastMoneyPushBase); len(bases) != 2 || bases[1] != EastMoneyPushFallbackBase {
		t.Fatalf("主地址是 push2 时要带上备用地址：%v", bases)
	}
}

func TestFetchClistRejectsTotalMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pn") == "1" {
			_, _ = w.Write([]byte(`{"data":{"total":3,"diff":[{"f12":"600519"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"total":3,"diff":[]}}`))
	}))
	defer server.Close()
	_, err := FetchClist(context.Background(), ClistQuery{BaseURL: server.URL, PageSize: 1}, func(ClistItem) error { return nil })
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v", err)
	}
}

// 一只的行格式不对只跳过这一只，其他照常返回。
func TestParseTencentSkipsBadLine(t *testing.T) {
	sample, err := os.ReadFile("testdata/tencent_close_20261106_a.txt")
	if err != nil {
		t.Fatal(err)
	}
	body := append([]byte(`v_sh600000="1~浦发~600000~坏~";`+"\n"), sample...)
	quotes := ParseTencent(body)
	if len(quotes) != 5 {
		t.Fatalf("应跳过坏行、保留样例里的 5 只：%d", len(quotes))
	}
}
