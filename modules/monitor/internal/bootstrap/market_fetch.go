package bootstrap

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monmarketfetch "github.com/mooyang-code/moox/modules/monitor/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

func startMarketFetchConsumer(ctx context.Context, cfg *config.Config, runtime *Runtime) {
	if cfg == nil || runtime == nil || !cfg.Observability.Enabled {
		return
	}
	store := monmarketfetch.NewStore()
	runtime.MarketFetchStore = store
	jsCfg := jetstream.ConfigFromEnv(cfg.Observability.EventBusURLs, "moox-monitor-market-fetch")
	if path := strings.TrimSpace(cfg.Observability.CredentialFile); path != "" {
		if err := jsCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(path)); err != nil {
			log.WarnContextf(ctx, "market fetch consumer credential unavailable: %v", err)
			return
		}
	}
	client, err := jetstream.Connect(ctx, jsCfg)
	if err != nil {
		log.WarnContextf(ctx, "market fetch consumer EventBus unavailable: %v", err)
		return
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		client.Close()
		return
	}
	if err := monmarketfetch.Start(ctx, client, registry, store); err != nil {
		client.Close()
		log.WarnContextf(ctx, "market fetch consumer disabled: %v", err)
		return
	}
	runtime.Go(func() { <-ctx.Done(); _ = client.Close() })
}
