package accessproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteNonces struct {
	db *sql.DB
	// lastPrune 是上次清理过期记录的时间（UnixNano）：清理按间隔、分批进行，不在每次请求的串行事务里删除全部过期记录。
	lastPrune atomic.Int64
}

const (
	noncePruneInterval = time.Second
	noncePruneBatch    = 500
)

func OpenSQLiteNonces(path string) (*SQLiteNonces, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("nonce store path is required")
	}
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create nonce store directory: %w", err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				return nil, fmt.Errorf("secure nonce store directory: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open nonce store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure nonce store: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gateway_nonces (
		namespace TEXT NOT NULL,
		nonce TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		PRIMARY KEY(namespace, nonce)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create nonce store schema: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_gateway_nonces_expires_at ON gateway_nonces(expires_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create nonce store expiry index: %w", err)
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("secure nonce store file: %w", err)
		}
	}
	return &SQLiteNonces{db: db}, nil
}

func (n *SQLiteNonces) Close() error {
	if n == nil || n.db == nil {
		return nil
	}
	return n.db.Close()
}

func (n *SQLiteNonces) Consume(ctx context.Context, namespace, nonce string, ttl time.Duration) (bool, error) {
	if n == nil || n.db == nil {
		return false, errors.New("nonce store is unavailable")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(nonce) == "" || ttl <= 0 {
		return false, errors.New("nonce namespace, value and positive TTL are required")
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin nonce transaction: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UnixNano()
	// 过期记录按间隔、每次最多一批地清理，删除不再随每个请求发生；未清掉的过期记录由下面的插入语句直接复用。
	if last := n.lastPrune.Load(); now-last >= int64(noncePruneInterval) && n.lastPrune.CompareAndSwap(last, now) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_nonces WHERE rowid IN (SELECT rowid FROM gateway_nonces WHERE expires_at <= ? LIMIT ?)`, now, noncePruneBatch); err != nil {
			return false, fmt.Errorf("prune expired nonces: %w", err)
		}
	}
	// 同一个 nonce 仅在旧记录已过期时才可再次使用；仍有效的记录冲突后不更新（影响行数为 0，即重放）。
	result, err := tx.ExecContext(ctx, `INSERT INTO gateway_nonces(namespace, nonce, expires_at) VALUES(?, ?, ?)
		ON CONFLICT(namespace, nonce) DO UPDATE SET expires_at = excluded.expires_at WHERE gateway_nonces.expires_at <= ?`,
		namespace, nonce, time.Now().Add(ttl).UnixNano(), now)
	if err != nil {
		return false, fmt.Errorf("insert nonce: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit nonce: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read nonce result: %w", err)
	}
	return rows == 1, nil
}
