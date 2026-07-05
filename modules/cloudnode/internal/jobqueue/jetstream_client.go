package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Runtime owns the CloudNode NATS connection and optional embedded server.
type Runtime struct {
	server *natsserver.Server
	nc     *nats.Conn
	js     nats.JetStreamContext
}

// Connect connects to a CloudNode JetStream runtime, starting embedded NATS when configured.
func Connect(ctx context.Context, cfg config.JetStreamConfig) (*Runtime, error) {
	if cfg.Embedded.Enabled {
		return StartEmbedded(ctx, cfg.Embedded)
	}
	return connectURL(ctx, cfg.NATSURL)
}

func connectURL(ctx context.Context, natsURL string) (*Runtime, error) {
	natsURL = strings.TrimSpace(natsURL)
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}
	timeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if until := time.Until(deadline); until > 0 && until < timeout {
			timeout = until
		}
	}
	nc, err := nats.Connect(
		natsURL,
		nats.Timeout(timeout),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(5),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats %s: %w", natsURL, err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}
	return &Runtime{nc: nc, js: js}, nil
}

// JetStream returns the underlying JetStream context.
func (r *Runtime) JetStream() nats.JetStreamContext {
	if r == nil {
		return nil
	}
	return r.js
}

// Conn returns the underlying NATS connection.
func (r *Runtime) Conn() *nats.Conn {
	if r == nil {
		return nil
	}
	return r.nc
}

// EnsureStreams creates or updates the CloudNode execution and projection streams.
func (r *Runtime) EnsureStreams(cfg config.JetStreamConfig) error {
	if r == nil || r.js == nil {
		return fmt.Errorf("jetstream runtime is not initialized")
	}
	if cfg.SubjectPrefix == "" {
		cfg.SubjectPrefix = DefaultSubjectPrefix
	}
	if err := ValidateNamingConfig(NamingConfig{SubjectPrefix: cfg.SubjectPrefix}); err != nil {
		return err
	}
	if cfg.ExecStream == "" {
		cfg.ExecStream = DefaultExecStream
	}
	if cfg.ProjectionStream == "" {
		cfg.ProjectionStream = DefaultProjectionStream
	}
	if err := ensureStream(r.js, &nats.StreamConfig{
		Name:      cfg.ExecStream,
		Subjects:  []string{ExecStreamSubject(NamingConfig{SubjectPrefix: cfg.SubjectPrefix})},
		Retention: nats.WorkQueuePolicy,
		Storage:   nats.FileStorage,
		Replicas:  1,
		MaxAge:    72 * time.Hour,
		Discard:   nats.DiscardOld,
	}); err != nil {
		return fmt.Errorf("ensure exec stream: %w", err)
	}
	if err := ensureStream(r.js, &nats.StreamConfig{
		Name:      cfg.ProjectionStream,
		Subjects:  []string{ProjectionStreamSubject(NamingConfig{SubjectPrefix: cfg.SubjectPrefix})},
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		Replicas:  1,
		MaxAge:    168 * time.Hour,
		Discard:   nats.DiscardOld,
	}); err != nil {
		return fmt.Errorf("ensure projection stream: %w", err)
	}
	return nil
}

func ensureStream(js nats.JetStreamContext, cfg *nats.StreamConfig) error {
	if _, err := js.StreamInfo(cfg.Name); err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			_, addErr := js.AddStream(cfg)
			return addErr
		}
		return err
	}
	_, err := js.UpdateStream(cfg)
	return err
}

// Close closes the connection and embedded server when present.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	if r.nc != nil {
		r.nc.Close()
	}
	if r.server != nil {
		r.server.Shutdown()
		r.server.WaitForShutdown()
	}
	return nil
}
