package unitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errHealthIdentity 表示健康端口上应答的不是本次拉起的进程（二进制或启动标识对不上），这时不自动重启。
var errHealthIdentity = errors.New("runtime health response process identity is invalid; refusing automatic restart")

// probe 请求组件在本机 127.0.0.1 上的健康端口（只听本机、不验签）。
func (s *runtimeState) probe(ctx context.Context, component Component, record pidRecord, readiness bool) (bool, error) {
	ready, _, err := s.probeReasons(ctx, component, record, readiness)
	return ready, err
}

// notReadyReasons 从健康响应里取出没就绪的项：details 里值为 false、以 _ready 结尾的字段名，
// 以及 missing_services 列出的服务名。只取名字，不取自由文本，自由文本可能带私有内容。
func notReadyReasons(raw []byte) []string {
	var payload struct {
		Details map[string]any `json:"details"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	reasons := []string{}
	for key, value := range payload.Details {
		if ok, isBool := value.(bool); isBool && !ok && strings.HasSuffix(key, "_ready") {
			reasons = append(reasons, key)
		}
	}
	if services, ok := payload.Details["missing_services"].([]any); ok {
		for _, service := range services {
			if name, ok := service.(string); ok {
				reasons = append(reasons, name)
			}
		}
	}
	sort.Strings(reasons)
	return reasons
}

func (s *runtimeState) probeReasons(ctx context.Context, component Component, record pidRecord, readiness bool) (bool, []string, error) {
	catalog, _ := s.catalog.Component(component.ID)
	path := "/healthz"
	if readiness {
		path = "/readyz"
	}
	address := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(catalog.Health.Port)) + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false, nil, errors.New("cannot construct runtime health request")
	}
	client := &http.Client{
		Timeout:       2 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return false, nil, errors.New("runtime health endpoint is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return false, nil, errHealthIdentity
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if response.StatusCode != http.StatusOK {
		return false, notReadyReasons(raw), nil
	}
	if err != nil || len(raw) > 64<<10 {
		return false, nil, errors.New("runtime health response cannot be read or exceeds its bound")
	}
	var payload struct {
		Ready  bool   `json:"ready"`
		Binary string `json:"binary_sha256"`
		BootID string `json:"boot_id"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Binary != record.BinarySHA256 || payload.BootID != record.BootID {
		return false, nil, errHealthIdentity
	}
	if !payload.Ready {
		return false, notReadyReasons(raw), nil
	}
	return true, nil, nil
}
