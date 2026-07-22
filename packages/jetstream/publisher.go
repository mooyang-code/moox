package jetstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

type publishOptions struct {
	orderingKey string
}

// PublishOption changes transport metadata without changing the message contract.
type PublishOption func(*publishOptions)

// WithOrderingKey attaches an ordering hint; it does not make PublishBatch ordered.
func WithOrderingKey(key string) PublishOption {
	return func(opts *publishOptions) {
		opts.orderingKey = strings.TrimSpace(key)
	}
}

type PublishAck struct {
	Stream    string
	Sequence  uint64
	Duplicate bool
}

// PublishResult is the result for one input to PublishBatch.
type PublishResult struct {
	Ack *PublishAck
	Err error
}

func (c *Client) Publish(ctx context.Context, msg *messagepb.MooxMessage, opts ...PublishOption) (*PublishAck, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrPublishTimeout, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	if err := c.alive(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	var options publishOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	raw, err := marshalMessage(msg, c.cfg.MaxPayload)
	if err != nil {
		return nil, err
	}
	natsMsg := &nats.Msg{Subject: msg.GetTopic(), Header: nats.Header{}, Data: raw}
	natsMsg.Header.Set(nats.MsgIdHdr, msg.GetMessageId())
	natsMsg.Header.Set("Content-Type", OuterContentType)
	if options.orderingKey != "" {
		natsMsg.Header.Set("Moox-Ordering-Key", options.orderingKey)
	}
	ack, err := c.js.PublishMsg(natsMsg, nats.Context(ctx))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("%w: %w", ErrConnection, ctx.Err())
		}
		if errors.Is(err, nats.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrPublishTimeout, err)
		}
		return nil, fmt.Errorf("%w: publish %s: %w", ErrConnection, msg.GetTopic(), err)
	}
	return &PublishAck{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// PublishRaw publishes a caller-owned protobuf envelope without making the
// transport package depend on a business event schema. packages/events uses
// this boundary for EventMessage; business modules must not call it directly.
func (c *Client) PublishRaw(ctx context.Context, subject, messageID string, payload []byte, contentType string) (*PublishAck, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	if err := c.alive(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	if err := validateSubject(subject); err != nil {
		return nil, fmt.Errorf("%w: subject: %v", ErrInvalidMessage, err)
	}
	if strings.TrimSpace(messageID) == "" {
		return nil, fmt.Errorf("%w: message_id is required", ErrInvalidMessage)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: payload is required", ErrInvalidMessage)
	}
	if c.cfg.MaxPayload > 0 && len(payload) > c.cfg.MaxPayload {
		return nil, fmt.Errorf("%w: payload size %d exceeds %d", ErrInvalidMessage, len(payload), c.cfg.MaxPayload)
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return nil, fmt.Errorf("%w: content_type is required", ErrInvalidMessage)
	}
	natsMsg := &nats.Msg{Subject: subject, Header: nats.Header{}, Data: append([]byte(nil), payload...)}
	natsMsg.Header.Set(nats.MsgIdHdr, messageID)
	natsMsg.Header.Set("Content-Type", contentType)
	ack, err := c.js.PublishMsg(natsMsg, nats.Context(ctx))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, fmt.Errorf("%w: %w", ErrConnection, ctx.Err())
		}
		if errors.Is(err, nats.ErrTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrPublishTimeout, err)
		}
		return nil, fmt.Errorf("%w: publish %s: %w", ErrConnection, subject, err)
	}
	return &PublishAck{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// PublishBatch starts one independent JetStream publication per message and
// waits for each individual result. results[i] always belongs to messages[i].
// Bounded concurrency makes broker publication and acknowledgement completion
// order unspecified; this method does not guarantee JetStream sequence order,
// especially across different subjects or targets. Callers must not use it to
// impose cross-instrument ordering.
func (c *Client) PublishBatch(ctx context.Context, messages []*messagepb.MooxMessage, opts ...PublishOption) []PublishResult {
	batchConcurrency := 0
	if c != nil {
		batchConcurrency = c.cfg.BatchConcurrency
	}
	return publishBatch(ctx, messages, opts, batchConcurrency, c.Publish)
}

func publishBatch(ctx context.Context, messages []*messagepb.MooxMessage, opts []PublishOption, batchConcurrency int, publish func(context.Context, *messagepb.MooxMessage, ...PublishOption) (*PublishAck, error)) []PublishResult {
	results := make([]PublishResult, len(messages))
	if len(messages) == 0 {
		return results
	}
	concurrency := 64
	if batchConcurrency > 0 {
		concurrency = batchConcurrency
	}
	if concurrency > maxBatchConcurrency {
		concurrency = maxBatchConcurrency
	}
	if concurrency > len(messages) {
		concurrency = len(messages)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index].Ack, results[index].Err = publish(ctx, messages[index], opts...)
			}
		}()
	}
	for index := range messages {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}
