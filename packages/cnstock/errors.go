package cnstock

import (
	"errors"
	"fmt"
	"net/http"
)

// 上游错误的类别。调用方按 errors.Is 判断，再映射成自己的错误类型。
var (
	// ErrProtocol 表示响应格式不对或不完整（翻页对不上总数等）。
	ErrProtocol = errors.New("cnstock: 上游响应格式不对")
	// ErrTimeout 表示请求没有得到响应（超时、连接失败）。
	ErrTimeout = errors.New("cnstock: 上游请求超时或连接失败")
	// ErrRateLimited 表示上游限流（HTTP 429）。
	ErrRateLimited = errors.New("cnstock: 上游限流")
	// ErrHTTPStatus 表示上游返回了错误的 HTTP 状态码。
	ErrHTTPStatus = errors.New("cnstock: 上游返回错误状态码")
)

// HTTPStatusError 带着上游的 HTTP 状态码，errors.Is(err, ErrHTTPStatus) 为真。
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%v: status=%d", ErrHTTPStatus, e.Status)
}
func (e *HTTPStatusError) Unwrap() error { return ErrHTTPStatus }

func statusError(status int) error {
	switch {
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status >= 400:
		return &HTTPStatusError{Status: status}
	}
	return nil
}
