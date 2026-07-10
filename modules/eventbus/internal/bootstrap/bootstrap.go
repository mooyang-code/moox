// Package bootstrap wires EventBus in its dependency-safe startup order.
package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/eventbus/internal/broker"
	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/mooyang-code/moox/modules/eventbus/internal/health"
	"github.com/mooyang-code/moox/modules/eventbus/internal/management"
	"github.com/mooyang-code/moox/modules/eventbus/internal/metricspublish"
	"github.com/mooyang-code/moox/modules/eventbus/internal/registry"
	eventbusgen "github.com/mooyang-code/moox/modules/eventbus/proto/eventbusgen"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

type Runtime struct {
	Config   *config.Config
	Broker   *broker.Server
	Conn     *nats.Conn
	Registry *registry.Registry
	Health   *health.Server
}

func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	rt, err := Start(ctx, s, "./config/app.yaml")
	if err != nil {
		return nil, err
	}
	log.InfoContextf(ctx, "moox-eventbus initialized at %s", rt.Broker.URL())
	return s, nil
}

func Start(ctx context.Context, s *server.Server, configPath string) (*Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 1. config
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	// 2. embedded broker
	b, err := broker.New(cfg)
	if err != nil {
		return nil, err
	}
	if err := b.Start(ctx); err != nil {
		return nil, err
	}
	// 3. local JetStream client
	nc, err := connect(ctx, b.URL(), cfg)
	if err != nil {
		_ = b.Shutdown(context.Background())
		return nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		_ = b.Shutdown(context.Background())
		return nil, err
	}
	// 4. topology reconciliation gates readiness
	reg, err := registry.New(js, cfg)
	if err != nil {
		nc.Close()
		_ = b.Shutdown(context.Background())
		return nil, err
	}
	if _, err := reg.Reconcile(ctx); err != nil {
		nc.Close()
		_ = b.Shutdown(context.Background())
		return nil, err
	}
	// 5. read-only management RPC
	mgr := management.New(js, cfg, management.Options{Ready: b.Ready, Connections: b.Connections})
	if s != nil {
		svc := s.Service("trpc.moox.eventbus.EventBusMgr")
		if svc != nil {
			eventbusgen.RegisterEventBusMgrService(svc, mgr)
		} else {
			log.Warn("EventBusMgr service is not configured, skip register")
		}
	}
	registerMetricsReporter(s)
	// 6. health/metrics; ready is set only after every previous stage succeeds.
	hs := health.New(cfg.Health.Addr, b, reg, nc)
	hs.SetReady(true)
	if err := hs.Start(ctx); err != nil {
		nc.Close()
		_ = b.Shutdown(context.Background())
		return nil, err
	}
	rt := &Runtime{Config: cfg, Broker: b, Conn: nc, Registry: reg, Health: hs}
	go func() { <-ctx.Done(); _ = rt.Shutdown(context.Background()) }()
	return rt, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil { return }
	h, err := metricspublish.NewHandler(metricspublish.DefaultConfig("moox-eventbus"))
	if err != nil { log.Warnf("eventbus metrics reporter disabled: %v", err); return }
	service := s.Service("trpc.moox.eventbus.metrics.timer")
	if service == nil { log.Warn("eventbus metrics timer service is not configured, skip register"); return }
	timer.RegisterHandlerService(service, h.Handle)
}

func connect(ctx context.Context, rawURL string, cfg *config.Config) (*nats.Conn, error) {
	opts := []nats.Option{nats.Name("moox-eventbus-control"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.Timeout(5 * time.Second)}
	if cfg.Broker.Auth.Enabled {
		opts = append(opts, nats.UserInfo(cfg.Broker.Auth.Username, cfg.Broker.Auth.Password))
	}
	if cfg.Broker.TLS.Enabled {
		if cfg.Broker.TLS.CAFile != "" {
			opts = append(opts, nats.RootCAs(cfg.Broker.TLS.CAFile))
		}
		opts = append(opts, nats.ClientCert(cfg.Broker.TLS.CertFile, cfg.Broker.TLS.KeyFile), nats.Secure())
	}
	return nats.Connect(rawURL, opts...)
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if r.Health != nil {
		r.Health.SetReady(false)
		_ = r.Health.Shutdown(ctx)
	}
	if r.Conn != nil {
		_ = r.Conn.Drain()
	}
	if r.Conn != nil {
		r.Conn.Close()
	}
	if r.Broker != nil {
		return r.Broker.Shutdown(ctx)
	}
	return nil
}
