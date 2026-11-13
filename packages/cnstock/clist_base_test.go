package cnstock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// hostRouter 是假网络：按主机名把请求转给假服务并记下顺序；push2Down 为真时对 push2 的请求立即返回连接错误。
type hostRouter struct {
	mu        sync.Mutex
	target    *url.URL
	push2Down bool
	hosts     []string // 每次请求的主机名，按顺序
}

func (r *hostRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	r.mu.Lock()
	r.hosts = append(r.hosts, host)
	down := r.push2Down && host == eastMoneyPushHost
	r.mu.Unlock()
	if down {
		return nil, errors.New("假网络：连接被对端断开")
	}
	rewritten := req.Clone(req.Context())
	rewritten.URL.Scheme = r.target.Scheme
	rewritten.URL.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(rewritten)
}

// BaseURL 留空（A 股的现有用法）：零值行为不变——先请求 push2，失败后换 push2test，之后各页固定用 push2test。
// ClistResult.Base 报告实际用的地址；push2 正常时报 push2。
func TestFetchClistZeroValueBaseFallsBackAndSticks(t *testing.T) {
	server := &pagedServer{}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	down := &hostRouter{target: target, push2Down: true}
	_, result, err := collect(context.Background(), ClistQuery{PageSize: 2, HTTPClient: &http.Client{Transport: down}})
	if err != nil {
		t.Fatal(err)
	}
	want := "push2.eastmoney.com,push2test.eastmoney.com,push2test.eastmoney.com,push2test.eastmoney.com"
	if got := strings.Join(down.hosts, ","); got != want {
		t.Fatalf("请求顺序 = %s，期望 %s", got, want)
	}
	if result.Total != 6 || result.Pages != 3 || result.Base != EastMoneyPushFallbackBase {
		t.Fatalf("result = %+v", result)
	}

	healthy := &hostRouter{target: target}
	_, result, err = collect(context.Background(), ClistQuery{PageSize: 2, HTTPClient: &http.Client{Transport: healthy}})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range healthy.hosts {
		if host != eastMoneyPushHost {
			t.Fatalf("push2 正常时不应请求 %s", host)
		}
	}
	if result.Base != EastMoneyPushBase {
		t.Fatalf("Base = %q", result.Base)
	}
}

// 用上一次的 Base 当 BaseURL：直接从备用地址起步，不再请求 push2。
func TestFetchClistStartsFromGivenFallbackBase(t *testing.T) {
	server := &pagedServer{}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	router := &hostRouter{target: target, push2Down: true}
	_, _, err = collect(context.Background(), ClistQuery{BaseURL: EastMoneyPushFallbackBase, PageSize: 2, HTTPClient: &http.Client{Transport: router}})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range router.hosts {
		if host != "push2test.eastmoney.com" {
			t.Fatalf("不应请求 %s", host)
		}
	}
}
