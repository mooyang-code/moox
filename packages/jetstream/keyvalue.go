package jetstream

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

var (
	ErrKVKeyNotFound = errors.New("jetstream kv key not found")
	ErrKVKeyExists   = errors.New("jetstream kv key exists")
	ErrKVNoKeys      = errors.New("jetstream kv has no keys")
)

// KVEntry is the context-aware, copy-safe representation returned by KVStore.
type KVEntry struct {
	Value    []byte
	Revision uint64
}

// KVStore is the bind-only API for an EventBus-owned JetStream KV bucket.
// Operations accept contexts so callers cannot accidentally block forever.
type KVStore interface {
	Create(ctx context.Context, key string, value []byte) (uint64, error)
	Get(ctx context.Context, key string) (*KVEntry, error)
	Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)
	Keys(ctx context.Context) ([]string, error)
}

type kvStoreAdapter struct{ kv nats.KeyValue }

func (k kvStoreAdapter) Create(ctx context.Context, key string, value []byte) (uint64, error) {
	if err := contextErr(ctx, "before kv create"); err != nil {
		return 0, err
	}
	revision, err := k.kv.Create(key, append([]byte(nil), value...))
	return revision, mapKVError(err)
}

func (k kvStoreAdapter) Get(ctx context.Context, key string) (*KVEntry, error) {
	if err := contextErr(ctx, "before kv get"); err != nil {
		return nil, err
	}
	entry, err := k.kv.Get(key)
	if err != nil {
		return nil, mapKVError(err)
	}
	return &KVEntry{Value: append([]byte(nil), entry.Value()...), Revision: entry.Revision()}, nil
}

func (k kvStoreAdapter) Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error) {
	if err := contextErr(ctx, "before kv update"); err != nil {
		return 0, err
	}
	n, err := k.kv.Update(key, append([]byte(nil), value...), revision)
	return n, mapKVError(err)
}

func (k kvStoreAdapter) Keys(ctx context.Context) ([]string, error) {
	if err := contextErr(ctx, "before kv keys"); err != nil {
		return nil, err
	}
	keys, err := k.kv.Keys()
	return append([]string(nil), keys...), mapKVError(err)
}

func mapKVError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, nats.ErrKeyNotFound), errors.Is(err, nats.ErrKeyDeleted):
		return fmt.Errorf("%w: %v", ErrKVKeyNotFound, err)
	case errors.Is(err, nats.ErrKeyExists):
		return fmt.Errorf("%w: %v", ErrKVKeyExists, err)
	case errors.Is(err, nats.ErrNoKeysFound):
		return fmt.Errorf("%w: %v", ErrKVNoKeys, err)
	default:
		return err
	}
}

// BindKV opens an existing bucket without creating or reconciling it. EventBus owns KV lifecycle.
func (c *Client) BindKV(ctx context.Context, bucket string) (KVStore, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	js, err := c.jetStream()
	if err != nil {
		return nil, err
	}
	kv, err := js.KeyValue(bucket)
	if err != nil {
		return nil, mapKVError(err)
	}
	return kvStoreAdapter{kv: kv}, nil
}
