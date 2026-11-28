// Package store owns the single SQLite persistence boundary for Trade.
package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/cache"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormLogger 保留 GORM 的慢查询和出错日志，去掉 ErrRecordNotFound（仓储把它当普通的“没有”返回，
// 例如组合还没有目标）和终端颜色。ParameterizedQueries 让日志里的 SQL 只留占位符，账户配置不随语句写进日志。
var gormLogger = logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
	SlowThreshold:             200 * time.Millisecond,
	LogLevel:                  logger.Warn,
	IgnoreRecordNotFoundError: true,
	ParameterizedQueries:      true,
})

const (
	sqliteBusyTimeoutMS = 5000
	// readOnlyConns 是只读连接池的连接数：管理台几个页签同时查询够用。
	readOnlyConns = 4
)

var (
	ErrConflict           = errors.New("数据已经被修改，刷新后再试")
	ErrInvalidRecord      = errors.New("数据不合法")
	ErrIncompatibleSchema = sqlitedb.ErrIncompatibleSchema
)

type Store struct {
	// db 是这个 Store 的连接：Open 返回的 Store 是唯一的读写连接，交易执行的读写都走它；
	// 只读视图（见 ReadOnly）是同一个库文件上的只读连接池。
	db *gorm.DB
	// readOnly 是只读视图；内存库没有单独的只读连接，为空，ReadOnly 返回自己。
	readOnly              *Store
	accountLocks          sync.Map
	logicalLocks          sync.Map
	executionLocks        sync.Map
	logicalMembershipLock sync.Mutex
	metrics               *report.ModuleMetrics

	// fundWarned 记录资金设置告警的时间（组合 -> 时间），同一个组合一小时只告警一次。
	fundWarnMu sync.Mutex
	fundWarned map[string]time.Time

	// 很少变化、写入点明确的数据缓存在内存里：标的定义按场所缓存整份，账户身份按账户缓存。
	// 标的和账户的写入都会清掉对应缓存，下单时不必每笔都查库。只读视图和读写连接共用同一份缓存，
	// 写入清缓存时两边一起清。
	instrumentSets *cache.Memo[instrumentScope, *instrumentSet]
	identities     *cache.Memo[accountKey, tradingAccountIdentity]
}

// ReadOnly 返回只读视图：同一个库文件上的只读连接池，管理台的查询接口经它读库。WAL 模式下读不挡写，
// 管理台的查询不再和交易执行的写入排同一个读写连接。只读视图看得到所有已提交的数据，看不到还没提交的事务，
// 所以交易执行路径和写接口的读取仍走读写连接。经只读视图写库会被 SQLite 拒绝；它的账户锁、组合锁和读写连接
// 不是同一套，不能经它加锁。内存库没有单独的只读连接，返回自己。
func (s *Store) ReadOnly() *Store {
	if s.readOnly == nil {
		return s
	}
	return s.readOnly
}

// InvalidateCaches 清空内存缓存，用于绕过存储层直接改表之后（例如测试直接改表）。
func (s *Store) InvalidateCaches() {
	if s == nil {
		return
	}
	s.instrumentSets.Invalidate()
	s.identities.Invalidate()
}

func (s *Store) SetModuleMetrics(metrics *report.ModuleMetrics) {
	if s != nil {
		s.metrics = metrics
	}
}

func (s *Store) ModuleMetrics() *report.ModuleMetrics {
	if s == nil {
		return nil
	}
	return s.metrics
}

type Tx struct {
	db    *gorm.DB
	store *Store
	// 本事务写过标的或账户时，读取直接查库：未提交的数据不能进缓存。
	instrumentsWritten bool
	accountsWritten    bool
}

func Open(path string) (*Store, error) {
	db, err := sqlitedb.OpenGORM(sqlitedb.Options{
		Path:          path,
		BusyTimeoutMS: sqliteBusyTimeoutMS,
		Logger:        gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("打开交易数据库失败：%w", err)
	}
	if err := sqlitedb.EnsureSchemaGORM(db, path, "trade", schema.AllSQL(), dataformat.LifecycleParts(), lifecycle.StateFileForDB(path)); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, err
	}
	s := &Store{
		db:             db,
		instrumentSets: &cache.Memo[instrumentScope, *instrumentSet]{},
		identities:     &cache.Memo[accountKey, tradingAccountIdentity]{},
	}
	// 内存库每个连接各是一个独立的库，只读查询直接用读写连接。
	if sqlitedb.IsMemoryPath(path) {
		return s, nil
	}
	readDB, err := sqlitedb.OpenGORM(sqlitedb.Options{
		Path:          path,
		ReadOnly:      true,
		BusyTimeoutMS: sqliteBusyTimeoutMS,
		MaxOpenConns:  readOnlyConns,
		MaxIdleConns:  readOnlyConns,
		Logger:        gormLogger,
	})
	if err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, fmt.Errorf("打开交易库只读连接失败：%w", err)
	}
	s.readOnly = &Store{db: readDB, instrumentSets: s.instrumentSets, identities: s.identities}
	return s, nil
}

// A background scan can defer a busy account without waiting behind its sync.
func (s *Store) TryLockTradingAccount(tradingAccountID string) (func(), bool) {
	value, _ := s.accountLocks.LoadOrStore(tradingAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	if !mutex.TryLock() {
		return nil, false
	}
	return mutex.Unlock, true
}

func (s *Store) LockLogicalAccount(spaceID string, logicalAccountID string) func() {
	key := spaceID + "\x00" + logicalAccountID
	value, _ := s.logicalLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (s *Store) LockLogicalAccountContext(ctx context.Context, spaceID, logicalAccountID string) (func(), error) {
	value, _ := s.logicalLocks.LoadOrStore(spaceID+"\x00"+logicalAccountID, &sync.Mutex{})
	return lockContext(ctx, value.(*sync.Mutex))
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	if s.readOnly == nil {
		return sqlDB.Close()
	}
	readDB, err := s.readOnly.db.DB()
	if err != nil {
		return err
	}
	// 先关只读连接，主连接最后关闭时才能做收尾的检查点。
	return errors.Join(readDB.Close(), sqlDB.Close())
}

func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

func (s *Store) Transaction(ctx context.Context, fn func(*Tx) error) error {
	if fn == nil {
		return fmt.Errorf("%w: nil transaction callback", ErrInvalidRecord)
	}
	var written *Tx
	err := s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		tx := &Tx{db: db, store: s}
		written = tx
		return fn(tx)
	})
	// 提交或回滚之后再清一次：事务内写入时已经清过，这里覆盖事务期间别处重新填进去的旧数据。
	if written != nil {
		if written.instrumentsWritten {
			s.instrumentSets.Invalidate()
		}
		if written.accountsWritten {
			s.identities.Invalidate()
		}
	}
	return err
}

func (s *Store) DBForTest() *gorm.DB {
	return s.db
}

func writeError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
