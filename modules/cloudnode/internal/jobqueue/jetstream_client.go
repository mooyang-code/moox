package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// Runtime owns a connection to the centrally managed moox-eventbus service.
// It never creates Streams, consumers, or KV buckets.
type Runtime struct {
	client     *jetstream.Client
	extraClose func() error
}

func Connect(ctx context.Context, cfg config.JetStreamConfig) (*Runtime, error) {
	urls := append([]string(nil), cfg.URLs...)
	if len(urls) == 0 && strings.TrimSpace(cfg.NATSURL) != "" {
		urls = []string{cfg.NATSURL}
	}
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: urls, Name: "moox-cloudnode"})
	if err != nil {
		return nil, err
	}
	return &Runtime{client: client}, nil
}

func (r *Runtime) Client() *jetstream.Client {
	if r == nil {
		return nil
	}
	return r.client
}

// JetStream is retained as a compatibility alias for tests and callers that
// only need the shared client; it does not expose raw NATS ownership.
func (r *Runtime) JetStream() *jetstream.Client { return r.Client() }
func (r *Runtime) SetCloseHook(fn func() error) {
	if r != nil {
		r.extraClose = fn
	}
}
func (r *Runtime) EnsureStreams(_ config.JetStreamConfig, _ config.JobItemConfig) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("jetstream runtime is not initialized")
	}
	return nil
}
func (r *Runtime) KeyValue(bucket string) (jetstream.KeyValue, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("jetstream runtime is not initialized")
	}
	return r.client.KeyValue(bucket)
}
func (r *Runtime) Close() error {
	if r == nil || r.client == nil {
		return nil
	}
	err := r.client.Close()
	if r.extraClose != nil {
		err = errors.Join(err, r.extraClose())
	}
	return err
}
