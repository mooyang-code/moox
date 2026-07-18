package eventbus

import (
	"context"
	"fmt"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	infraeventbus "github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	"github.com/mooyang-code/moox/packages/jetstream"
)

func NewRowsCommittedBus(ctx context.Context, cfg storageconfig.StorageEventBus) (coreeventbus.Bus, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "memory":
		return coreeventbus.NewMemoryBus(), nil
	case "nats", "jetstream":
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
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
		return infraeventbus.NewSubscriberBus(client, cfg.SubjectPrefix, infraeventbus.SubscriberOptions{
			StreamName: cfg.StreamName, AckWait: time.Duration(cfg.AckWaitMS) * time.Millisecond,
			MaxDeliver: cfg.MaxDeliver, MaxInFlight: cfg.MaxInFlight, MaxAckPending: cfg.MaxAckPending,
		})
	default:
		return nil, fmt.Errorf("unsupported storage eventbus type %s", cfg.Type)
	}
}
