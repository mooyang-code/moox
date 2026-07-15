package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dgraph-io/badger/v4"
)

type Nonces struct{ db *badger.DB }

func OpenNonces(path string) (*Nonces, error) {
	if path == "" {
		return nil, errors.New("nonce store path is required")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create nonce store: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("secure nonce store: %w", err)
	}
	opts := badger.DefaultOptions(path).WithLogger(nil).WithSyncWrites(true)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open nonce store: %w", err)
	}
	return &Nonces{db: db}, nil
}

func (nonces *Nonces) Close() error { return nonces.db.Close() }

func (nonces *Nonces) Consume(ctx context.Context, namespace, nonce string, ttl time.Duration) (bool, error) {
	if namespace == "" || nonce == "" {
		return false, errors.New("nonce namespace and value are required")
	}
	if ttl <= 0 {
		return false, errors.New("nonce TTL must be positive")
	}
	key := []byte(namespace + ":" + nonce)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		inserted := false
		err := nonces.db.Update(func(txn *badger.Txn) error {
			_, err := txn.Get(key)
			if err == nil {
				return nil
			}
			if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			if err := txn.SetEntry(badger.NewEntry(key, []byte{1}).WithTTL(ttl)); err != nil {
				return err
			}
			inserted = true
			return nil
		})
		if errors.Is(err, badger.ErrConflict) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("consume nonce: %w", err)
		}
		return inserted, nil
	}
}
