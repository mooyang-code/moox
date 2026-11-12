package cnstock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// PageRetries 为负数按 0 处理：照常请求一次，失败时返回真实的错误，不是不请求就报错。
func TestFetchClistNegativeRetriesBehaveAsZero(t *testing.T) {
	server := &pagedServer{fail: func(page string, _ int) int {
		if page == "2" {
			return http.StatusBadGateway
		}
		return 0
	}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()
	_, _, err := collect(context.Background(), ClistQuery{BaseURL: ts.URL, PageSize: 2, PageRetries: -1})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(server.requests) != "[1 2]" {
		t.Fatalf("requests = %v", server.requests)
	}
	// 没有失败时负数也能读完。
	healthy := &pagedServer{}
	ts2 := httptest.NewServer(healthy.handler())
	defer ts2.Close()
	if codes, _, err := collect(context.Background(), ClistQuery{BaseURL: ts2.URL, PageSize: 2, PageRetries: -5}); err != nil || len(codes) != 6 {
		t.Fatalf("codes=%v err=%v", codes, err)
	}
}
