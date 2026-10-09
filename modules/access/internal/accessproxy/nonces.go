package accessproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteNonces struct {
	db *sql.DB
}

func OpenSQLiteNonces(path string) (*SQLiteNonces, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("nonce 存储路径不能为空")
	}
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("创建 nonce 存储目录: %w", err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				return nil, fmt.Errorf("设置 nonce 存储目录权限: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 nonce 存储: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("配置 nonce 存储: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gateway_nonces (
		namespace TEXT NOT NULL,
		nonce TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		PRIMARY KEY(namespace, nonce)
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("创建 nonce 表: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_gateway_nonces_expires_at ON gateway_nonces(expires_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("创建 nonce 过期索引: %w", err)
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("设置 nonce 存储文件权限: %w", err)
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
		return false, errors.New("nonce 存储不可用")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(nonce) == "" || ttl <= 0 {
		return false, errors.New("nonce 的命名空间、取值和有效期都不能为空")
	}
	tx, err := n.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("开始 nonce 事务: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UnixNano()
	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_nonces WHERE expires_at <= ?`, now); err != nil {
		return false, fmt.Errorf("清理过期 nonce: %w", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gateway_nonces(namespace, nonce, expires_at) VALUES(?, ?, ?)`, namespace, nonce, time.Now().Add(ttl).UnixNano())
	if err != nil {
		return false, fmt.Errorf("写入 nonce: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交 nonce: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取 nonce 写入结果: %w", err)
	}
	return rows == 1, nil
}
