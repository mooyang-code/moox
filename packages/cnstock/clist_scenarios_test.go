package cnstock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 这份文件是“限流器接入前后 FetchClist 行为不变”的比对：每个场景用脚本化的假网络（真实的 push2、push2test 主机名）
// 跑一遍，记下请求序列（主机、页号、第几次、结果）、等待、读到的行、统计与错误，和 testdata/clist_scenarios.golden 逐字比对。
// 金样是在接入限流器之前的代码上生成的；接入之后每个场景换上一个全新的、用假时钟的限流器（见 scenarioLimiter），
// 所以限流器只许让请求变慢，不许改变请求序列、重试、回退域名的条件、返回结果与错误类别。
// 重新生成金样：CLIST_SCENARIO_WRITE=<路径> go test -run TestFetchClistScenarios。

type scenarioNetwork struct {
	script func(host string, page string, n int) string
	cancel context.CancelFunc
	counts map[string]int
	log    []string
}

// RoundTrip 按脚本返回：ok、reset（连接被断开）、s429、s403、s404、s500、s502、badjson、nodata、cancel（先取消 ctx 再失败）。
func (n *scenarioNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	host := req.URL.Hostname()
	page := req.URL.Query().Get("pn")
	key := host + "/" + page
	if n.counts == nil {
		n.counts = map[string]int{}
	}
	n.counts[key]++
	action := "ok"
	if n.script != nil {
		action = n.script(host, page, n.counts[key])
	}
	n.log = append(n.log, fmt.Sprintf("req %s pn=%s try=%d -> %s", host, page, n.counts[key], action))
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	}
	switch action {
	case "reset":
		return nil, errors.New("read: connection reset by peer")
	case "cancel":
		if n.cancel != nil {
			n.cancel()
		}
		return nil, req.Context().Err()
	case "s429":
		return respond(429, "")
	case "s403":
		return respond(403, "")
	case "s404":
		return respond(404, "")
	case "s500":
		return respond(500, "")
	case "s502":
		return respond(502, "")
	case "badjson":
		return respond(200, "{not json")
	case "nodata":
		return respond(200, `{"rc":0}`)
	case "mismatch":
		return respond(200, fmt.Sprintf(`{"data":{"total":%d,"diff":[{"f12":"X%s"},{"f12":"Y%s"}]}}`, 9, page, page))
	case "empty":
		return respond(200, `{"data":{"total":6,"diff":[]}}`)
	}
	rows := map[string]string{"1": `{"f12":"A1"},{"f12":"A2"}`, "2": `{"f12":"B1"},{"f12":"B2"}`, "3": `{"f12":"C1"},{"f12":"C2"}`}[page]
	return respond(200, fmt.Sprintf(`{"data":{"total":6,"diff":[%s]}}`, rows))
}

type scenario struct {
	name     string
	query    ClistQuery
	script   func(host string, page string, n int) string
	visitErr string // 非空时 visit 在读到该代码时返回错误
	// cancelOnVisit 非空时，visit 在读到该代码时取消 ctx。
	cancelOnVisit string
	// allowedDiff 为真的场景在接入限流器之后的结果与金样不同（见场景 31），单独对照 testdata/clist_scenarios_diff.golden。
	allowedDiff bool
}

func scenarioHosts(actions map[string]string) func(string, string, int) string {
	return func(h string, page string, n int) string {
		if action, ok := actions[h+"/"+page+"/"+fmt.Sprint(n)]; ok {
			return action
		}
		if action, ok := actions[h+"/"+page+"/*"]; ok {
			return action
		}
		if action, ok := actions[h+"/*/*"]; ok {
			return action
		}
		return "ok"
	}
}

func errorClass(err error) string {
	if err == nil {
		return "none"
	}
	var classes []string
	for name, target := range map[string]error{
		"timeout": ErrTimeout, "ratelimited": ErrRateLimited, "protocol": ErrProtocol, "httpstatus": ErrHTTPStatus,
		"canceled": context.Canceled, "deadline": context.DeadlineExceeded,
	} {
		if errors.Is(err, target) {
			classes = append(classes, name)
		}
	}
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		classes = append(classes, fmt.Sprintf("status=%d", statusErr.Status))
	}
	if len(classes) == 0 {
		return "other"
	}
	// map 遍历顺序不定，排序后输出。
	for i := 1; i < len(classes); i++ {
		for j := i; j > 0 && classes[j] < classes[j-1]; j-- {
			classes[j], classes[j-1] = classes[j-1], classes[j]
		}
	}
	return strings.Join(classes, "+")
}

func runScenario(t *testing.T, sc scenario) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	network := &scenarioNetwork{script: sc.script, cancel: cancel}
	query := sc.query
	query.PageSize = 2
	query.HTTPClient = &http.Client{Transport: network}
	var sleeps []string
	query.Sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d.String())
		return ctx.Err()
	}
	scenarioLimiter(&query)
	var codes []string
	result, err := FetchClist(ctx, query, func(item ClistItem) error {
		codes = append(codes, item.Code)
		if sc.cancelOnVisit != "" && item.Code == sc.cancelOnVisit {
			cancel()
		}
		if sc.visitErr != "" && item.Code == sc.visitErr {
			return errors.New("visit 拒绝")
		}
		return nil
	})
	var out bytes.Buffer
	fmt.Fprintf(&out, "== %s\n", sc.name)
	for _, line := range network.log {
		fmt.Fprintln(&out, line)
	}
	fmt.Fprintf(&out, "sleeps %v\nrows %v\nresult %+v\nerr class=%s msg=%v\n\n", sleeps, codes, result, errorClass(err), err)
	return out.String()
}

func clistScenarios() []scenario {
	ms := time.Millisecond
	push2, test := "push2.eastmoney.com", "push2test.eastmoney.com"
	return []scenario{
		{name: "01 默认地址全部成功"},
		{name: "02 指定主地址全部成功", query: ClistQuery{BaseURL: EastMoneyPushBase}},
		{name: "03 push2 首页被断开后回退并固定在备用地址", script: scenarioHosts(map[string]string{push2 + "/*/*": "reset"})},
		{name: "04 push2 返回 502 时回退", script: scenarioHosts(map[string]string{push2 + "/1/*": "s502"})},
		{name: "05 push2 返回 500 时回退", script: scenarioHosts(map[string]string{push2 + "/1/*": "s500"})},
		{name: "06 push2 返回 429 不回退", script: scenarioHosts(map[string]string{push2 + "/1/*": "s429"})},
		{name: "07 push2 返回 403 不回退", script: scenarioHosts(map[string]string{push2 + "/1/*": "s403"})},
		{name: "08 push2 返回 404 不回退", script: scenarioHosts(map[string]string{push2 + "/1/*": "s404"})},
		{name: "09 两个地址都被断开", script: scenarioHosts(map[string]string{push2 + "/*/*": "reset", test + "/*/*": "reset"})},
		{name: "10 备用地址 429", script: scenarioHosts(map[string]string{push2 + "/*/*": "reset", test + "/1/*": "s429"})},
		{name: "11 从备用地址起步", query: ClistQuery{BaseURL: EastMoneyPushFallbackBase}},
		{name: "12 从备用地址起步第二页失败不重试", query: ClistQuery{BaseURL: EastMoneyPushFallbackBase}, script: scenarioHosts(map[string]string{test + "/2/*": "s502"})},
		{name: "13 第二页失败后重试成功", query: ClistQuery{PageRetries: 2, RetryBackoff: 10 * ms}, script: scenarioHosts(map[string]string{push2 + "/2/1": "reset", test + "/2/1": "reset"})},
		{name: "14 第二页重试用完", query: ClistQuery{PageRetries: 2, RetryBackoff: 10 * ms}, script: scenarioHosts(map[string]string{push2 + "/2/*": "s502"})},
		{name: "15 重试期间遇到 429 立即失败", query: ClistQuery{PageRetries: 3}, script: scenarioHosts(map[string]string{push2 + "/2/2": "s429", push2 + "/2/1": "s502"})},
		{name: "16 负数重试按零处理", query: ClistQuery{PageRetries: -3}, script: scenarioHosts(map[string]string{push2 + "/2/*": "s502"})},
		{name: "17 页间隔", query: ClistQuery{PageInterval: 7 * ms}},
		{name: "18 页间隔加重试", query: ClistQuery{PageInterval: 5 * ms, PageRetries: 1, RetryBackoff: 3 * ms}, script: scenarioHosts(map[string]string{push2 + "/3/1": "reset", test + "/3/1": "s502"})},
		{name: "19 页数上限", query: ClistQuery{MaxPages: 2}},
		{name: "20 响应不是 JSON", script: scenarioHosts(map[string]string{push2 + "/2/*": "badjson"})},
		{name: "21 响应缺 data", script: scenarioHosts(map[string]string{push2 + "/1/*": "nodata"})},
		{name: "22 总数前后不一致", script: scenarioHosts(map[string]string{push2 + "/2/*": "mismatch"})},
		{name: "23 首页为空", script: scenarioHosts(map[string]string{push2 + "/1/*": "empty"})},
		{name: "24 visit 返回错误立即停止", visitErr: "B1"},
		{name: "25 请求中取消 ctx 且有重试", query: ClistQuery{PageRetries: 3, RetryBackoff: 5 * ms}, script: scenarioHosts(map[string]string{push2 + "/2/*": "cancel", test + "/2/*": "cancel"})},
		{name: "26 请求中取消 ctx 且无重试", script: scenarioHosts(map[string]string{push2 + "/2/*": "cancel", test + "/2/*": "cancel"})},
		{name: "27 自定义地址不回退", query: ClistQuery{BaseURL: "https://example.test"}, script: scenarioHosts(map[string]string{"example.test/2/*": "s502"})},
		{name: "28 回退后备用地址第三页失败再重试", query: ClistQuery{PageRetries: 1, RetryBackoff: 4 * ms}, script: scenarioHosts(map[string]string{push2 + "/*/*": "reset", test + "/3/1": "s500"})},
		{name: "29 读完一页后取消 ctx 且无重试", cancelOnVisit: "A2"},
		{name: "30 读完一页后取消 ctx 且有重试", cancelOnVisit: "A2", query: ClistQuery{PageRetries: 2}},
		// 31 是唯一允许的差别：同一页在同一主机上连续失败到第 5 次时进入冷却，第 6 次请求不再发出，错误由“重试 5 次后仍失败（超时类）”
		// 变成 ErrCoolingDown。接入之前的代码是 6 次请求，错误是 clist 第 2 页重试 5 次后仍失败。线上 A 股 PageRetries 为 0、美股为 2，单页最多 3 次，到不了。
		{name: "31 第二页一直被断开且重试 5 次", allowedDiff: true, query: ClistQuery{PageRetries: 5, RetryBackoff: 3 * ms}, script: scenarioHosts(map[string]string{push2 + "/2/*": "reset"})},
	}
}

func TestFetchClistScenarios(t *testing.T) {
	var main, diff bytes.Buffer
	for _, sc := range clistScenarios() {
		if sc.allowedDiff {
			diff.WriteString(runScenario(t, sc))
		} else {
			main.WriteString(runScenario(t, sc))
		}
	}
	// 文件以单个换行结尾（git diff --check 不允许末尾空行）。
	mainText := strings.TrimRight(main.String(), "\n") + "\n"
	diffText := strings.TrimRight(diff.String(), "\n") + "\n"
	if path := os.Getenv("CLIST_SCENARIO_WRITE"); path != "" {
		for name, text := range map[string]string{path: mainText, path + ".diff": diffText} {
			if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for name, got := range map[string]string{"testdata/clist_scenarios.golden": mainText, "testdata/clist_scenarios_diff.golden": diffText} {
		golden, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(golden) {
			t.Fatalf("FetchClist 的行为与 %s 不一致。\n--- 现在 ---\n%s\n--- 金样 ---\n%s", name, got, string(golden))
		}
	}
}
