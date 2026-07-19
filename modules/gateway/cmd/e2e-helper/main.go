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
	"syscall"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"

	"github.com/mooyang-code/moox/modules/gateway/internal/router"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func main() {
	nodeID := flag.String("node-id", "", "target Gateway node ID")
	upstreamURL := flag.String("upstream-url", "", "loopback Monitor upstream URL")
	readyFile := flag.String("ready-file", "", "file receiving the service URL")
	nonceDirectory := flag.String("nonce-dir", "", "persistent nonce directory")
	keyID := flag.String("key-id", "", "service HMAC key ID")
	flag.Parse()
	if err := run(*nodeID, *upstreamURL, *readyFile, *nonceDirectory, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(nodeID, upstreamURL, readyFile, nonceDirectory, keyID, secret string) error {
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
