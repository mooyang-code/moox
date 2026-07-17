package jetstream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

var (
	ErrKVKeyNotFound = errors.New("jetstream kv key not found")
	ErrKVKeyExists   = errors.New("jetstream kv key exists")
	ErrKVNoKeys      = errors.New("jetstream kv has no keys")
)

// LegacyKVEntry preserves the synchronous adapter used by existing CloudNode
// code. New callers should use KVStore and KVEntry below.
type LegacyKVEntry interface {
	Value() []byte
	Revision() uint64
}
type KeyValue interface {
	Create(key string, value []byte) (uint64, error)
	Get(key string) (LegacyKVEntry, error)
	Update(key string, value []byte, revision uint64) (uint64, error)
	Keys() ([]string, error)
}

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

type keyValueAdapter struct{ kv nats.KeyValue }

type kvStoreAdapter struct{ kv nats.KeyValue }

func (k keyValueAdapter) Create(key string, value []byte) (uint64, error) {
	rev, err := k.kv.Create(key, value)
	return rev, mapKVError(err)
}
func (k keyValueAdapter) Get(key string) (LegacyKVEntry, error) {
	entry, err := k.kv.Get(key)
	if err != nil {
		return nil, mapKVError(err)
	}
	return entry, nil
}
func (k keyValueAdapter) Update(key string, value []byte, rev uint64) (uint64, error) {
	n, err := k.kv.Update(key, value, rev)
	return n, mapKVError(err)
}
func (k keyValueAdapter) Keys() ([]string, error) {
	keys, err := k.kv.Keys()
	return keys, mapKVError(err)
}

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

func (c *Client) KeyValue(bucket string) (KeyValue, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	kv, err := c.js.KeyValue(bucket)
	if err != nil {
		return nil, err
	}
	return keyValueAdapter{kv: kv}, nil
}

// BindKV opens an existing bucket without creating or reconciling it. EventBus owns KV lifecycle.
func (c *Client) BindKV(ctx context.Context, bucket string) (KVStore, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.alive(); err != nil {
		return nil, err
	}
	kv, err := c.js.KeyValue(bucket)
	if err != nil {
		return nil, mapKVError(err)
	}
	return kvStoreAdapter{kv: kv}, nil
}

func (c *Client) CreateKeyValue(bucket string, ttl time.Duration) (KeyValue, error) {
	if err := c.alive(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	kv, err := c.js.CreateKeyValue(&nats.KeyValueConfig{Bucket: bucket, Storage: nats.FileStorage, History: 1, TTL: ttl})
	if err != nil {
		return nil, err
	}
	return keyValueAdapter{kv: kv}, nil
}
