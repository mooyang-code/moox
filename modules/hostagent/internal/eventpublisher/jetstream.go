package eventpublisher

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type JetStreamPublisher struct {
	mu        sync.RWMutex
	client    *jetstream.Client
	publisher *events.Publisher
}

func New(ctx context.Context, configPath, agentID string) (*JetStreamPublisher, error) {
	eventBusConfig, err := config.LoadEventBus(configPath)
	if err != nil {
		return nil, err
	}
	client, err := jetstream.Connect(ctx, jetstream.Config{
		URLs: eventBusConfig.URLs, Name: "moox-host-agent-" + agentID,
		Username: eventBusConfig.Username, Password: eventBusConfig.EventBusToken,
		TLSCAFile: eventBusConfig.CAFile, ReconnectBufferBytes: 0,
		ConnectTimeout: 5 * time.Second, MaxReconnects: -1,
	})
	if err != nil {
		return nil, err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &JetStreamPublisher{client: client, publisher: publisher}, nil
}

func (p *JetStreamPublisher) PublishHostMetric(ctx context.Context, messageID string, metric *hostmetricpb.HostMetric, occurredAt time.Time) error {
	if p == nil || metric == nil {
		return errors.New("host metric publisher is not initialized")
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.publisher == nil {
		return errors.New("host metric publisher is not initialized")
	}
	_, err := p.publisher.Publish(ctx, events.ObservabilityHostSnapshotReported, metric, events.PublishOptions{
		EventID: messageID, OccurredAt: occurredAt, SpaceID: "moox_system", SubjectID: metric.GetAgentId(),
	})
	return err
}

func (p *JetStreamPublisher) Ready() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.client != nil && p.client.Ready()
}

func (p *JetStreamPublisher) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var err error
	if p.client != nil {
		err = p.client.Close()
	}
	p.client = nil
	p.publisher = nil
	return err
}
