// Package policysync 定期从 Monitor 拉取主机策略（告警渠道与主机守护参数），写入 <部署根>/run/host-policy.json。
// moox-runtime watch 与证书巡检只读这个文件；拉取失败时保留旧文件，控制面不可用也不影响主机侧告警。
package policysync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/prometheus/client_golang/prometheus"
)

// Interval 是拉取周期；启动时先拉一次。
const Interval = 5 * time.Minute

// Fetcher 调用 MonitorMgr.GetHostPolicy。
type Fetcher func(context.Context) (*monitorpb.GetHostPolicyRsp, error)

// Result 是一次同步的结果：written（内容变化，已写入）、unchanged（与缓存一致）。
type Result string

const (
	Written   Result = "written"
	Unchanged Result = "unchanged"
)

var (
	syncTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_hostagent_host_policy_sync_total", Help: "主机策略同步次数，按结果（written/unchanged/error）区分。",
	}, []string{"result"})
	lastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "moox_hostagent_host_policy_last_success_timestamp_seconds", Help: "最近一次成功同步主机策略的时间。",
	})
	policyVersion = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "moox_hostagent_host_policy_version", Help: "本机主机策略缓存的版本号。",
	})
	registerOnce sync.Once
)

// Syncer 把 Monitor 的主机策略同步到本机缓存文件。
type Syncer struct {
	path  string
	fetch Fetcher
	now   func() time.Time

	mu      sync.Mutex
	lastErr string
	lastAt  time.Time
}

// New 创建同步器；path 是 host-policy.json 的绝对路径。
func New(path string, fetch Fetcher) (*Syncer, error) {
	if path == "" || !filepath.IsAbs(path) || fetch == nil {
		return nil, errors.New("host policy sync requires an absolute cache path and a fetcher")
	}
	registerOnce.Do(func() {
		for _, collector := range []prometheus.Collector{syncTotal, lastSuccess, policyVersion} {
			_ = prometheus.Register(collector)
		}
	})
	return &Syncer{path: path, fetch: fetch, now: time.Now}, nil
}

// SyncOnce 拉取一次；内容变化时版本号加一并原子写入，内容相同不重写。失败时旧文件保持不变。
func (s *Syncer) SyncOnce(ctx context.Context) (Result, error) {
	result, err := s.syncOnce(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		syncTotal.WithLabelValues("error").Inc()
		s.lastErr = err.Error()
		return "", err
	}
	syncTotal.WithLabelValues(string(result)).Inc()
	s.lastErr, s.lastAt = "", s.now().UTC()
	lastSuccess.Set(float64(s.lastAt.Unix()))
	return result, nil
}

func (s *Syncer) syncOnce(ctx context.Context) (Result, error) {
	rsp, err := s.fetch(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch host policy: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("fetch host policy: %s %s", code, rsp.GetRetInfo().GetMsg())
	}
	policy := fromResponse(rsp, s.now().UTC())
	if err := policy.Validate(); err != nil {
		return "", fmt.Errorf("monitor returned an invalid host policy: %w", err)
	}
	existing, readErr := hostpolicy.Read(s.path)
	if readErr == nil && existing.Source == hostpolicy.SourceMonitor && existing.ContentHash == policy.Hash() {
		policyVersion.Set(float64(existing.Version))
		return Unchanged, nil
	}
	if readErr == nil {
		policy.Version = existing.Version + 1
	} else {
		// 没有缓存，或缓存损坏、权限不对：直接用中心的值重写。
		policy.Version = 1
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return "", err
	}
	if err := hostpolicy.Write(s.path, policy); err != nil {
		return "", err
	}
	policyVersion.Set(float64(policy.Version))
	return Written, nil
}

func fromResponse(rsp *monitorpb.GetHostPolicyRsp, now time.Time) hostpolicy.Policy {
	policy := hostpolicy.Policy{Source: hostpolicy.SourceMonitor, FetchedAt: now, NotificationChannels: []hostpolicy.Channel{}}
	for _, channel := range rsp.GetChannels() {
		policy.NotificationChannels = append(policy.NotificationChannels, hostpolicy.Channel{Type: channel.GetChannelType(), WebhookURL: channel.GetWebhookUrl()})
	}
	if w := rsp.GetWatchdog(); w != nil {
		policy.Watchdog = hostpolicy.Watchdog{
			Disabled: w.GetDisabled(), MergeWindow: w.GetMergeWindow(), CrashLoopFailures: int(w.GetCrashLoopFailures()),
			CrashLoopWindow: w.GetCrashLoopWindow(), CrashLoopRetry: w.GetCrashLoopRetry(), CrashLoopSlow: w.GetCrashLoopSlow(),
			StableAfter: w.GetStableAfter(), NotReadyAlert: w.GetNotReadyAlert(),
		}
	}
	return policy
}

// Run 启动时同步一次，之后每个周期同步一次，直到 ctx 结束。错误只记录，由下一轮重试。
func (s *Syncer) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := s.SyncOnce(callCtx); err != nil && onError != nil {
			onError(err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Status 返回最近一次成功的时间与最近一次错误，供健康详情展示。
func (s *Syncer) Status() (time.Time, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAt, s.lastErr
}
