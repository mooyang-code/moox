package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// Nonces 记录已经用过的签名 nonce，用来拒绝重放的请求。
type Nonces struct{ db *badger.DB }

// OpenNonces 打开 nonce 存储，目录只允许属主访问。
func OpenNonces(path string) (*Nonces, error) {
	if path == "" {
		return nil, errors.New("nonce 存储目录不能为空")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("创建 nonce 存储目录: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("设置 nonce 存储目录权限: %w", err)
	}
	opts := badger.DefaultOptions(path).WithLogger(nil).WithSyncWrites(true)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("打开 nonce 存储: %w", err)
	}
	return &Nonces{db: db}, nil
}

// Close 关闭 nonce 存储。
func (nonces *Nonces) Close() error { return nonces.db.Close() }

// Check 检查 nonce 存储是否可读，供就绪检查使用。
func (nonces *Nonces) Check() error {
	return nonces.db.View(func(_ *badger.Txn) error { return nil })
}

// Consume 登记一个 nonce；返回 false 表示它已经用过，即请求被重放。
func (nonces *Nonces) Consume(ctx context.Context, namespace, nonce string, ttl time.Duration) (bool, error) {
	if namespace == "" || nonce == "" {
		return false, errors.New("nonce 的命名空间和取值不能为空")
	}
	if ttl <= 0 {
		return false, errors.New("nonce 的有效期必须大于 0")
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
			return false, fmt.Errorf("登记 nonce: %w", err)
		}
		return inserted, nil
	}
}
