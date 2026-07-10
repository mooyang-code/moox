package jobqueue

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// Runtime owns a connection to the centrally managed moox-eventbus service.
// It never creates Streams, consumers, or KV buckets.
type Runtime struct{ client *jetstream.Client }

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
	return r.client.Close()
}
