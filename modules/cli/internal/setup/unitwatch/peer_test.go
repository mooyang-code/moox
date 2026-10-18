package unitwatch

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
)

// testCA 生成一个 CA 与由它签发、SAN 为 dnsName 的服务端证书，返回 CA 的 PEM 文件路径与服务端证书。
func testCA(t *testing.T, dnsName string) (string, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{dnsName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "moox-ca.crt")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))
	return path, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serveTLS 在本地端口上提供 TLS 握手，返回地址。
func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	return listener.Addr().String()
}

// 可达且证书由 MooX CA 签发给 control：通过；证书不是这个 CA 签发、主机名不符、端口不通：失败。
func TestProbeTLS(t *testing.T) {
	caFile, cert := testCA(t, "control")
	address := serveTLS(t, cert)
	require.NoError(t, probeTLS(t.Context(), address, "control", caFile))
	require.Error(t, probeTLS(t.Context(), address, "storage", caFile), "主机名不符")
	otherCA, _ := testCA(t, "control")
	require.Error(t, probeTLS(t.Context(), address, "control", otherCA), "不是本机 CA 签发")

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	unreachable := closed.Addr().String()
	require.NoError(t, closed.Close())
	require.Error(t, probeTLS(t.Context(), unreachable, "control", caFile), "端口不通")
}

// 连续三轮失败（约 3 分钟）发一条严重告警，之后不重复；恢复时发一条提示；control 主机自己不检查。
func TestObservePeerAlertsOnceAndRecovers(t *testing.T) {
	state := &peerState{}
	start := time.Date(2026, 10, 18, 6, 0, 3, 0, time.UTC)
	fail := &PeerReport{ControlHostID: "control", Target: "10.1.12.13:11003", Error: "dial tcp: i/o timeout"}
	for i := 0; i < 2; i++ {
		require.Empty(t, observePeer(fail, state, "storage", start.Add(time.Duration(i)*time.Minute)))
	}
	messages := observePeer(fail, state, "storage", start.Add(2*time.Minute))
	require.Len(t, messages, 1)
	require.Equal(t, notification.SeverityCritical, messages[0].Severity)
	require.Equal(t, "【MooX 交叉检查】control 失联（由 storage 观测）", messages[0].Title)
	require.Contains(t, messages[0].Body, "i/o timeout")
	require.Empty(t, observePeer(fail, state, "storage", start.Add(3*time.Minute)))

	ok := &PeerReport{ControlHostID: "control", Target: fail.Target, Reachable: true}
	messages = observePeer(ok, state, "storage", start.Add(5*time.Minute))
	require.Len(t, messages, 1)
	require.Equal(t, notification.SeverityInfo, messages[0].Severity)
	require.Equal(t, "【MooX 交叉检查】control 已恢复（由 storage 观测）", messages[0].Title)
	require.Empty(t, observePeer(ok, state, "storage", start.Add(6*time.Minute)))

	// 一次短暂失败后恢复：不告警。
	require.Empty(t, observePeer(fail, state, "storage", start.Add(7*time.Minute)))
	require.Empty(t, observePeer(ok, state, "storage", start.Add(8*time.Minute)))
}

// 整轮守护：非 control 主机的交叉检查结果写进报告，告警经同一套渠道发出；状态跨轮保存在 peer-control.json。
func TestRunDeliversPeerControlAlerts(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"host": {"host-gateway", "host-agent"}}, hostpolicy.Watchdog{})
	writeGatewayConfig(t, h.root, "storage")
	for path, plan := range h.plans {
		plan.HostID = "storage"
		h.plans[path] = plan
	}
	// 本机 CA 文件不存在，默认握手检查必然失败，不需要真实网络。
	for i := 0; i < 3; i++ {
		report := run()
		require.NotNil(t, report.PeerControl)
		require.False(t, report.PeerControl.Reachable)
		h.now = h.now.Add(time.Minute)
	}
	require.Equal(t, []string{"【MooX 交叉检查】control 失联（由 storage 观测）"}, titles(sink.messages))
	require.FileExists(t, filepath.Join(h.root, "run", "watchdog", "peer-control.json"))
}

func writeGatewayConfig(t *testing.T, root, hostID string) {
	t.Helper()
	raw, err := hostgatewayconfig.Encode(hostgatewayconfig.Default(hostID, "control", "10.1.12.13", "key-1"))
	require.NoError(t, err)
	path := hostGatewayConfigPath(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}

func TestCheckControlPeerUsesHostGatewayConfig(t *testing.T) {
	root := t.TempDir()
	probe := func(_ context.Context, target, serverName, caFile string) error {
		require.Equal(t, "10.1.12.13:11003", target)
		require.Equal(t, "control", serverName)
		require.Equal(t, filepath.Join(root, "host", "current", "certs", "moox-ca.crt"), caFile)
		return errors.New("connection refused")
	}
	report, err := checkControlPeer(t.Context(), root, probe)
	require.NoError(t, err)
	require.Nil(t, report, "没有主机网关配置时不检查")

	writeGatewayConfig(t, root, "storage")
	report, err = checkControlPeer(t.Context(), root, probe)
	require.NoError(t, err)
	require.False(t, report.Reachable)
	require.Equal(t, "connection refused", report.Error)

	controlRoot := t.TempDir()
	writeGatewayConfig(t, controlRoot, "control")
	report, err = checkControlPeer(t.Context(), controlRoot, probe)
	require.NoError(t, err)
	require.Nil(t, report, "control 主机不检查自己")
}
