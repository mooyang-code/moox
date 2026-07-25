package jobqueue

import (
	"context"
	"errors"
	"fmt"

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
	clientCfg := jetstream.ConfigFromEnv(urls, "moox-cloudnode")
	if cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
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
