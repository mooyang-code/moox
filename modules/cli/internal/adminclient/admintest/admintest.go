// Package admintest 提供 adminclient 的测试替身：每次调用转成对 httptest 服务器的
// POST /<tRPC 服务名>/<方法>，请求体为 JSON，空间 ID 放在 X-Space-Id 请求头。
package admintest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
)

// Client 返回把调用发往 baseURL 的 adminclient。
func Client(baseURL string) *adminclient.Client {
	return adminclient.NewWithSender(Sender(baseURL))
}

// Sender 返回把调用发往 baseURL 的发送函数；非 2xx 响应视为调用失败。
func Sender(baseURL string) adminclient.Sender {
	baseURL = strings.TrimRight(baseURL, "/")
	return func(ctx context.Context, servicePath, method string, body []byte, spaceID string) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+servicePath+"/"+method, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		if spaceID != "" {
			request.Header.Set("X-Space-Id", spaceID)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(raw)))
		}
		return raw, nil
	}
}
