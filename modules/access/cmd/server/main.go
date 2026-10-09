package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/access/internal/accessproxy"
	"github.com/mooyang-code/moox/modules/access/internal/config"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var Version = "dev"
var BuildTime, GitCommit string

func main() {
	path := flag.String("config", "config/app.yaml", "Access application configuration")
	framework := flag.String("conf", "config/trpc_go.yaml", "tRPC framework configuration")
	flag.Parse()
	trpc.ServerConfigPath = *framework
	trpclog.InstallServiceName("access")
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	nonces, err := accessproxy.OpenSQLiteNonces(cfg.NoncePath)
	if err != nil {
		return err
	}
	defer nonces.Close()
	credentials, err := gatewayauth.LoadCredentialRegistry(cfg.VerificationFile)
	if err != nil {
		return err
	}
	gateway, err := cfg.GatewayClient.OpenInternal(cfg.SourcePath, filepath.Dir(cfg.NoncePath), func(err error) { log.Printf("access directory refresh failed: %v", err) })
	if err != nil {
		return err
	}
	defer gateway.Close()
	proxy, err := accessproxy.New(accessproxy.Options{HostID: gateway.LocalHostID(), Credentials: credentials, Gateway: gateway, Nonces: nonces})
	if err != nil {
		return err
	}
	s := trpc.NewServer(server.WithCurrentSerializationType(codec.SerializationTypeNoop))
	if err := accessproxy.RegisterAccessService(s.Service(accessproxy.AccessServiceName), proxy); err != nil {
		return err
	}
	state := healthz.NewState("access", "access@"+gateway.LocalHostID(), Version, GitCommit)
	handler, err := healthz.WrapFromEnv(healthz.StandardMux(state.Snapshot, promhttp.Handler()))
	if err != nil {
		return err
	}
	if s.Service("trpc.moox.access.Health") == nil {
		return errors.New("access health service is required")
	}
	if err := healthz.RegisterNoProtocolServiceMux(s.Service("trpc.moox.access.Health"), handler); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	updateReady(state, gateway)
	for {
		select {
		case <-ctx.Done():
			state.SetReady(false)
			closeErr := s.Close(nil)
			return errors.Join(closeErr, <-done)
		case err := <-done:
			state.SetReady(false)
			_ = s.Close(nil)
			if err == nil {
				err = errors.New("server exited without an error")
			}
			return fmt.Errorf("access server stopped unexpectedly: %w", err)
		case <-ticker.C:
			updateReady(state, gateway)
		}
	}
}

// Readiness follows the client's verified directory. Control-plane downtime
// does not withdraw valid cached routes; missing service placements do.
func updateReady(state *healthz.State, gateway *gatewayclient.Client) {
	directory := gateway.Directory()
	catalog, err := servicecatalog.LoadEmbedded()
	ready := err == nil && directory.Version != ""
	for _, principal := range catalog.Principals {
		for _, grant := range principal.Allow {
			ready = ready && len(directory.Services[grant.Service]) > 0
		}
	}
	state.SetReady(ready)
}
