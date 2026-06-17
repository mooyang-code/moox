package transport_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	"github.com/stretchr/testify/require"
)

func TestRegistryCreatesProducerByKind(t *testing.T) {
	transport.RegisterProducerKind("fake", func(opts transport.ProducerOptions) (transport.Producer, error) {
		return &fakeProducer{opts: opts}, nil
	})

	producer, err := transport.NewProducer("fake", transport.ProducerOptions{ServerURL: "memory://test"})
	require.NoError(t, err)
	require.Equal(t, transport.ProducerOptions{ServerURL: "memory://test"}, producer.Options())
	require.NoError(t, producer.Send(context.Background(), &transport.Message{Subject: "storage.rows.changed", Data: []byte("ok")}))
}

type fakeProducer struct {
	opts transport.ProducerOptions
}

func (p *fakeProducer) Connect(context.Context) error {
	return nil
}

func (p *fakeProducer) Close() error {
	return nil
}

func (p *fakeProducer) Send(context.Context, *transport.Message) error {
	return nil
}

func (p *fakeProducer) IsConnected() bool {
	return true
}

func (p *fakeProducer) Options() transport.ProducerOptions {
	return p.opts
}
