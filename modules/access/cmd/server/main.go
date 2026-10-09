package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/access/internal/accessproxy"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

func main() {
	trpclog.InstallServiceName("access")
	nonces, err := accessproxy.OpenSQLiteNonces(envOrDefault("MOOX_ACCESS_NONCE_PATH", "./data/access/nonces.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer nonces.Close()

	principals, err := accessproxy.LoadPrincipals(envOrDefault("MOOX_ACCESS_PRINCIPALS_FILE", "./secrets/access-principals.yaml"))
	if err != nil {
		log.Fatal(err)
	}
	proxy, err := accessproxy.New(accessproxy.Options{
		Principals:         principals,
		InboundTargetNode:  strings.TrimSpace(os.Getenv("MOOX_ACCESS_TARGET_NODE")),
		UpstreamTargetNode: strings.TrimSpace(os.Getenv("MOOX_ACCESS_UPSTREAM_TARGET_NODE")),
		UpstreamTarget:     strings.TrimSpace(os.Getenv("MOOX_ACCESS_UPSTREAM_TARGET")),
		NonceNamespace:     envOrDefault("MOOX_ACCESS_NONCE_NAMESPACE", "access"),
		MaxBodyBytes:       envInt64("MOOX_ACCESS_MAX_BODY_BYTES", 32<<20),
		Timeout:            envDuration("MOOX_ACCESS_TIMEOUT", 30*time.Second),
		Nonces:             nonces,
	})
	if err != nil {
		log.Fatal(err)
	}

	s := trpc.NewServer(server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	accessService := s.Service(accessproxy.AccessServiceName)
	if accessService == nil {
		log.Fatalf("access service %q is not configured", accessproxy.AccessServiceName)
	}
	if err := accessproxy.RegisterAccessService(accessService, proxy); err != nil {
		log.Fatal(fmt.Errorf("register access service: %w", err))
	}
	stopHealth, err := registerHealth(s, strings.TrimSpace(os.Getenv("MOOX_ACCESS_UPSTREAM_TARGET")))
	if err != nil {
		log.Fatal(err)
	}
	defer stopHealth()
	if err := s.Serve(); err != nil {
		log.Fatal(err)
	}
}

const healthServiceName = "trpc.moox.access.Health"

func registerHealth(s *server.Server, upstreamTarget string) (func(), error) {
	if s == nil || s.Service(healthServiceName) == nil {
		return func() {}, errors.New("access health service is not configured")
	}
	state := healthz.NewState("access", "access", Version, GitCommit)
	state.SetReady(false)
	handler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return func() {}, err
	}
	if err := healthz.RegisterNoProtocolServiceMux(s.Service(healthServiceName), handler); err != nil {
		return func() {}, fmt.Errorf("register access health: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	interval := envDuration("MOOX_ACCESS_READY_CHECK_INTERVAL", 5*time.Second)
	timeout := envDuration("MOOX_ACCESS_READY_CHECK_TIMEOUT", 2*time.Second)
	go monitorUpstream(ctx, state, upstreamTarget, interval, timeout)
	return cancel, nil
}

func monitorUpstream(ctx context.Context, state *healthz.State, target string, interval, timeout time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	var previous bool
	first := true
	probe := func() {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		err := probeUpstream(probeCtx, target)
		cancel()
		ready := err == nil
		state.SetReady(ready)
		if first || ready != previous {
			if err != nil {
				log.Printf("access upstream readiness=%t target=%s err=%v", ready, target, err)
			} else {
				log.Printf("access upstream readiness=%t target=%s", ready, target)
			}
		}
		first = false
		previous = ready
	}
	probe()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe()
		}
	}
}

func probeUpstream(ctx context.Context, target string) error {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.Scheme != "ip" || parsed.Host == "" {
		return fmt.Errorf("invalid upstream target")
	}
	if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
		return fmt.Errorf("invalid upstream target: %w", err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", parsed.Host)
	if err != nil {
		return err
	}
	return conn.Close()
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		log.Printf("invalid %s=%q; using %s", name, value, fallback)
		return fallback
	}
	return parsed
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		log.Printf("invalid %s=%q; using %d", name, value, fallback)
		return fallback
	}
	return parsed
}
