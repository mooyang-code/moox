package eventbus

import (
	"context"
	"fmt"
	"strings"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	infraeventbus "github.com/mooyang-code/moox/modules/storage/internal/infra/eventbus"
	"github.com/mooyang-code/moox/packages/jetstream"
)

func NewRowsUpdatedBus(ctx context.Context, cfg storageconfig.StorageEventBus) (coreeventbus.Bus, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "memory":
		return coreeventbus.NewMemoryBus(), nil
	case "nats", "jetstream":
		urls := append([]string(nil), cfg.URLs...)
		if len(urls) == 0 && strings.TrimSpace(cfg.NATSURL) != "" {
			urls = []string{cfg.NATSURL}
		}
		clientCfg := jetstream.ConfigFromEnv(urls, "moox-storage")
		if cfg.CredentialFile != "" {
			if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
				return nil, err
			}
		}
		client, err := jetstream.Connect(ctx, clientCfg)
		if err != nil {
			return nil, err
		}
		return infraeventbus.NewSubscriberBus(client, cfg.SubjectPrefix), nil
	default:
		return nil, fmt.Errorf("unsupported storage eventbus type %s", cfg.Type)
	}
}
