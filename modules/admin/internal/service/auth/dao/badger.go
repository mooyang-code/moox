package dao

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"trpc.group/trpc-go/trpc-go/log"
)

// CacheDB BadgerDB封装，提供缓存操作
type CacheDB struct {
	db    *badger.DB
	locks sync.Map // 进程内锁管理
}

// NewCacheDB 创建BadgerDB实例
func NewCacheDB(dataDir string) (*CacheDB, error) {
	opts := badger.DefaultOptions(dataDir).
		WithLogger(nil). // 禁用默认日志，使用trpc日志
		WithLoggingLevel(badger.WARNING)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}

	cdb := &CacheDB{
		db: db,
	}

	// 启动垃圾回收
	go cdb.runGC()

	return cdb, nil
}

// NewCacheDBFromBadger 从现有 BadgerDB 实例创建 CacheDB（用于与 database.Manager 集成）
func NewCacheDBFromBadger(db *badger.DB) (*CacheDB, error) {
	if db == nil {
		return nil, fmt.Errorf("badger db is nil")
	}

	cdb := &CacheDB{
		db: db,
	}

	// 启动垃圾回收
	go cdb.runGC()

	return cdb, nil
}

// Close 关闭数据库
func (c *CacheDB) Close() error {
	return c.db.Close()
}

// Set 设置键值对，支持TTL
func (c *CacheDB) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.db.Update(func(txn *badger.Txn) error {
		entry := badger.NewEntry([]byte(key), []byte(value))
		if ttl > 0 {
			entry = entry.WithTTL(ttl)
		}
		return txn.SetEntry(entry)
	})
}

// Get 获取值
func (c *CacheDB) Get(ctx context.Context, key string) (string, error) {
	var value string
	err := c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			value = string(val)
			return nil
		})
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return "", ErrKeyNotFound
	}
	return value, err
}

// Del 删除键
func (c *CacheDB) Del(ctx context.Context, keys ...string) error {
	return c.db.Update(func(txn *badger.Txn) error {
		for _, key := range keys {
			if err := txn.Delete([]byte(key)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Exists 检查键是否存在
func (c *CacheDB) Exists(ctx context.Context, key string) (bool, error) {
	err := c.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(key))
		return err
	})
	if errors.Is(err, badger.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Expire 设置键的过期时间
func (c *CacheDB) Expire(ctx context.Context, key string, ttl time.Duration) error {
	return c.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}

		var value []byte
		err = item.Value(func(val []byte) error {
			value = make([]byte, len(val))
			copy(value, val)
			return nil
		})
		if err != nil {
			return err
		}

		entry := badger.NewEntry([]byte(key), value).WithTTL(ttl)
		return txn.SetEntry(entry)
	})
}

// TTL 获取键的剩余过期时间
func (c *CacheDB) TTL(ctx context.Context, key string) (time.Duration, error) {
	var ttl time.Duration
	err := c.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		if err != nil {
			return err
		}

		expiresAt := item.ExpiresAt()
		if expiresAt == 0 {
			ttl = -1 // 永不过期
			return nil
		}

		remaining := time.Unix(int64(expiresAt), 0).Sub(time.Now())
		if remaining <= 0 {
			ttl = 0 // 已过期
		} else {
			ttl = remaining
		}
		return nil
	})

	if errors.Is(err, badger.ErrKeyNotFound) {
		return -2, nil // 键不存在
	}
	return ttl, err
}

// Lock 实现分布式锁
func (c *CacheDB) Lock(ctx context.Context, key string, ttl time.Duration) bool {
	lockKey := "lock:" + key

	// 尝试获取锁
	err := c.db.Update(func(txn *badger.Txn) error {
		// 检查锁是否已存在
		_, err := txn.Get([]byte(lockKey))
		if err == nil {
			return fmt.Errorf("lock already exists")
		}
		if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}

		// 设置锁
		entry := badger.NewEntry([]byte(lockKey), []byte("1"))
		if ttl > 0 {
			entry = entry.WithTTL(ttl)
		}
		return txn.SetEntry(entry)
	})

	return err == nil
}

// Unlock 释放锁
func (c *CacheDB) Unlock(ctx context.Context, key string) error {
	lockKey := "lock:" + key
	return c.Del(ctx, lockKey)
}

// Scan 扫描键，支持前缀匹配
func (c *CacheDB) Scan(ctx context.Context, prefix string, limit int) ([]string, error) {
	var keys []string

	err := c.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()

		prefixBytes := []byte(prefix)
		count := 0

		for it.Seek(prefixBytes); it.ValidForPrefix(prefixBytes) && (limit == 0 || count < limit); it.Next() {
			item := it.Item()
			key := string(item.Key())
			keys = append(keys, key)
			count++
		}
		return nil
	})

	return keys, err
}

// BatchSet 批量设置
func (c *CacheDB) BatchSet(ctx context.Context, kvs map[string]string, ttl time.Duration) error {
	return c.db.Update(func(txn *badger.Txn) error {
		for key, value := range kvs {
			entry := badger.NewEntry([]byte(key), []byte(value))
			if ttl > 0 {
				entry = entry.WithTTL(ttl)
			}
			if err := txn.SetEntry(entry); err != nil {
				return err
			}
		}
		return nil
	})
}

// BatchGet 批量获取
func (c *CacheDB) BatchGet(ctx context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string)

	err := c.db.View(func(txn *badger.Txn) error {
		for _, key := range keys {
			item, err := txn.Get([]byte(key))
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}

			err = item.Value(func(val []byte) error {
				result[key] = string(val)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	return result, err
}

// Incr 自增
func (c *CacheDB) Incr(ctx context.Context, key string) (int64, error) {
	var newValue int64

	err := c.db.Update(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(key))
		var currentValue int64 = 0

		if err == nil {
			err = item.Value(func(val []byte) error {
				if len(val) > 0 {
					_, parseErr := fmt.Sscanf(string(val), "%d", &currentValue)
					return parseErr
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}

		newValue = currentValue + 1
		entry := badger.NewEntry([]byte(key), []byte(fmt.Sprintf("%d", newValue)))
		return txn.SetEntry(entry)
	})

	return newValue, err
}

// runGC 运行垃圾回收
func (c *CacheDB) runGC() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		err := c.db.RunValueLogGC(0.7)
		if err != nil {
			// 这是正常的，当没有需要回收的数据时会返回错误
			log.Debugf("[Auth] GC completed: %v", err)
		}
	}
}

// Ping 测试连接
func (c *CacheDB) Ping(ctx context.Context) error {
	return c.Set(ctx, "ping", "pong", time.Second)
}

// 错误定义
var (
	ErrKeyNotFound = fmt.Errorf("key not found")
	ErrLockFailed  = fmt.Errorf("failed to acquire lock")
)
