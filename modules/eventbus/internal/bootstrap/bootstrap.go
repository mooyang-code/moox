// Package bootstrap wires EventBus in its dependency-safe startup order.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	"gopkg.in/yaml.v3"
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
	hs := health.New(b, reg, nc)
	hs.SetReady(true)
	if s != nil {
		if err := hs.Register(s.Service("trpc.moox.eventbus.Health")); err != nil {
			nc.Close()
			_ = b.Shutdown(context.Background())
			return nil, err
		}
	}
	rt := &Runtime{Config: cfg, Broker: b, Conn: nc, Registry: reg, Health: hs}
	go func() { <-ctx.Done(); _ = rt.Shutdown(context.Background()) }()
	return rt, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	// The reporter identity must match the SysDeploy service name so Monitor
	// can authorize snapshots without a second, manually maintained registry.
	h, err := metricspublish.NewHandler(metricspublish.DefaultConfig("eventbus"))
	if err != nil {
		log.Warnf("eventbus metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.eventbus.metrics.timer")
	if service == nil {
		log.Warn("eventbus metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}

func connect(ctx context.Context, rawURL string, cfg *config.Config) (*nats.Conn, error) {
	opts := []nats.Option{nats.Name("moox-eventbus-control"), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.ReconnectBufSize(-1), nats.Timeout(5 * time.Second)}
	username, password, caFile := cfg.Broker.Auth.Username, cfg.Broker.Auth.Password, cfg.Broker.TLS.CAFile
	if cfg.InternalClient.TLSCAFile != "" {
		caFile = cfg.InternalClient.TLSCAFile
	}
	if cfg.InternalClient.CredentialFile != "" {
		credentials, err := readInternalCredentials(cfg.InternalClient.CredentialFile)
		if err != nil {
			return nil, err
		}
		username, password = credentials.Username, credentials.Password
		if credentials.CAFile != "" {
			caFile = credentials.CAFile
		}
	}
	if username != "" || password != "" {
		opts = append(opts, nats.UserInfo(username, password))
	}
	if cfg.Broker.TLS.Enabled {
		if caFile != "" {
			opts = append(opts, nats.RootCAs(caFile))
		}
		opts = append(opts, nats.Secure())
	}
	return nats.Connect(rawURL, opts...)
}

type internalCredentials struct {
	Username string `yaml:"username"`
	Token    string `yaml:"token"`
	Password string `yaml:"password"`
	CAFile   string `yaml:"ca_file"`
}

func readInternalCredentials(path string) (internalCredentials, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return internalCredentials{}, fmt.Errorf("stat internal client credentials: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		return internalCredentials{}, fmt.Errorf("internal client credentials must be a regular 0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return internalCredentials{}, fmt.Errorf("read internal client credentials: %w", err)
	}
	var credentials internalCredentials
	if err := yaml.Unmarshal(raw, &credentials); err != nil {
		return internalCredentials{}, fmt.Errorf("parse internal client credentials: %w", err)
	}
	credentials.Username = strings.TrimSpace(credentials.Username)
	if credentials.Password == "" {
		credentials.Password = credentials.Token
	}
	if credentials.Username == "" || credentials.Password == "" {
		return internalCredentials{}, fmt.Errorf("internal client credentials require username and token/password")
	}
	return credentials, nil
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
