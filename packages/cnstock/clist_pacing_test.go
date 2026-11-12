package cnstock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// pagedServer 是三页（每页 2 条，共 6 条）的假 clist 服务；fail 决定某次请求（按页号、该页第几次请求）是否返回错误状态。
type pagedServer struct {
	mu       sync.Mutex
	requests []string // 每次请求的页号
	perPage  map[string]int
	fail     func(page string, attempt int) int // 返回非 0 时用它作 HTTP 状态码
	stamps   []time.Time
}

func (s *pagedServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("pn")
		s.mu.Lock()
		s.requests = append(s.requests, page)
		s.stamps = append(s.stamps, time.Now())
		if s.perPage == nil {
			s.perPage = map[string]int{}
		}
		s.perPage[page]++
		attempt := s.perPage[page]
		s.mu.Unlock()
		if s.fail != nil {
			if status := s.fail(page, attempt); status != 0 {
				w.WriteHeader(status)
				return
			}
		}
		var rows string
		switch page {
		case "1":
			rows = `{"f12":"A1"},{"f12":"A2"}`
		case "2":
			rows = `{"f12":"B1"},{"f12":"B2"}`
		case "3":
			rows = `{"f12":"C1"},{"f12":"C2"}`
		}
		_, _ = fmt.Fprintf(w, `{"data":{"total":6,"diff":[%s]}}`, rows)
	})
}

func collect(ctx context.Context, query ClistQuery) ([]string, ClistResult, error) {
	var codes []string
	result, err := FetchClist(ctx, query, func(item ClistItem) error {
		codes = append(codes, item.Code)
		return nil
	})
	return codes, result, err
}

// 可选字段全是零值：不等待、不重试，第 2 页失败时这一页只请求一次、整次失败，行为与加这些字段之前一致。
func TestFetchClistZeroValueOptionsKeepOriginalBehavior(t *testing.T) {
	server := &pagedServer{fail: func(page string, _ int) int {
		if page == "2" {
			return http.StatusBadGateway
		}
		return 0
	}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	slept := 0
	_, _, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, Sleep: func(context.Context, time.Duration) error { slept++; return nil }})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if slept != 0 {
		t.Fatalf("零值时不应等待，实际等了 %d 次", slept)
	}
	if got := fmt.Sprint(server.requests); got != "[1 2]" {
		t.Fatalf("失败页只请求一次：%s", got)
	}
}

// 页间隔：每页之前（第一页除外）等一次 PageInterval。
func TestFetchClistWaitsPageIntervalBetweenPages(t *testing.T) {
	server := &pagedServer{}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	var waits []time.Duration
	codes, result, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, PageInterval: 7 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }})
	if err != nil || len(codes) != 6 || result.Total != 6 {
		t.Fatalf("codes=%v result=%+v err=%v", codes, result, err)
	}
	if fmt.Sprint(waits) != "[7ms 7ms]" {
		t.Fatalf("三页应等两次：%v", waits)
	}
}

// 页间隔用真实时间：相邻两次请求的间隔不小于设定值。
func TestFetchClistPageIntervalIsRealTime(t *testing.T) {
	server := &pagedServer{}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	if _, _, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, PageInterval: 30 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(server.stamps); i++ {
		if gap := server.stamps[i].Sub(server.stamps[i-1]); gap < 28*time.Millisecond {
			t.Fatalf("第 %d 与第 %d 次请求只隔 %v", i, i+1, gap)
		}
	}
}

// 某页失败一次后重试成功；重试前等待的时间逐次递增；已取到的行不重复。
func TestFetchClistRetriesFailedPage(t *testing.T) {
	server := &pagedServer{fail: func(page string, attempt int) int {
		if page == "2" && attempt <= 2 {
			return http.StatusServiceUnavailable
		}
		return 0
	}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	var waits []time.Duration
	codes, result, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, PageRetries: 2, RetryBackoff: 10 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }})
	if err != nil || result.Total != 6 || fmt.Sprint(codes) != "[A1 A2 B1 B2 C1 C2]" {
		t.Fatalf("codes=%v result=%+v err=%v", codes, result, err)
	}
	if fmt.Sprint(server.requests) != "[1 2 2 2 3]" {
		t.Fatalf("requests = %v", server.requests)
	}
	if fmt.Sprint(waits) != "[10ms 20ms]" {
		t.Fatalf("重试前的等待应逐次递增：%v", waits)
	}
}

// 某页一直失败：重试用完后整次失败，这一页的请求数 = 1 + PageRetries，后面的页不再请求。
func TestFetchClistGivesUpAfterRetries(t *testing.T) {
	server := &pagedServer{fail: func(page string, _ int) int {
		if page == "2" {
			return http.StatusBadGateway
		}
		return 0
	}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	_, _, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, PageRetries: 3,
		Sleep: func(context.Context, time.Duration) error { return nil }})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(server.requests) != "[1 2 2 2 2]" {
		t.Fatalf("requests = %v", server.requests)
	}
}

// 429 与 4xx 不重试：刚被限流时再请求只会更糟。
func TestFetchClistDoesNotRetryRateLimit(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		server := &pagedServer{fail: func(page string, _ int) int {
			if page == "1" {
				return status
			}
			return 0
		}}
		ts := httptest.NewServer(server.handler())
		_, _, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, PageRetries: 3,
			Sleep: func(context.Context, time.Duration) error { return nil }})
		ts.Close()
		if err == nil || len(server.requests) != 1 {
			t.Fatalf("status %d：err=%v requests=%v", status, err, server.requests)
		}
	}
}

// 取消 ctx 后立即退出，不再重试，错误能 errors.Is 到 context.Canceled。
func TestFetchClistStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := &pagedServer{fail: func(page string, _ int) int {
		if page == "2" {
			cancel()
			return http.StatusBadGateway
		}
		return 0
	}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	_, _, err := collect(ctx, ClistQuery{BaseURL: ts.URL, PageSize: 2, PageRetries: 5, RetryBackoff: time.Hour})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(server.requests) != "[1 2]" {
		t.Fatalf("取消后不应再请求：%v", server.requests)
	}

	// 等待期间取消：Sleep 默认实现立即返回。
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	server2 := &pagedServer{}
	ts2 := httptest.NewServer(server2.handler())
	defer ts2.Close()
	start := time.Now()
	_, _, err = collect(ctx2, ClistQuery{BaseURL: ts2.URL, PageSize: 2, PageInterval: time.Hour})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v, 耗时 %v", err, time.Since(start))
	}
}

// MaxPages 限制页数：总数大于上限允许读的行数时报错，不再继续请求。
func TestFetchClistHonorsMaxPages(t *testing.T) {
	server := &pagedServer{}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	_, _, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, MaxPages: 2})
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(server.requests) != "[1 2]" {
		t.Fatalf("requests = %v", server.requests)
	}
}
