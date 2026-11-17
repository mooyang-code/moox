//go:build eventbus_e2e

package main

// 真实 EventBus 进程联调：用真的 moox-admin-cli 逻辑（bootstrap、host-bundle、eventbus-credentials）签发主机证书、
// 导出角色文件与用户表，按部署根的布局摆好，再起预先编译的 moox-eventbus 进程，核对：
// 服务端用所在主机的主机证书，客户端用 MooX CA 根证书校验，认证与主题权限仍按角色令牌。
// 运行方法见 scripts/test/gates/test-eventbus-host-cert-e2e.sh。

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

const (
	eventBusE2ETargetSubject = "moox.event.trade.target.weight_requested.v1.crypto.account-1"
	eventBusE2EPeriodSubject = "moox.event.storage.dataset.period.completed.v1.crypto.dataset-1"
)

func TestEventBusHostCertificateRealProcessE2E(t *testing.T) {
	binary, configDir := os.Getenv("MOOX_EVENTBUS_E2E_BINARY"), os.Getenv("MOOX_EVENTBUS_E2E_CONFIG_DIR")
	require.NotEmpty(t, binary, "MOOX_EVENTBUS_E2E_BINARY 要指向预先编译的 moox-eventbus")
	require.NotEmpty(t, configDir, "MOOX_EVENTBUS_E2E_CONFIG_DIR 要指向 modules/eventbus/config")
	address := eventBusE2EAddress(t)

	// 两套互不相干的控制面：real 是这套系统的 MooX CA，foreign 是别的 CA。存储机的地址就是本机的非回环地址。
	real := newEventBusE2EControl(t, address)
	foreign := newEventBusE2EControl(t, address)
	storage := real.hostBundle(t, "storage")
	control := real.hostBundle(t, "control")
	clients := real.exportClients(t)

	// 部署根：secrets/ 下是身份包（根证书、主机证书），secrets/eventbus/ 下是角色文件与用户表。
	root := eventBusE2ERoot(t, storage, clients)
	strategy, trade := filepath.Join(root, "secrets", "eventbus", "strategy-eventbus.yaml"), filepath.Join(root, "secrets", "eventbus", "trade-eventbus.yaml")
	hostCertificate := eventBusE2ETLS{cert: filepath.Join(root, "secrets", hostbundle.HostCertificateFile), key: filepath.Join(root, "secrets", hostbundle.HostTLSKeyFile), ca: filepath.Join(root, "secrets", hostbundle.CAFile)}

	t.Run("host-certificate-and-role-acl", func(t *testing.T) {
		port := eventBusE2EPort(t)
		process := startEventBusE2E(t, binary, configDir, root, port, net.JoinHostPort(address, strconv.Itoa(port)), &hostCertificate)
		process.waitReady(t, hostCertificate.ca)
		remote, local := eventBusE2EURL(address, port), eventBusE2EURL("127.0.0.1", port)

		// 别的主机（按存储机地址）和同机组件（按回环地址）都能连，证书都通过校验。
		publisher := connectEventBusE2E(t, remote, strategy, nil)
		sameHost := connectEventBusE2E(t, local, trade, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		// 按权限发布与消费：策略发目标事件，Trade 用自己的消费者收到。
		_, err := publisher.PublishRaw(ctx, eventBusE2ETargetSubject, "e2e-target-1", []byte("target"), "application/octet-stream")
		require.NoError(t, err)
		consumer, err := sameHost.NewConsumer(ctx, eventBusE2ETradeConsumer("trade_target_weight_v1"))
		require.NoError(t, err)
		deliveries, err := consumer.Fetch(ctx, 1)
		require.NoError(t, err)
		require.Len(t, deliveries, 1)
		require.Equal(t, "target", string(deliveries[0].RawData))
		require.NoError(t, deliveries[0].Ack(ctx))
		require.NoError(t, consumer.Close())

		// 越权：策略不能发 Storage 的周期完成事件，也不能建 Trade 的消费者；Trade 不能发目标事件，也不能建不属于自己的消费者。
		denied := func(name string, call func(context.Context) error) {
			deniedCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			require.Error(t, call(deniedCtx), name)
		}
		denied("策略发周期完成事件", func(ctx context.Context) error {
			_, err := publisher.PublishRaw(ctx, eventBusE2EPeriodSubject, "e2e-denied-1", []byte("x"), "application/octet-stream")
			return err
		})
		denied("策略建 Trade 的消费者", func(ctx context.Context) error {
			_, err := publisher.NewConsumer(ctx, eventBusE2ETradeConsumer("trade_target_weight_v1"))
			return err
		})
		denied("Trade 发目标事件", func(ctx context.Context) error {
			_, err := sameHost.PublishRaw(ctx, eventBusE2ETargetSubject, "e2e-denied-2", []byte("x"), "application/octet-stream")
			return err
		})
		denied("Trade 建别的消费者", func(ctx context.Context) error {
			_, err := sameHost.NewConsumer(ctx, eventBusE2ETradeConsumer("not_owned"))
			return err
		})
		// 越权的发布没有进流：流里只有前面那一条，已被确认消费（工作队列流）。
		raw, err := nats.Connect(local, nats.UserInfo("eventbus-internal-admin", clients.token(t, "internal-admin")), nats.RootCAs(hostCertificate.ca), nats.TLSHandshakeFirst())
		require.NoError(t, err)
		defer raw.Close()
		js, err := raw.JetStream()
		require.NoError(t, err)
		info, err := js.StreamInfo("MOOX_TRADE")
		require.NoError(t, err)
		require.Zero(t, info.State.Msgs)
		require.EqualValues(t, 1, info.State.LastSeq, "只有授权的那一次发布进了流")

		// 证书对了但令牌不对：认证被拒。证书只证明服务端是谁，客户端是谁仍由角色令牌决定。
		require.Error(t, tryEventBusE2E(remote, strategy, func(cfg *jetstream.Config) { cfg.Password = "wrong-token" }))

		// 没有 MooX CA 根证书的客户端连不上：jetstream 客户端直接拒绝，原生客户端用系统信任库校验失败；明文连接也不行。
		require.ErrorContains(t, tryEventBusE2E(remote, strategy, func(cfg *jetstream.Config) { cfg.TLSCAFile = "" }), "requires TLS CA")
		_, err = nats.Connect(remote, nats.UserInfo("strategy-eventbus", clients.token(t, "strategy-eventbus")), nats.Secure(&tls.Config{MinVersion: tls.VersionTLS12}), nats.TLSHandshakeFirst(), nats.Timeout(2*time.Second))
		require.Error(t, err)
		_, err = nats.Connect("nats://"+net.JoinHostPort(address, strconv.Itoa(port)), nats.UserInfo("strategy-eventbus", clients.token(t, "strategy-eventbus")), nats.Timeout(2*time.Second))
		require.Error(t, err)

		// 拿着别的 CA 的客户端连不上。
		require.Error(t, tryEventBusE2E(remote, strategy, func(cfg *jetstream.Config) {
			cfg.TLSCAFile = filepath.Join(foreign.hostBundle(t, "storage").BundleDir, hostbundle.CAFile)
		}))
		t.Log("SCENARIO PASS host-certificate-and-role-acl")
	})

	t.Run("certificate-from-another-ca-is-rejected", func(t *testing.T) {
		// EventBus 拿的是别的 CA 给同一个主机 ID、同一个地址签的证书（它自己那一套材料齐全，能正常起来）；
		// 这套系统的组件用 MooX CA 校验，连不上。
		foreignRoot := eventBusE2ERoot(t, foreign.hostBundle(t, "storage"), foreign.exportClients(t))
		foreignCertificate := eventBusE2ETLS{cert: filepath.Join(foreignRoot, "secrets", hostbundle.HostCertificateFile), key: filepath.Join(foreignRoot, "secrets", hostbundle.HostTLSKeyFile), ca: filepath.Join(foreignRoot, "secrets", hostbundle.CAFile)}
		port := eventBusE2EPort(t)
		process := startEventBusE2E(t, binary, configDir, foreignRoot, port, net.JoinHostPort(address, strconv.Itoa(port)), &foreignCertificate)
		process.waitReady(t, foreignCertificate.ca)
		for _, url := range []string{eventBusE2EURL(address, port), eventBusE2EURL("127.0.0.1", port)} {
			err := tryEventBusE2E(url, strategy, nil)
			require.Error(t, err, url)
			require.Contains(t, err.Error(), "certificate", url)
		}
		t.Log("SCENARIO PASS certificate-from-another-ca-is-rejected")
	})

	t.Run("certificate-of-another-host-is-rejected", func(t *testing.T) {
		// 存储机上的 EventBus 拿了控制机的主机证书（同一个 MooX CA 签的）。
		wrong := eventBusE2ETLS{cert: filepath.Join(control.BundleDir, hostbundle.HostCertificateFile), key: filepath.Join(control.BundleDir, hostbundle.HostTLSKeyFile), ca: hostCertificate.ca}
		// 配置里宣告的对外地址是存储机的，证书里没有：进程不启动。
		port := eventBusE2EPort(t)
		process := startEventBusE2E(t, binary, configDir, root, port, net.JoinHostPort(address, strconv.Itoa(port)), &wrong)
		require.Contains(t, process.waitExit(t), "does not cover advertised host")
		// 就算进程起来了（宣告地址写成回环，绕过启动时的检查），别的主机按存储机地址连它，证书里没有这个地址，连不上。
		port = eventBusE2EPort(t)
		process = startEventBusE2E(t, binary, configDir, root, port, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &wrong)
		process.waitReady(t, wrong.ca)
		err := tryEventBusE2E(eventBusE2EURL(address, port), strategy, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "certificate")
		t.Log("SCENARIO PASS certificate-of-another-host-is-rejected")
	})

	t.Run("broker-without-certificate-does-not-start", func(t *testing.T) {
		port := eventBusE2EPort(t)
		process := startEventBusE2E(t, binary, configDir, root, port, net.JoinHostPort(address, strconv.Itoa(port)), nil)
		require.Contains(t, process.waitExit(t), "non-loopback broker requires authentication and TLS CA")
		t.Log("SCENARIO PASS broker-without-certificate-does-not-start")
	})
}

// eventBusE2EAddress 是本机的一个非回环 IPv4 地址，当作存储机的对外地址：回环地址每张主机证书里都有，
// 要核对“证书是不是这台主机的”必须用非回环地址连。
func eventBusE2EAddress(t *testing.T) string {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, item := range addresses {
		if network, ok := item.(*net.IPNet); ok && network.IP.To4() != nil && !network.IP.IsLoopback() && !network.IP.IsLinkLocalUnicast() {
			return network.IP.String()
		}
	}
	t.Fatal("这台机器没有非回环的 IPv4 地址，无法核对主机证书的地址")
	return ""
}

func eventBusE2EURL(host string, port int) string {
	return "tls://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func eventBusE2EPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func eventBusE2ETradeConsumer(durable string) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Stream: "MOOX_TRADE", Durable: durable, FilterSubject: "moox.event.trade.target.weight_requested.v1.>",
		AckWait: 5 * time.Second, MaxDeliver: 3, MaxAckPending: 8, FetchMaxWait: 2 * time.Second, DeliverPolicy: nats.DeliverAllPolicy,
	}
}

// eventBusE2EControl 是一套控制面：自己的 Admin 库、主密钥和 CA。
type eventBusE2EControl struct{ opts bootstrapOptions }

func newEventBusE2EControl(t *testing.T, storageAddress string) eventBusE2EControl {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir = filepath.Join(dir, "private")
	require.NoError(t, os.Mkdir(dir, 0o700))
	opts := bootstrapOptions{topologyFile: filepath.Join(dir, "topology.json"), dbPath: filepath.Join(dir, "data", "admin.db"), masterFile: filepath.Join(dir, "secrets", "admin-encryption.key"),
		pkiDir: filepath.Join(dir, "secrets", "pki"), outputDir: filepath.Join(dir, "bundles"), operatorPublicKey: identityPublicKey("operator")}
	writeBootstrapTopology(t, opts, hostbundle.Topology{Version: 1, ControlHostID: "control", Hosts: []hostbundle.Host{
		{HostID: "control", Address: "203.0.113.10", Components: []string{"admin", "collector"}},
		{HostID: "storage", Address: storageAddress, Components: []string{"eventbus", "storage-primary", "storage-view"}},
	}})
	runBootstrapFixture(t, opts)
	control := eventBusE2EControl{opts: opts}
	control.credentials(t, "ensure")
	return control
}

func (c eventBusE2EControl) credentials(t *testing.T, sub string, extra ...string) []byte {
	t.Helper()
	args := append([]string{"eventbus-credentials", sub, "--db-path", c.opts.dbPath, "--encryption-key-file", c.opts.masterFile, "--node-id", "control"}, extra...)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runEventBusCredentialsCommand(args, &stdout, &stderr), stderr.String())
	return stdout.Bytes()
}

func (c eventBusE2EControl) hostBundle(t *testing.T, hostID string) hostbundle.Metadata {
	t.Helper()
	return runHostBundleFixture(t, c.opts, hostBundleRun{hostID: hostID})
}

// eventBusE2EClients 是一次 export-clients --server 的导出目录。
type eventBusE2EClients struct{ dir string }

func (c eventBusE2EControl) exportClients(t *testing.T) eventBusE2EClients {
	t.Helper()
	dir := filepath.Join(filepath.Dir(c.opts.outputDir), "eventbus-export")
	var metadata hostbundle.ClientMetadata
	require.NoError(t, json.Unmarshal(c.credentials(t, "export-clients", "--server", "--roles", "internal-admin,strategy-eventbus,trade-eventbus", "--output-dir", dir), &metadata))
	require.True(t, metadata.Server)
	return eventBusE2EClients{dir: dir}
}

func (c eventBusE2EClients) token(t *testing.T, name string) string {
	t.Helper()
	credential, err := jetstream.LoadCredentialFile(filepath.Join(c.dir, name+".yaml"))
	require.NoError(t, err)
	return credential.Password
}

// eventBusE2ERoot 按部署根的布局摆身份材料：secrets/ 下是身份包里的根证书与主机证书，secrets/eventbus/ 下是导出的角色文件与用户表。
// 角色文件原样使用：里面的 ca_file 是 ../moox-ca.crt，正好指到 secrets/ 下的根证书。
func eventBusE2ERoot(t *testing.T, bundle hostbundle.Metadata, clients eventBusE2EClients) string {
	t.Helper()
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	require.NoError(t, os.MkdirAll(filepath.Join(secrets, "eventbus"), 0o700))
	copyFile := func(from, to string) {
		raw, err := os.ReadFile(from)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(to, raw, 0o600))
	}
	for _, name := range []string{hostbundle.CAFile, hostbundle.HostCertificateFile, hostbundle.HostTLSKeyFile} {
		copyFile(filepath.Join(bundle.BundleDir, name), filepath.Join(secrets, name))
	}
	for _, name := range []string{hostbundle.UsersFile, "internal-admin.yaml", "strategy-eventbus.yaml", "trade-eventbus.yaml"} {
		copyFile(filepath.Join(clients.dir, name), filepath.Join(secrets, "eventbus", name))
	}
	return root
}

type eventBusE2ETLS struct{ cert, key, ca string }

type eventBusE2EProcess struct {
	command *exec.Cmd
	output  *bytes.Buffer
	done    chan error
	port    int
	logs    string
}

// startEventBusE2E 起一个真实的 moox-eventbus 进程：业务配置取仓库里的 app.yaml（流定义原样），
// 只改监听、宣告地址、认证与 TLS，改法与部署时一致。certificate 为 nil 表示不配 TLS。
func startEventBusE2E(t *testing.T, binary, configDir, root string, port int, advertise string, certificate *eventBusE2ETLS) *eventBusE2EProcess {
	t.Helper()
	work := t.TempDir()
	app := eventBusE2EYAML(t, filepath.Join(configDir, "app.yaml"))
	broker := app["broker"].(map[string]any)
	broker["host"], broker["port"], broker["client_advertise"] = "0.0.0.0", port, advertise
	broker["store_dir"] = filepath.Join(work, "jetstream")
	broker["auth"] = map[string]any{"enabled": true, "users_file": filepath.Join(root, "secrets", "eventbus", hostbundle.UsersFile)}
	broker["tls"] = map[string]any{"enabled": false}
	if certificate != nil {
		broker["tls"] = map[string]any{"enabled": true, "cert_file": certificate.cert, "key_file": certificate.key, "ca_file": certificate.ca}
	}
	app["internal_client"] = map[string]any{"credential_file": filepath.Join(root, "secrets", "eventbus", "internal-admin.yaml")}
	framework := eventBusE2EYAML(t, filepath.Join(configDir, "trpc_go.yaml"))
	server := framework["server"].(map[string]any)
	server["admin"].(map[string]any)["port"] = eventBusE2EPort(t)
	for _, item := range server["service"].([]any) {
		if service := item.(map[string]any); service["protocol"] != "timer" {
			service["port"] = eventBusE2EPort(t)
		}
	}
	framework["plugins"].(map[string]any)["metrics"].(map[string]any)["prometheus"].(map[string]any)["port"] = eventBusE2EPort(t)
	write := func(name string, document map[string]any) string {
		raw, err := yamlv3.Marshal(document)
		require.NoError(t, err)
		path := filepath.Join(work, name)
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		return path
	}
	process := &eventBusE2EProcess{output: &bytes.Buffer{}, done: make(chan error, 1), port: port, logs: filepath.Join(work, "logs", "trpc.log")}
	process.command = exec.Command(binary, "-conf", write("trpc_go.yaml", framework), "-config", write("app.yaml", app))
	process.command.Dir, process.command.Stdout, process.command.Stderr = work, process.output, process.output
	require.NoError(t, process.command.Start())
	go func() { process.done <- process.command.Wait() }()
	t.Cleanup(func() {
		_ = process.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-process.done:
		case <-time.After(10 * time.Second):
			_ = process.command.Process.Kill()
		}
	})
	return process
}

func eventBusE2EYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, yamlv3.Unmarshal(raw, &document))
	return document
}

func (p *eventBusE2EProcess) text() string {
	logs, _ := os.ReadFile(p.logs)
	return p.output.String() + string(logs)
}

// waitReady 等 broker 在回环地址上完成 TLS 握手（按给定的根证书校验）。
func (p *eventBusE2EProcess) waitReady(t *testing.T, caFile string) {
	t.Helper()
	raw, err := os.ReadFile(caFile)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(raw))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			p.done <- err
			t.Fatalf("EventBus 进程提前退出：%v\n%s", err, p.text())
		default:
		}
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port)), &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("EventBus 进程 30 秒内没有就绪\n%s", p.text())
}

// waitExit 等进程自己以失败退出，返回它的输出与日志。
func (p *eventBusE2EProcess) waitExit(t *testing.T) string {
	t.Helper()
	select {
	case err := <-p.done:
		p.done <- err
		require.Error(t, err, "进程应当以失败退出")
		return p.text()
	case <-time.After(30 * time.Second):
		t.Fatalf("EventBus 进程没有退出\n%s", p.text())
		return ""
	}
}

// tryEventBusE2E 用角色文件连一次 EventBus（和各组件一样走 jetstream 客户端），返回连接错误。
func tryEventBusE2E(url, roleFile string, mutate func(*jetstream.Config)) error {
	client, err := dialEventBusE2E(url, roleFile, mutate)
	if err == nil {
		_ = client.Close()
	}
	return err
}

func connectEventBusE2E(t *testing.T, url, roleFile string, mutate func(*jetstream.Config)) *jetstream.Client {
	t.Helper()
	client, err := dialEventBusE2E(url, roleFile, mutate)
	require.NoError(t, err, url)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func dialEventBusE2E(url, roleFile string, mutate func(*jetstream.Config)) (*jetstream.Client, error) {
	cfg := jetstream.Config{Name: "eventbus-host-cert-e2e", ConnectTimeout: 3 * time.Second, ReconnectWait: 50 * time.Millisecond}
	if err := cfg.ApplyCredentialFile(roleFile); err != nil {
		return nil, fmt.Errorf("读角色文件: %w", err)
	}
	// 导出的角色文件里是目录里的固定端口；部署时会改写成实际地址，这里直接换成这次进程的地址。
	cfg.URLs = []string{url}
	if mutate != nil {
		mutate(&cfg)
	}
	return jetstream.Connect(context.Background(), cfg)
}
