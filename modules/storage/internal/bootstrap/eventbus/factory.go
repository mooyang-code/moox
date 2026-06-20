package eventbus

import (
	"context"
	"fmt"
	"strings"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	infraeventbus "github.com/mooyang-code/moox/modules/storage/internal/infra/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	_ "github.com/mooyang-code/moox/modules/storage/internal/infra/transport/nats"
)

func NewRowsChangedBus(ctx context.Context, cfg storageconfig.StorageEventBus) (coreeventbus.Bus, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "memory":
		return coreeventbus.NewMemoryBus(), nil
	case "nats":
		defaultSubject := infraeventbus.RowsChangedSubject(cfg.SubjectPrefix)
		subject := cfg.RowsChangedSubject
		if subject == "" {
			subject = defaultSubject
		}
		streamSubject := infraeventbus.SubjectPrefixWildcard(cfg.SubjectPrefix)
		if subject != defaultSubject {
			streamSubject = subject
		}
		producer, err := transport.NewProducer(transport.ProducerKindNATS, transport.ProducerOptions{
			ServerURL:      cfg.NATSURL,
			ConnectTimeout: 10 * time.Second,
			StreamName:     cfg.StreamName,
			StreamSubjects: []string{streamSubject},
			ConsumerName:   cfg.ConsumerName,
		})
		if err != nil {
			return nil, err
		}
		if err := producer.Connect(ctx); err != nil {
			return nil, err
		}
		pubsub, ok := producer.(infraeventbus.PubSub)
		if !ok {
			_ = producer.Close()
			return nil, fmt.Errorf("storage eventbus type %s does not support subscription", cfg.Type)
		}
		return infraeventbus.NewSubscriberBus(pubsub, subject), nil
	default:
		return nil, fmt.Errorf("unsupported storage eventbus type %s", cfg.Type)
	}
}
