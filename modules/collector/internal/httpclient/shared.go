package httpclient

import (
	"crypto/tls"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// 进程内共享的行情 HTTP 客户端：SCF 函数进程会连续服务多次调用，默认每次调用新建客户端，上一分钟建好的连接全部丢弃，
// 每分钟每个标的都要重新握手。2026-10-20 的整点实测里，新建连接上的第一个请求常常停住数秒，而复用连接上的请求都正常返回；
// 打开共享后，上一次调用留下的空闲连接可在下一分钟直接复用。由采集计划的 conn_reuse 控制，默认关闭。
var (
	connReuse    atomic.Bool
	sharedOnce   sync.Once
	sharedClient *HTTPClient
)

// SetConnReuse 设置本次调用是否使用进程内共享客户端。SCF 实例同一时刻只处理一次调用，调用开始时设置即可。
func SetConnReuse(enabled bool) { connReuse.Store(enabled) }

// ConnReuseEnabled 返回本次调用是否使用共享客户端。
func ConnReuseEnabled() bool { return connReuse.Load() }

// SharedClientIfEnabled 在打开连接复用时返回进程内共享客户端，否则返回 nil（调用方沿用每次新建的客户端）。
func SharedClientIfEnabled() *HTTPClient {
	if !connReuse.Load() {
		return nil
	}
	sharedOnce.Do(func() {
		sharedClient = NewHTTPClient(&http.Client{
			Timeout: defaultRequestTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
				// 每个函数一次约 25 个并发请求，空闲连接上限要容得下一整批，否则多出的连接会被关掉。
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 64,
				IdleConnTimeout:     90 * time.Second,
			},
		})
	})
	return sharedClient
}
