package unitwatch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

// control 交叉检查（计划 4.5）：非 control 主机每轮与 control 主机网关的跨主机 TLS 入口握手，
// 用本机的 MooX 私有 CA 校验证书与主机名。连续失败约 3 分钟推送“control 失联”，恢复后推送“已恢复”。
const (
	peerTimeout      = 5 * time.Second
	peerAlertAfter   = 3 * time.Minute
	peerAlertMinRuns = 3
	// peerTolerance 允许 cron 每分钟触发的时间抖动：三轮失败之间相隔略少于 3 分钟时也算满足。
	peerTolerance = 70 * time.Second
)

// PeerReport 是本轮 control 交叉检查的结果；control 主机自己不检查（报告里没有这一项）。
type PeerReport struct {
	ControlHostID  string `json:"control_host_id"`
	Target         string `json:"target"`
	Reachable      bool   `json:"reachable"`
	Error          string `json:"error,omitempty"`
	FailingSeconds int64  `json:"failing_seconds,omitempty"`
}

// PeerProbe 与目标完成一次 TLS 握手，返回失败原因。
type PeerProbe func(ctx context.Context, target, serverName, caFile string) error

// peerState 保存在 run/watchdog/peer-control.json。
type peerState struct {
	FailingSince time.Time `json:"failing_since,omitzero"`
	Failures     int       `json:"failures,omitempty"`
	Alerted      bool      `json:"alerted,omitempty"`
	LastOK       time.Time `json:"last_ok,omitzero"`
	LastError    string    `json:"last_error,omitempty"`
}

// hostGatewayConfigPath 是本机 host 单元当前发布里的主机网关配置。
func hostGatewayConfigPath(root string) string {
	return filepath.Join(root, "host", "current", "host-gateway", "config", "app.yaml")
}

// checkControlPeer 在非 control 主机上检查 control 网关；control 主机或没有主机网关配置时返回 nil。
func checkControlPeer(ctx context.Context, root string, probe PeerProbe) (*PeerReport, error) {
	config, err := hostgatewayconfig.Load(hostGatewayConfigPath(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取主机网关配置：%w", err)
	}
	if config.Host.ID == config.Control.HostID {
		return nil, nil
	}
	report := &PeerReport{ControlHostID: config.Control.HostID, Target: config.Control.Target}
	if err := probe(ctx, config.Control.Target, config.Control.HostID, config.TLS.CAFile); err != nil {
		report.Error = err.Error()
	} else {
		report.Reachable = true
	}
	return report, nil
}

// probeTLS 是默认的握手检查：只校验服务端证书（与主机网关之间的调用一致，ServerName 为 control 的主机 ID），不发送客户端凭据。
func probeTLS(ctx context.Context, target, serverName, caFile string) error {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("读取 MooX CA：%w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("MooX CA 文件里没有证书")
	}
	ctx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName}}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	return conn.Close()
}

// observePeer 更新交叉检查状态并返回需要推送的告警。
func observePeer(report *PeerReport, state *peerState, host string, now time.Time) []notification.Message {
	if report.Reachable {
		alerted, since := state.Alerted, state.FailingSince
		*state = peerState{LastOK: now}
		if !alerted {
			return nil
		}
		return []notification.Message{peerMessage(report, host, now, notification.SeverityInfo, "已恢复",
			"失联时长："+durationText(now.Sub(since).Round(time.Minute)))}
	}
	if state.FailingSince.IsZero() {
		state.FailingSince = now
	}
	state.Failures++
	state.LastError = report.Error
	elapsed := now.Sub(state.FailingSince)
	report.FailingSeconds = int64(elapsed.Seconds())
	if state.Alerted || state.Failures < peerAlertMinRuns || elapsed < peerAlertAfter-peerTolerance {
		return nil
	}
	state.Alerted = true
	return []notification.Message{peerMessage(report, host, now, notification.SeverityCritical, "失联",
		fmt.Sprintf("现象：连续 %d 轮（约 %s）无法与 control 网关 %s 完成 TLS 握手", state.Failures, durationText(elapsed.Round(time.Minute)), report.Target),
		"最近错误："+firstLine(report.Error),
		"影响：Monitor、EventBus 与推送都在 control 上，期间其他告警可能发不出来",
		"处理：检查 control 主机是否在线、主机网关进程与安全组；control 自己的守护会在 1 分钟内拉起网关")}
}

func peerMessage(report *PeerReport, host string, now time.Time, severity notification.Severity, state string, lines ...string) notification.Message {
	lines = append(lines, "时间："+now.In(beijing).Format("2006-01-02 15:04:05")+"（北京时间）")
	return notification.Message{
		Key:      fmt.Sprintf("watchdog:%s:peer-control:%s", host, state),
		Severity: severity,
		Title:    fmt.Sprintf("【MooX 交叉检查】%s %s（由 %s 观测）", report.ControlHostID, state, host),
		Body:     strings.Join(lines, "\n"),
		Labels:   map[string]string{"host": host, "peer": report.ControlHostID, "kind": "peer_control"},
	}
}

func (s *store) loadPeer() (*peerState, error) {
	state := &peerState{}
	raw, err := os.ReadFile(filepath.Join(s.dir, "peer-control.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, state); err != nil {
		// 状态损坏时从头计数，不影响守护。
		return &peerState{}, nil
	}
	return state, nil
}

func (s *store) savePeer(state *peerState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, "peer-control.json"), raw)
}
