package unitwatch

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/notification"
)

// EventBus 端点检查：EventBus 不一定和本机同机（默认部署在存储机上）。非 EventBus 主机每轮对 EventBus 端点做一次 TLS 握手，
// 连续失败约 3 分钟推送“EventBus 不可达”，恢复后推送“已恢复”。EventBus 不可达时，本机所有客户端都会未就绪，
// 守护据此抑制它们各自的“持续未就绪”告警，只留这一条（见 planAlerts）。
//
// 握手用 HostAgent 的 EventBus 凭据文件里的地址与 CA：它在每台主机的 host 单元里都有，而且就是连 EventBus 的那份配置。

// EventBusReport 是本轮 EventBus 端点检查的结果；EventBus 与本机同机（连的是回环地址）或没有凭据文件时没有这一项。
type EventBusReport struct {
	Target         string `json:"target"`
	Reachable      bool   `json:"reachable"`
	Error          string `json:"error,omitempty"`
	FailingSeconds int64  `json:"failing_seconds,omitempty"`
}

// eventBusCredentialPath 是本机 host 单元当前发布里 HostAgent 的 EventBus 凭据文件。
func eventBusCredentialPath(root string) string {
	return filepath.Join(root, "host", "current", "host-agent", "config", "eventbus.yaml")
}

// checkEventBusPeer 检查 EventBus 端点；没有凭据文件、或 EventBus 就在本机时返回 nil。
func checkEventBusPeer(ctx context.Context, root string, probe PeerProbe) (*EventBusReport, error) {
	path := eventBusCredentialPath(root)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	}
	credential, err := jetstream.LoadCredentialFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 EventBus 凭据：%w", err)
	}
	if len(credential.URLs) != 1 {
		return nil, fmt.Errorf("EventBus 凭据需要恰好一个地址")
	}
	endpoint, err := url.Parse(credential.URLs[0])
	if err != nil || endpoint.Hostname() == "" || endpoint.Port() == "" {
		return nil, fmt.Errorf("EventBus 地址无效")
	}
	if ip := net.ParseIP(endpoint.Hostname()); (ip != nil && ip.IsLoopback()) || endpoint.Hostname() == "localhost" {
		return nil, nil
	}
	target := net.JoinHostPort(endpoint.Hostname(), endpoint.Port())
	report := &EventBusReport{Target: target}
	caFile := credential.CAFile
	if caFile != "" && !filepath.IsAbs(caFile) {
		caFile = filepath.Join(filepath.Dir(path), caFile)
	}
	if err := probe(ctx, target, endpoint.Hostname(), caFile); err != nil {
		report.Error = err.Error()
	} else {
		report.Reachable = true
	}
	return report, nil
}

// observeEventBus 更新 EventBus 端点检查状态并返回需要推送的告警。
func observeEventBus(report *EventBusReport, state *peerState, host string, now time.Time) []notification.Message {
	event, elapsed := advancePeer(state, report.Reachable, report.Error, now)
	if !report.Reachable {
		report.FailingSeconds = int64(elapsed.Seconds())
	}
	message := func(severity notification.Severity, state string, lines ...string) notification.Message {
		lines = append(lines, "时间："+now.In(beijing).Format("2006-01-02 15:04:05")+"（北京时间）")
		return notification.Message{
			Key:      fmt.Sprintf("watchdog:%s:peer-eventbus:%s", host, state),
			Severity: severity,
			Title:    fmt.Sprintf("【MooX 交叉检查】EventBus %s（由 %s 观测）", state, host),
			Body:     strings.Join(lines, "\n"),
			Labels:   map[string]string{"host": host, "peer": "eventbus", "kind": "peer_eventbus"},
		}
	}
	switch event {
	case peerUp:
		return []notification.Message{message(notification.SeverityInfo, "已恢复", "不可达时长："+durationText(elapsed.Round(time.Minute)))}
	case peerDown:
		return []notification.Message{message(notification.SeverityCritical, "不可达",
			fmt.Sprintf("现象：连续 %d 轮（约 %s）无法与 EventBus %s 完成 TLS 握手", state.Failures, durationText(elapsed.Round(time.Minute)), report.Target),
			"最近错误："+firstLine(report.Error),
			"影响：本机依赖 EventBus 的组件都会未就绪，守护不再逐个报“持续未就绪”",
			"处理：检查 EventBus 所在主机是否在线、eventbus 进程与安全组（4222）")}
	}
	return nil
}

func (s *store) loadEventBusPeer() (*peerState, error) { return s.loadPeerFile("peer-eventbus.json") }
func (s *store) saveEventBusPeer(state *peerState) error {
	return s.savePeerFile("peer-eventbus.json", state)
}
