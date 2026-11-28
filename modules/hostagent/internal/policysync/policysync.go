// Package policysync 每分钟调用 MonitorMgr.SyncHostAgent 上报这一分钟采到的主机快照并拉取全部告警渠道，
// 写入 <部署根>/data/host-agent/host-policy.json；拉取失败时保留旧文件，控制面不可用也不影响非控制机直推告警。
// 同步结果同时是非控制机对控制机与 Monitor 的直推依据（见 Watch）。
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

// Fetcher 调用 MonitorMgr.SyncHostAgent。同步由 HostAgent 的采样定时器驱动（启动时一次，之后每分钟一次）：
// 连续 3 次失败即推送“控制机或 Monitor 不可达”（Watch）。
type Fetcher func(context.Context, *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error)

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

	watch *Watch
}

// SetWatch 让每次拉取的结果交给直推判断（控制机或 Monitor 不可达、Monitor 反复重启）。
func (s *Syncer) SetWatch(watch *Watch) { s.watch = watch }

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

// SyncOnce 带上主机快照（可以为空）拉取一次；内容变化时版本号加一并原子写入，内容相同不重写。失败时旧文件保持不变。
// 交给直推判断的只是“能不能从 Monitor 拿到策略”：本机写缓存失败时 Monitor 是可达的，不算不可达。
func (s *Syncer) SyncOnce(ctx context.Context, report *monitorpb.HostReport) (Result, error) {
	result, startedAt, fetchErr, err := s.syncOnce(ctx, report)
	if s.watch != nil {
		s.watch.Observe(ctx, startedAt, fetchErr)
	}
	if err != nil {
		syncTotal.WithLabelValues("error").Inc()
		return "", err
	}
	syncTotal.WithLabelValues(string(result)).Inc()
	lastSuccess.Set(float64(s.now().UTC().Unix()))
	return result, nil
}

// syncOnce 拉取并写入一次；startedAt 是 Monitor 在响应里给出的进程启动时刻（毫秒，拉取失败时为 0）。
// fetchErr 只在没能从 Monitor 拿到策略时非空，err 还包括本机写缓存的失败。
func (s *Syncer) syncOnce(ctx context.Context, report *monitorpb.HostReport) (result Result, startedAt int64, fetchErr, err error) {
	rsp, err := s.fetch(ctx, &monitorpb.SyncHostAgentReq{Host: report})
	if err != nil {
		err = fmt.Errorf("同步主机快照与告警渠道失败: %w", err)
		return "", 0, err, err
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		err = fmt.Errorf("同步主机快照与告警渠道失败: %s %s", code, rsp.GetRetInfo().GetMsg())
		return "", 0, err, err
	}
	result, err = s.write(rsp)
	return result, rsp.GetMonitorStartedAt(), nil, err
}

// write 校验并写入主机策略；内容相同不重写。
func (s *Syncer) write(rsp *monitorpb.SyncHostAgentRsp) (Result, error) {
	policy := fromResponse(rsp, s.now().UTC())
	if err := policy.Validate(); err != nil {
		return "", fmt.Errorf("monitor returned an invalid host policy: %w", err)
	}
	existing, readErr := hostpolicy.Read(s.path)
	if readErr == nil && existing.ContentHash == policy.Hash() {
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

func fromResponse(rsp *monitorpb.SyncHostAgentRsp, now time.Time) hostpolicy.Policy {
	policy := hostpolicy.Policy{FetchedAt: now, NotificationChannels: []hostpolicy.Channel{}}
	for _, channel := range rsp.GetChannels() {
		policy.NotificationChannels = append(policy.NotificationChannels, hostpolicy.Channel{Type: channel.GetChannelType(), WebhookURL: channel.GetWebhookUrl(), Secret: channel.GetSecret()})
	}
	return policy
}
