package command

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
)

func TestManifestCollectorEnvironmentPreflightStopsAllUploads(t *testing.T) {
	setCollectorCLSTestCredentials(t)
	goEnvironment, err := exec.Command("go", "env", "-json", "GOPATH", "GOCACHE").Output()
	require.NoError(t, err)
	var paths map[string]string
	require.NoError(t, json.Unmarshal(goEnvironment, &paths))
	t.Setenv("GOPATH", paths["GOPATH"])
	t.Setenv("GOCACHE", paths["GOCACHE"])
	t.Setenv("HOME", t.TempDir())
	ca := mustTestEventBusCAPEM(t)
	host := startCollectorPublicationSSH(t, ca)
	var uploads, creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/collectmgr/GetTaskList", "/api/service/collectmgr/GetTaskList":
			_, _ = io.WriteString(w, `{"ret_info":{"code":0},"tasks":[]}`)
		case "/api/admin/cloudnode/ListCloudAccounts", "/api/service/cloudnode/ListCloudAccounts":
			_, _ = io.WriteString(w, `{"ret_info":{"code":0},"accounts":[{"account_id":"tencent-scf","credential_secret_id":"tencent-default"}]}`)
		case "/api/service/secret/GetSecretValue":
			_, _ = io.WriteString(w, `{"ret_info":{"code":0},"secret":{"category":"cloud","provider":"tencent","status":"active","key_id":"cls-id","secret_value":"cls-secret"}}`)
		case "/api/admin/cloudnode/InitPackageUpload", "/api/service/cloudnode/InitPackageUpload":
			uploads.Add(1)
			http.Error(w, "must not upload", http.StatusInternalServerError)
		case "/api/admin/cloudnode/SubmitCreateNodes", "/api/admin/cloudnode/SubmitDeployNodes", "/api/service/cloudnode/SubmitCreateNodes", "/api/service/cloudnode/SubmitDeployNodes":
			creates.Add(1)
			http.Error(w, "must not create", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected control endpoint: %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	previousCLS := newCollectorCLSAPI
	newCollectorCLSAPI = func(string, string, string) (tencent.CLSAPI, error) { return collectorCLSAPI{}, nil }
	t.Cleanup(func() { newCollectorCLSAPI = previousCLS })
	collectorRoot, err := filepath.Abs(filepath.Join("..", "..", "..", "collector"))
	require.NoError(t, err)
	manifestPath := filepath.Join(t.TempDir(), "moox.toml")
	manifest := fmt.Sprintf(`[admin]
username = "admin"
password = "test-password"
[tencent_cloud]
secret_id = "test-id"
secret_key = "test-key"
region = "ap-guangzhou"
[eventbus]
host = "203.0.113.10"
port = 4222
tls_enabled = true
[hosts."127.0.0.1"]
port = %d
username = "test"
password = "test"
[hosts."203.0.113.10"]
port = 22
username = "test"
password = "test"
[hosts."203.0.113.20"]
port = 22
username = "test"
password = "test"
[control_host]
name = "control"
host = "127.0.0.1"
[scf_fetcher]
enabled = true
[scf_fetcher.cloud_account]
account_id = "tencent-scf"
account_name = "Tencent SCF"
credential_secret_id = "tencent-default"
app_id = "1255382561"
cos_region = "ap-guangzhou"
cos_bucket = "moox-scf-guangzhou-1255382561"
[[scf_fetcher.spaces]]
space_id = "stockcn"
entrypoint = "market_data"
market_id = "stockcn"
instrument_type = "equity"
provider_id = "eastmoney"
source_id = "stockcn_http"
package_config_dir = "scf/stockcn"
package_name = "moox-collector-stockcn"
function_prefix = "moox-fetcher-stockcn"
timer_function_count = 2
measured_safe_group_size = 1
memory_size = 64
timeout_seconds = 60
realtime_batch_size = 10
max_inflight_requests = 10
request_timeout_ms = 1000
http_max_attempts = 4
storage_max_attempts = 1
storage_timeout_ms = 5000
collector_rpc_gateway_target = "ip://203.0.113.10:11003"
collector_gateway_target_node = "collector"
storage_gateway_host = "203.0.113.20"
storage_access_target_nodes = { ap-guangzhou = "a", ap-singapore = "%s" }
[[scf_fetcher.spaces.regions]]
region = "ap-guangzhou"
enabled = true
function_count = 1
[[scf_fetcher.spaces.regions]]
region = "ap-singapore"
enabled = true
function_count = 1
`, host.Port, strings.Repeat("b", 120))
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))
	fetcher, _, err := loadCollectorSCFFetcherConfigSnapshot(manifestPath, "stockcn")
	require.NoError(t, err)
	base := collectorPublishOptions{
		collectorPackageOptions: collectorPackageOptions{CollectorRoot: collectorRoot, SpaceID: "stockcn", PackageConfigDir: fetcher.PackageConfigDir, Entrypoint: fetcher.Entrypoint, CLSLogsetID: "logset-from-api", CLSTopicID: "topic-from-api", EventBusCAPEM: ca},
		SpaceID:                 "stockcn", Region: "ap-guangzhou", TriggerType: "timer", BizType: "market_fetcher", PackageName: fetcher.PackageName, FetcherConfig: fetcher,
		StorageRPCGatewayTarget: fetcher.StorageRPCGatewayTarget, StoragePrimaryAuthSecret: "primary-secret", RuntimeServiceKeyID: "collector", RuntimeServiceSecretKey: "aabbccdd",
		EventBusCredential: &jetstream.CredentialFile{Version: 1, URLs: []string{"tls://203.0.113.10:4222"}, Username: "publisher", Password: "publisher-secret"},
		CLSHost:            "ap-guangzhou.cls.tencentcs.com",
	}
	require.NoError(t, prepareCollectorPublicationTrust(&base))
	item, err := buildCollectorCreateNodeItem(base, collectorPreflightPackageID(base))
	require.NoError(t, err)
	padding := strings.Repeat("x", 4096-tencent.SCFEnvironmentBytes(item.Environment)-len("PADDING")-2-64)
	for _, tc := range []struct{ name, packageName, padding, failingRegion string }{
		{"first region overflow", strings.Repeat("n", 4096), "", "ap-guangzhou"},
		{"second region overflow", "moox-collector", padding, "ap-singapore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := collectorPublishOptions{
				collectorPackageOptions: collectorPackageOptions{CollectorRoot: collectorRoot, Out: filepath.Join(t.TempDir(), "package.zip")},
				ControlURL:              server.URL, File: manifestPath, SpaceID: "stockcn", PackageName: tc.packageName,
			}
			if tc.padding != "" {
				opts.Env = []string{"PADDING=" + tc.padding}
			}
			_, err := publishCollectorFunction(context.Background(), opts)
			require.ErrorContains(t, err, "4096")
			require.ErrorContains(t, err, "region "+tc.failingRegion)
			require.Zero(t, uploads.Load())
			require.Zero(t, creates.Load())
			require.NotContains(t, err.Error(), "publisher-secret")
		})
	}
}

func startCollectorPublicationSSH(t *testing.T, ca []byte) setupconfig.Host {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(private)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	config := &xssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer raw.Close()
				connection, channels, requests, err := xssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer connection.Close()
				go xssh.DiscardRequests(requests)
				for request := range channels {
					if request.ChannelType() != "session" {
						_ = request.Reject(xssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := request.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "exec" {
								_ = request.Reply(false, nil)
								continue
							}
							var payload struct{ Command string }
							_ = xssh.Unmarshal(request.Payload, &payload)
							_ = request.Reply(true, nil)
							var stdout string
							switch {
							case strings.Contains(payload.Command, "moox-collector-blacklist-preflight"):
								raw := "scf_region_blacklists:\n  stockcn: []\n"
								stdout = fmt.Sprintf("sha256:%x\n/data/moox/bin/moox-collector\n%s", sha256.Sum256([]byte(raw)), raw)
							case strings.Contains(payload.Command, "market-fetch-publisher.yaml"):
								stdout = "version: 1\nurls: [tls://203.0.113.10:4222]\nusername: publisher\npassword: publisher-secret\n"
							case strings.Contains(payload.Command, "ca.pem"), strings.Contains(payload.Command, "peers.pem"):
								stdout = string(ca)
							case strings.Contains(payload.Command, "storage-internal-auth.env"):
								stdout = "MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n"
							case strings.Contains(payload.Command, "gateway-moox-cli.key"):
								stdout = "11223344"
							case strings.Contains(payload.Command, "gateway-collector.key"):
								stdout = "aabbccdd"
							case strings.Contains(payload.Command, "root.crt"):
							default:
								t.Errorf("unexpected SSH command: %s", payload.Command)
							}
							_, _ = io.WriteString(channel, stdout)
							_, _ = channel.SendRequest("exit-status", false, xssh.Marshal(struct{ Status uint32 }{}))
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); workers.Wait() })
	host := setupconfig.Host{Name: "control", Address: "127.0.0.1", Port: port, Username: "test", Password: "test"}
	require.NoError(t, setupssh.TrustHost(context.Background(), sshTarget(host), xssh.FingerprintSHA256(signer.PublicKey()), setupssh.Options{}))
	return host
}
