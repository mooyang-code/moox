package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/config"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/stretchr/testify/require"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestProductionRuntimeAuthenticatedHealthAndShutdown(t *testing.T) {
	port := func() int {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		value := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		return value
	}
	nativePort, healthPort := port(), port()
	path := filepath.Join(t.TempDir(), "trpc_go.yaml")
	framework := fmt.Sprintf("server:\n  timeout: 120000\n  service:\n    - name: trpc.moox.egress.Proxy\n      ip: 127.0.0.1\n      port: %d\n      network: tcp\n      protocol: trpc\n      transport: go-net\n    - name: trpc.moox.egress.Health\n      ip: 127.0.0.1\n      port: %d\n      network: tcp\n      protocol: http_no_protocol\n", nativePort, healthPort)
	require.NoError(t, os.WriteFile(path, []byte(framework), 0o600))
	previous := trpc.ServerConfigPath
	trpc.ServerConfigPath = path
	t.Cleanup(func() { trpc.ServerConfigPath = previous })
	t.Setenv("MOOX_INSTANCE_ID", "egress-proxy@compute-test")
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "fixture")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "synthetic-health-secret")
	cfg, err := config.Load("../../config/app.yaml")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()
	t.Cleanup(cancel)
	httpClient := &http.Client{Timeout: 200 * time.Millisecond}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", healthPort)
	require.Eventually(t, func() bool {
		response, err := httpClient.Get(endpoint + "/readyz")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusUnauthorized
	}, 5*time.Second, 20*time.Millisecond)
	signed := func(path string) *http.Request {
		request, err := http.NewRequest(http.MethodGet, endpoint+path, nil)
		require.NoError(t, err)
		nonce, err := requestauth.NewNonce()
		require.NoError(t, err)
		stamp := time.Now().Unix()
		signature, err := requestauth.Sign("synthetic-health-secret", requestauth.Material{Method: http.MethodGet, Path: path, Timestamp: stamp, Nonce: nonce})
		require.NoError(t, err)
		request.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("moox-health-v1/fixture/%d/%s/%s", stamp, nonce, signature))
		return request
	}
	request := signed("/readyz")
	response, err := httpClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), `"ready":true`)
	require.Contains(t, string(body), "egress-proxy@compute-test")
	response, err = httpClient.Do(request)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	proxyClient := egresspb.NewProxyClientProxy(client.WithTarget(fmt.Sprintf("ip://127.0.0.1:%d", nativePort)), client.WithProtocol("trpc"), client.WithTimeout(time.Second))
	rsp, err := proxyClient.Do(context.Background(), &egresspb.DoReq{Method: "GET", Host: "evil.test", Path: "/"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode())
	response, err = httpClient.Do(signed("/metrics"))
	require.NoError(t, err)
	body, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Contains(t, string(body), "moox_egress_dns_resolver_requests_total")
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("egress runtime did not stop")
	}
	for _, address := range []string{fmt.Sprintf("127.0.0.1:%d", nativePort), fmt.Sprintf("127.0.0.1:%d", healthPort)} {
		require.Eventually(t, func() bool {
			conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if conn != nil {
				_ = conn.Close()
			}
			return err != nil
		}, 5*time.Second, 20*time.Millisecond, "listener remained after shutdown: %s", address)
	}
}
