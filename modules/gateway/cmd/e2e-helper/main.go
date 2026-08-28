// Command e2e-helper starts the production Gateway service handler on an
// ephemeral loopback port for cross-module integration tests.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/mooyang-code/moox/modules/gateway/internal/router"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func main() {
	mode := flag.String("mode", "monitor-http", "helper mode: monitor-http or kline-native")
	nodeID := flag.String("node-id", "", "target Gateway node ID")
	upstreamURL := flag.String("upstream-url", "", "loopback Monitor upstream URL")
	upstreamAddress := flag.String("upstream-addr", "", "loopback native tRPC upstream address")
	readyFile := flag.String("ready-file", "", "file receiving the service URL")
	nonceDirectory := flag.String("nonce-dir", "", "persistent nonce directory")
	keyID := flag.String("key-id", "", "service HMAC key ID")
	flag.Parse()
	if err := run(*mode, *nodeID, *upstreamURL, *upstreamAddress, *readyFile, *nonceDirectory, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode, nodeID, upstreamURL, upstreamAddress, readyFile, nonceDirectory, keyID, secret string) error {
	if mode == "kline-native" {
		return runKlineNative(nodeID, upstreamAddress, readyFile, nonceDirectory, keyID, secret)
	}
	if mode != "monitor-http" {
		return fmt.Errorf("unsupported mode %q", mode)
	}
	return runMonitorHTTP(nodeID, upstreamURL, readyFile, nonceDirectory, keyID, secret)
}

func runMonitorHTTP(nodeID, upstreamURL, readyFile, nonceDirectory, keyID, secret string) error {
	parsed, err := url.Parse(upstreamURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" {
		return fmt.Errorf("upstream-url must be a loopback HTTP origin")
	}
	snapshot, err := gatewayproxy.NormalizeAndHash(nodeID, []gatewayproxy.Route{{
		ServiceID: "monitor", Address: parsed.Host, ServicePath: "trpc.moox.monitor.MonitorMgr",
		AllowedMethods: []string{"GetPeerSnapshot"},
		AllowedCallers: []string{"monitor"},
	}})
	if err != nil {
		return err
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		return err
	}
	nonces, err := store.OpenNonces(nonceDirectory)
	if err != nil {
		return err
	}
	defer nonces.Close()
	handler := router.New(router.Options{
		NodeID: nodeID, Credentials: gatewayauth.Credentials{KeyID: keyID, Secret: secret},
		MaxBodyBytes: 4 << 20, Table: &table, Nonces: nonces, Disabled: func() bool { return false },
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(readyFile, []byte("http://"+listener.Addr().String()), 0o600); err != nil {
		return err
	}
	defer os.Remove(readyFile)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 3*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func runKlineNative(nodeID, upstreamAddress, readyFile, nonceDirectory, keyID, secret string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(upstreamAddress))
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("upstream-addr must be a loopback host:port")
	}
	snapshot, err := gatewayproxy.NormalizeAndHash(nodeID, []gatewayproxy.Route{{
		ServiceID: "storage-primary", Address: upstreamAddress, ServicePath: "trpc.moox.storage.PrimaryStore",
		AllowedMethods: []string{"ReadTimeSeriesRows", "UpsertFields"}, AllowedCallers: []string{"moox-skill"},
	}})
	if err != nil {
		return err
	}
	var table gatewayproxy.Table
	if err := table.Replace(snapshot); err != nil {
		return err
	}
	nonces, err := store.OpenNonces(nonceDirectory)
	if err != nil {
		return err
	}
	defer nonces.Close()
	desc, implementation := router.NativeServiceDesc(router.NativeOptions{
		NodeID: nodeID,
		Credentials: gatewayauth.Credentials{
			KeyID: keyID, Caller: "moox-skill", Secret: secret,
		},
		Table: &table, Nonces: nonces, Disabled: func() bool { return false },
	})
	listener, err := net.Listen("tcp", "127.0.0.1:11003")
	if err != nil {
		return err
	}
	service := server.New(
		server.WithNetwork("tcp"),
		server.WithProtocol("trpc"),
		server.WithServiceName("trpc.moox.gateway.ServiceGateway"),
		server.WithListener(listener),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop),
	)
	if err := service.Register(desc, implementation); err != nil {
		listener.Close()
		return err
	}
	if err := writeReadyFile(readyFile, "ip://"+listener.Addr().String()); err != nil {
		listener.Close()
		return err
	}
	defer os.Remove(readyFile)
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	select {
	case <-ctx.Done():
		service.Close(nil)
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			return fmt.Errorf("native gateway did not stop")
		}
	case err := <-done:
		return err
	}
}

func writeReadyFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}
