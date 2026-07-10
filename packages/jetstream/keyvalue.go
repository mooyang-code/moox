package jetstream

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

var (
	ErrKVKeyNotFound = errors.New("jetstream kv key not found")
	ErrKVKeyExists   = errors.New("jetstream kv key exists")
	ErrKVNoKeys      = errors.New("jetstream kv has no keys")
)

type KVEntry interface {
	Value() []byte
	Revision() uint64
}
type KeyValue interface {
	Create(key string, value []byte) (uint64, error)
	Get(key string) (KVEntry, error)
	Update(key string, value []byte, revision uint64) (uint64, error)
	Keys() ([]string, error)
}

type keyValueAdapter struct{ kv nats.KeyValue }

func (k keyValueAdapter) Create(key string, value []byte) (uint64, error) {
	rev, err := k.kv.Create(key, value)
	return rev, mapKVError(err)
}
func (k keyValueAdapter) Get(key string) (KVEntry, error) {
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
