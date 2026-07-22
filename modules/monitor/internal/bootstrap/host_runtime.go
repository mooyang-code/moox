package bootstrap

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

func startHostStorageGate(ctx context.Context, cfg *config.Config, runtime *Runtime, gate *hostmetrics.StorageGate) {
	if cfg == nil || gate == nil || !cfg.Metrics.HostStorage.Enabled {
		return
	}
	interval := cfg.Metrics.HostStorage.MetadataRefreshInterval
	if interval <= 0 {
		interval = time.Minute
	}
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()
		if err := gate.Validate(checkCtx); err != nil {
			log.WarnContextf(ctx, "host storage schema check failed: %v", err)
		}
	}
	runtime.Go(func() {
		check()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	})
}

func startHostMetricsConsumer(ctx context.Context, cfg *config.Config, runtime *Runtime, store *hostmetrics.Store) {
	if cfg == nil || !cfg.Metrics.Enabled || !cfg.Metrics.HostStorage.Enabled || store == nil {
		return
	}
	runtime.Go(func() {
		for ctx.Err() == nil {
			urls := strings.Split(cfg.Metrics.EventBusURL, ",")
			jc := jetstream.ConfigFromEnv(urls, "moox-monitor-hostmetrics")
			if path := strings.TrimSpace(cfg.Metrics.HostEventBusCredentialFile); path != "" {
				if raw, err := jetstream.LoadCredentialFile(jetstream.ExpandCredentialPath(path)); err == nil {
					jc.Username, jc.Password, jc.TLSCAFile = raw.Username, raw.Password, raw.CAFile
				} else {
					log.WarnContextf(ctx, "host metrics credential unavailable: %v", err)
					waitHostMetrics(ctx)
					continue
				}
			}
			client, err := jetstream.Connect(ctx, jc)
			if err != nil {
				log.WarnContextf(ctx, "host metrics eventbus unavailable: %v", err)
				waitHostMetrics(ctx)
				continue
			}
			consumer, err := hostmetrics.BindWithDLQ(ctx, client, store, hostmetrics.NewDLQPublisher(client))
			if err != nil {
				_ = client.Close()
				log.WarnContextf(ctx, "host metrics durable unavailable: %v", err)
				waitHostMetrics(ctx)
				continue
			}
			err = consumer.Run(ctx)
			_ = consumer.Close()
			_ = client.Close()
			if err != nil && ctx.Err() == nil {
				log.WarnContextf(ctx, "host metrics consumer stopped: %v", err)
				waitHostMetrics(ctx)
			}
		}
	})
}

func waitHostMetrics(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(15 * time.Second):
	}
}

func normalizeHostStorageTarget(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:20102"
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	return "ip://" + raw
}
