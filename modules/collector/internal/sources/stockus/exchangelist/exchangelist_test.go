package exchangelist

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClist 是假的东方财富 clist 服务：每个市场（fs=m:105 等）一张按代码排序的列表，每页最多 100 行，带 total。
type fakeClist struct {
	mu       sync.Mutex
	lists    map[string][]string // fs → 排序后的代码
	requests []string            // 每次请求："m:105#3"（市场#页号）
	stamps   []time.Time
	attempts map[string]int // "m:105#3" → 该页已被请求的次数
	// fail 返回非 0 时，这次请求直接返回该 HTTP 状态码。
	fail func(fs string, page, attempt int) int
	// totalOf 可以改写自报的 total（默认等于列表长度）。
	totalOf func(fs string, real int) int
	// onRequest 在每次请求时调用（持锁之外）。
	onRequest func(fs string, page int)
}

func newFakeClist(lists map[string][]string) *fakeClist {
	sorted := map[string][]string{}
	for fs, codes := range lists {
		cp := append([]string(nil), codes...)
		sort.Strings(cp)
		sorted[fs] = cp
	}
	return &fakeClist{lists: sorted, attempts: map[string]int{}}
}

func (f *fakeClist) pages(fs string) int { return (len(f.lists[fs]) + 99) / 100 }

func (f *fakeClist) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeClist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	fs := query.Get("fs")
	page, _ := strconv.Atoi(query.Get("pn"))
	key := fs + "#" + strconv.Itoa(page)
	f.mu.Lock()
	f.requests = append(f.requests, key)
	f.stamps = append(f.stamps, time.Now())
	f.attempts[key]++
	attempt := f.attempts[key]
	f.mu.Unlock()
	if f.onRequest != nil {
		f.onRequest(fs, page)
	}
	if query.Get("pz") != "100" || r.URL.Path != "/api/qt/clist/get" {
		http.Error(w, "unexpected request", http.StatusBadRequest)
		return
	}
	if f.fail != nil {
		if status := f.fail(fs, page, attempt); status != 0 {
			w.WriteHeader(status)
			return
		}
	}
	codes := f.lists[fs]
	total := len(codes)
	if f.totalOf != nil {
		total = f.totalOf(fs, total)
	}
	start := (page - 1) * 100
	end := min(start+100, len(codes))
	var rows []map[string]any
	if start < len(codes) {
		market := 0
		switch fs {
		case "m:105":
			market = 105
		case "m:106":
			market = 106
		case "m:107":
			market = 107
		}
		for _, code := range codes[start:end] {
			rows = append(rows, map[string]any{"f12": code, "f13": market, "f14": code + " name"})
		}
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"total": total, "diff": rows}})
	_, _ = w.Write(body)
}

// testConfig 是不等待的配置：页间隔取消，重试等待压到 1 毫秒。
func testConfig(url string) Config {
	return Config{BaseURL: url, PageInterval: -1, RetryBackoff: time.Millisecond}
}

func readFile(t *testing.T, path ...string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(path...))
	require.NoError(t, err)
	return body
}

// sp500Codes 与 ndxCodes 读取相邻两个包录下的真实返回（2026-11-12）。
func sp500Codes(t *testing.T) []string {
	records, err := csv.NewReader(strings.NewReader(string(readFile(t, "..", "datahub", "testdata", "sp500.csv")))).ReadAll()
	require.NoError(t, err)
	var codes []string
	for _, record := range records[1:] {
		codes = append(codes, record[0])
	}
	return codes
}

func ndxCodes(t *testing.T) []string {
	var payload struct {
		Data struct {
			Data struct {
				Rows []struct {
					Symbol string `json:"symbol"`
				} `json:"rows"`
			} `json:"data"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(readFile(t, "..", "nasdaq", "testdata", "ndx.json"), &payload))
	var codes []string
	for _, row := range payload.Data.Data.Rows {
		codes = append(codes, row.Symbol)
	}
	return codes
}

// sampleLists 把录下的东方财富美股代码（em_us_codes.json：{代码: 市场编号}，11899 个）做成三个市场的列表。
// 样例缺约 2000 个代码（录的时候有些页被断开）：纳指 100 里不在样例中的补进 105，
// 标普 500 里剩下不在样例中的补进 106（带点的写成下划线），补完每个市场的 total 按最终列表重新算。返回补进去的代码数。
func sampleLists(t *testing.T) (map[string][]string, int) {
	var recorded map[string]int
	require.NoError(t, json.Unmarshal(readFile(t, "testdata", "em_us_codes.json"), &recorded))
	lists := map[string][]string{"m:105": nil, "m:106": nil, "m:107": nil}
	present := map[string]bool{}
	for code, market := range recorded {
		fs := fmt.Sprintf("m:%d", market)
		_, ok := lists[fs]
		require.True(t, ok, "%s 的市场 %d 不认识", code, market)
		lists[fs] = append(lists[fs], code)
		present[code] = true
	}
	added := 0
	// 纳指 100 先补（同时在标普 500 里的 AVGO 等是纳斯达克上市，不能补进 106）。
	for _, code := range ndxCodes(t) {
		if !hasAny(present, candidates(code)) {
			lists["m:105"] = append(lists["m:105"], code)
			present[code] = true
			added++
		}
	}
	for _, code := range sp500Codes(t) {
		if !hasAny(present, candidates(code)) {
			listed := strings.ReplaceAll(code, ".", "_")
			lists["m:106"] = append(lists["m:106"], listed)
			present[listed] = true
			added++
		}
	}
	return lists, added
}

func hasAny(present map[string]bool, list []string) bool {
	for _, item := range list {
		if present[item] {
			return true
		}
	}
	return false
}

func TestCandidatesOrder(t *testing.T) {
	require.Equal(t, []string{"AAPL"}, candidates("AAPL"))
	require.Equal(t, []string{"BRK.B", "BRK_B"}, candidates("BRK.B"))
	require.Equal(t, []string{"BF.B", "BF_B"}, candidates("BF.B"))
	require.Equal(t, []string{"A-B"}, candidates("A-B"), "横线不换写法")
}

// 标普 500 全部 503 只和纳指 100 的 101 只都能对上交易所；带点的代码用下划线写法对上；CBOE 在 107。
func TestLookupMatchesAllSP500AndNDXFromRecordedList(t *testing.T) {
	lists, added := sampleLists(t)
	require.Greater(t, added, 30, "样例缺的代码要补进假列表")
	fake := newFakeClist(lists)
	server := httptest.NewServer(fake)
	defer server.Close()

	spx := sp500Codes(t)
	require.Len(t, spx, 503)
	exchanges, err := Lookup(context.Background(), testConfig(server.URL), spx)
	require.NoError(t, err)
	require.Len(t, exchanges, 503, "503 只全部对上")
	for code, exchange := range exchanges {
		require.Contains(t, []string{"XNAS", "XNYS", "XASE"}, exchange, code)
	}
	require.Equal(t, "XNYS", exchanges["BRK.B"], "BRK.B 在东方财富是 BRK_B，在 106")
	require.Equal(t, "XNYS", exchanges["BF.B"], "BF.B 在东方财富是 BF_B，在 106")
	require.Equal(t, "XASE", exchanges["CBOE"], "CBOE 在 107")
	require.Equal(t, "XNAS", exchanges["AAPL"])

	// 每个市场翻完整：请求数 = 三个市场各自的页数之和。
	require.Equal(t, fake.pages("m:105")+fake.pages("m:106")+fake.pages("m:107"), fake.count())

	ndx := ndxCodes(t)
	require.Len(t, ndx, 101)
	exchanges, err = Lookup(context.Background(), testConfig(server.URL), ndx)
	require.NoError(t, err)
	require.Len(t, exchanges, 101)
	for code, exchange := range exchanges {
		require.Equal(t, "XNAS", exchange, code)
	}
}

// 同一代码出现在两个市场时取 105、106、107 顺序里的第一个；候选写法的顺序优先于市场顺序；
// 只做单向查找：列表里有 ZZZ_ 不代表成分代码 ZZZ 对得上。
func TestLookupPriorityAndOneWayMatch(t *testing.T) {
	fake := newFakeClist(map[string][]string{
		"m:105": {"BOTH1", "DOT_X", "ZZZ_"},
		"m:106": {"BOTH1", "BOTH2", "DOT.X"},
		"m:107": {"BOTH2", "ONLY7", "DOT_Y"},
	})
	server := httptest.NewServer(fake)
	defer server.Close()
	exchanges, err := Lookup(context.Background(), testConfig(server.URL), []string{"BOTH1", "BOTH2", "ONLY7", "DOT.X", "DOT.Y", "ZZZ", "MISSING"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"BOTH1": "XNAS", // 105、106 都有，取 105
		"BOTH2": "XNYS", // 106、107 都有，取 106
		"ONLY7": "XASE",
		"DOT.X": "XNYS", // 原样写法 DOT.X 在 106，优先于下划线写法 DOT_X（在 105）
		"DOT.Y": "XASE", // 点换下划线
	}, exchanges, "ZZZ、MISSING 对不上，不在结果里")
}

// 某页失败一次后重试成功，结果完整。
func TestLookupRetriesFailedPage(t *testing.T) {
	lists, _ := sampleLists(t)
	fake := newFakeClist(lists)
	fake.fail = func(fs string, page, attempt int) int {
		if fs == "m:106" && page == 3 && attempt == 1 {
			return http.StatusBadGateway
		}
		return 0
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	exchanges, err := Lookup(context.Background(), testConfig(server.URL), sp500Codes(t))
	require.NoError(t, err)
	require.Len(t, exchanges, 503)
	require.Equal(t, 2, fake.attempts["m:106#3"])
	require.Equal(t, fake.pages("m:105")+fake.pages("m:106")+fake.pages("m:107")+1, fake.count(), "只多了一次重试")
}

// 某页持续失败：整次报错，请求总数有上限（不超过页数 × (重试 + 1)），后面的页和市场不再请求。
func TestLookupFailsAfterRetriesWithBoundedRequests(t *testing.T) {
	lists, _ := sampleLists(t)
	fake := newFakeClist(lists)
	fake.fail = func(fs string, page, _ int) int {
		if fs == "m:105" && page == 5 {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.PageRetries = 2
	_, err := Lookup(context.Background(), cfg, sp500Codes(t))
	require.Error(t, err)
	require.ErrorContains(t, err, "m:105")
	require.ErrorContains(t, err, "重试 2 次后仍失败")
	require.LessOrEqual(t, fake.count(), fake.pages("m:105")*(2+1), "请求总数不超过 页数×(重试+1)")
	require.Equal(t, 4+3, fake.count(), "前 4 页各 1 次，第 5 页 1+2 次")
	for _, request := range fake.requests {
		require.True(t, strings.HasPrefix(request, "m:105#"), "失败后不再请求其他市场：%s", request)
	}

	// 整个服务一直 500：三个市场里只会碰第一个市场的第一页，1+重试 次请求后整次失败。
	all := newFakeClist(lists)
	all.fail = func(string, int, int) int { return http.StatusInternalServerError }
	allServer := httptest.NewServer(all)
	defer allServer.Close()
	_, err = Lookup(context.Background(), testConfig(allServer.URL), []string{"AAPL"})
	require.Error(t, err)
	require.Equal(t, 3, all.count())
}

// 429 不重试。
func TestLookupDoesNotRetryRateLimit(t *testing.T) {
	fake := newFakeClist(map[string][]string{"m:105": {"A"}, "m:106": {"B"}, "m:107": {"C"}})
	fake.fail = func(string, int, int) int { return http.StatusTooManyRequests }
	server := httptest.NewServer(fake)
	defer server.Close()
	_, err := Lookup(context.Background(), testConfig(server.URL), []string{"A"})
	require.Error(t, err)
	require.Equal(t, 1, fake.count())
}

// 一页返回的行数少于应有的（没有失败，只是少了行）：行数对不上自报的 total，整次报错，不悄悄跳过。
func TestLookupRejectsRowsNotEqualToTotal(t *testing.T) {
	var codes []string
	for i := range 250 {
		codes = append(codes, fmt.Sprintf("S%03d", i))
	}
	fake := newFakeClist(map[string][]string{"m:105": codes, "m:106": {"B"}, "m:107": {"C"}})
	// 自报的 total 比列表实际多 1：读完所有行后少 1 行。
	fake.totalOf = func(fs string, real int) int {
		if fs == "m:105" {
			return real + 1
		}
		return real
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	_, err := Lookup(context.Background(), testConfig(server.URL), []string{"S001"})
	require.Error(t, err)
	require.ErrorContains(t, err, "m:105")
	require.LessOrEqual(t, fake.count(), 3+1, "3 页读完后多读一页空页就结束")

	// 自报的 total 为 0、行数是两个满页（200 行，第三页为空）：cnstock 的翻页规则放行（没有“不满一页”可拒），
	// 只有 Lookup 自己的 total 检查能拦住；total 判断被去掉时这里会成功返回。
	var twoPages []string
	for i := range 200 {
		twoPages = append(twoPages, fmt.Sprintf("T%03d", i))
	}
	zero := newFakeClist(map[string][]string{"m:105": twoPages, "m:106": {"B"}, "m:107": {"C"}})
	zero.totalOf = func(string, int) int { return 0 }
	zeroServer := httptest.NewServer(zero)
	defer zeroServer.Close()
	_, err = Lookup(context.Background(), testConfig(zeroServer.URL), []string{"T001"})
	require.Error(t, err)
	require.ErrorContains(t, err, "接口自报总数 0")
	require.Equal(t, 3, zero.count(), "两个满页加一个空页")

	// 总数在翻页中途变了：报错。
	shifting := newFakeClist(map[string][]string{"m:105": codes, "m:106": {"B"}, "m:107": {"C"}})
	shifting.totalOf = func(fs string, real int) int { return real }
	shifting.onRequest = func(fs string, page int) {
		if fs == "m:105" && page == 2 {
			shifting.mu.Lock()
			shifting.lists["m:105"] = append(shifting.lists["m:105"], "S999")
			shifting.mu.Unlock()
		}
	}
	shiftingServer := httptest.NewServer(shifting)
	defer shiftingServer.Close()
	_, err = Lookup(context.Background(), testConfig(shiftingServer.URL), []string{"S001"})
	require.Error(t, err)
}

// 页数上限：服务端自报的总数无限大、永远有行返回时，翻到 MaxPages 页就整次失败，请求数不超过上限。
func TestLookupStopsAtMaxPages(t *testing.T) {
	var codes []string
	for i := range 1000 {
		codes = append(codes, fmt.Sprintf("S%04d", i))
	}
	fake := newFakeClist(map[string][]string{"m:105": codes, "m:106": codes, "m:107": codes})
	server := httptest.NewServer(fake)
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.MaxPages = 4
	_, err := Lookup(context.Background(), cfg, []string{"S0001"})
	require.Error(t, err)
	require.ErrorContains(t, err, "4 pages")
	require.Equal(t, 4, fake.count())
}

// ctx 已取消：一个请求都不发；请求过程中取消：立即退出，不再重试、不再翻页。
func TestLookupStopsOnContextCancel(t *testing.T) {
	lists, _ := sampleLists(t)
	fake := newFakeClist(lists)
	server := httptest.NewServer(fake)
	defer server.Close()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Lookup(cancelled, testConfig(server.URL), []string{"AAPL"})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 0, fake.count())

	ctx, cancelMid := context.WithCancel(context.Background())
	defer cancelMid()
	fake.onRequest = func(fs string, page int) {
		if fs == "m:105" && page == 3 {
			cancelMid()
		}
	}
	fake.fail = func(fs string, page, _ int) int {
		if fs == "m:105" && page == 3 {
			return http.StatusBadGateway
		}
		return 0
	}
	started := time.Now()
	_, err = Lookup(ctx, Config{BaseURL: server.URL, PageInterval: -1, RetryBackoff: time.Hour}, []string{"AAPL"})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "已超时或取消")
	require.Less(t, time.Since(started), 5*time.Second, "取消后不应等重试间隔")
	require.Equal(t, 3, fake.count())

	// 到期（超时）同样立即退出：页间隔很长时 ctx 先到期。
	expired, cancelExpired := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelExpired()
	slow := newFakeClist(lists)
	slowServer := httptest.NewServer(slow)
	defer slowServer.Close()
	started = time.Now()
	_, err = Lookup(expired, Config{BaseURL: slowServer.URL, PageInterval: time.Hour}, []string{"AAPL"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), 5*time.Second)
	require.Equal(t, 1, slow.count(), "第一页之后就在等页间隔，ctx 到期时退出")
}

// 页间隔生效：相邻两次请求（包括相邻两个市场之间）的间隔不小于 PageInterval。
func TestLookupWaitsPageInterval(t *testing.T) {
	var codes []string
	for i := range 250 {
		codes = append(codes, fmt.Sprintf("S%03d", i))
	}
	fake := newFakeClist(map[string][]string{"m:105": codes, "m:106": codes, "m:107": codes})
	server := httptest.NewServer(fake)
	defer server.Close()
	interval := 30 * time.Millisecond
	_, err := Lookup(context.Background(), Config{BaseURL: server.URL, PageInterval: interval}, []string{"S001"})
	require.NoError(t, err)
	require.Equal(t, 9, fake.count())
	for i := 1; i < len(fake.stamps); i++ {
		require.GreaterOrEqual(t, fake.stamps[i].Sub(fake.stamps[i-1]), interval-3*time.Millisecond, "第 %d 与第 %d 次请求", i, i+1)
	}

	// 注入的等待函数：8 个页间隔（9 页中间）+ 2 个市场间间隔里，市场间那两次单独算。
	var waits []time.Duration
	cfg := Config{BaseURL: server.URL, PageInterval: interval, Sleep: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}}
	_, err = Lookup(context.Background(), cfg, []string{"S001"})
	require.NoError(t, err)
	require.Len(t, waits, 2+3*2, "市场之间 2 次，每个市场 3 页之间 2 次")
	for _, wait := range waits {
		require.Equal(t, interval, wait)
	}
}

// 默认值：页间隔 0（节奏由限流器管）、重试 2 次、页数上限 80。
func TestConfigDefaults(t *testing.T) {
	cfg := Config{}.withDefaults()
	require.Zero(t, cfg.PageInterval)
	require.Equal(t, 2, cfg.PageRetries)
	require.Equal(t, 80, cfg.MaxPages)
	require.NotNil(t, cfg.HTTPClient)
	off := Config{PageInterval: -1, PageRetries: -1}.withDefaults()
	require.Zero(t, off.PageInterval)
	require.Zero(t, off.PageRetries)
}

func TestLookupRejectsEmptyCode(t *testing.T) {
	_, err := Lookup(context.Background(), testConfig("http://127.0.0.1:1"), []string{"AAPL", " "})
	require.Error(t, err)
}
