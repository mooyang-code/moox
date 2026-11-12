package notification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/netguard"
)

// postJSON 发送一个 JSON 请求并返回状态码与响应体。错误信息只带平台名，不带地址：Webhook 地址本身就是推送凭据。
func postJSON(ctx context.Context, client *http.Client, timeout time.Duration, platform, target string, header http.Header, payload []byte) (int, []byte, error) {
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("notification: failed to create %s request", platform)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, values := range header {
		for _, value := range values {
			request.Header.Set(key, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		if contextErr := requestContext.Err(); contextErr != nil {
			return 0, nil, fmt.Errorf("notification: %s request failed: %w", platform, contextErr)
		}
		// 地址防护拒绝时说清原因（不带地址），否则只看得到一句“请求失败”。
		if errors.Is(err, netguard.ErrAddressBlocked) {
			return 0, nil, fmt.Errorf("notification: %s 的目标地址不是公网地址", platform)
		}
		return 0, nil, fmt.Errorf("notification: %s request failed", platform)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("notification: failed to read %s response", platform)
	}
	if len(body) > maxResponseBytes {
		return 0, nil, fmt.Errorf("notification: %s response body exceeds %d byte limit", platform, maxResponseBytes)
	}
	return response.StatusCode, body, nil
}

// noRedirectClient 复制 client 并禁止跟随重定向：重定向后的主机可能在配置的信任边界之外。
func noRedirectClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{}
	} else {
		cloned := *client
		client = &cloned
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

var errNilSender = errors.New("notification: sender is nil")
