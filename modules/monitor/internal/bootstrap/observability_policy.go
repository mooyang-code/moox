package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	observabilityconsumer "github.com/mooyang-code/moox/modules/monitor/internal/observability/eventconsumer"
	"github.com/nats-io/nats.go"
)

func observabilityConsumerConfig(cfg config.ObservabilityConfig) (observabilityconsumer.Config, error) {
	consumer := observabilityconsumer.DefaultConfig()
	switch cfg.DeliverPolicy {
	case "all":
		consumer.DeliverPolicy = nats.DeliverAllPolicy
	case "new":
		consumer.DeliverPolicy = nats.DeliverNewPolicy
	default:
		return consumer, fmt.Errorf("observability.deliver_policy must be all or new")
	}
	return consumer, nil
}
