package pebble

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Options 控制主服务的 Pebble 库。库里存放全部数据集，数据集身份是每个物理键的一部分。
type Options struct {
	Path          string
	MaxEventBytes int
	// ChangeLogRetention 是变更日志的保留期（由保留期清理循环按它删除）；为 0 时取 DefaultChangeLogRetention。
	ChangeLogRetention time.Duration
	DisableSyncWrites  bool
	PeriodNow          func() time.Time
	// BlockCacheBytes 是 Pebble 块缓存大小；为 0 时由环境变量或主机内存推算，见 resolveBlockCache。
	BlockCacheBytes int64
	// DatasetExists 在写入拿到数据集读锁后确认数据集仍在元数据里，必须读当前最新的元数据；为空时不检查。
	DatasetExists func(spaceID, datasetID string) bool
}

type Store struct {
	db                 *cpebble.DB
	writeOptions       *cpebble.WriteOptions
	sourceStoreID      string
	maxEventBytes      int
	changeLogRetention time.Duration
	datasetWriteMu     sync.RWMutex
	// changeMu 保护变更日志的序号：行写入、完成标记与日志清理都在它的保护下提交。
	changeMu                sync.Mutex
	periodMu                sync.Mutex
	periodNow               func() time.Time
	periodFinalizeErrorMu   sync.Mutex
	periodFinalizeLastError string
	// markerWake 在完成标记提交后收到一个信号（容量 1，合并），转发协程据此立即发布，不等轮询。
	markerWake chan struct{}
	// changeSignal 在下一次变更日志提交时被关闭并换新，唤醒等待新变更的读取，见 noteChangeCommitted。
	changeSignalMu   sync.Mutex
	changeSignal     chan struct{}
	maintenanceMu    sync.Mutex
	maintenanceWG    sync.WaitGroup
	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
	closing          bool
	blockCacheBytes  int64
	// statsStop 通知缓存用量日志协程退出，由 Close 关闭。
	statsStop chan struct{}
	statsOnce sync.Once
	// datasetExists 见 Options.DatasetExists。
	datasetExists func(spaceID, datasetID string) bool
}

func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("pebble path is required")
	}
	if opts.PeriodNow == nil {
		opts.PeriodNow = time.Now
	}
	if err := ensureLayout(opts.Path); err != nil {
		return nil, err
	}
	cacheBytes, cacheSource := resolveBlockCache(opts.BlockCacheBytes)
	cache := cpebble.NewCache(cacheBytes)
	// Open 会自己持有一份引用；这里放掉创建时的那份，DB 关闭时缓存随之释放。
	defer cache.Unref()
	db, err := cpebble.Open(opts.Path, &cpebble.Options{Cache: cache})
	if err != nil {
		return nil, err
	}
	cacheLogf("storage Pebble 块缓存 %dMB（%s）", cacheBytes>>20, cacheSource)
	sourceStoreID, err := bindSourceStore(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	writeOptions := cpebble.Sync
	if opts.DisableSyncWrites {
		writeOptions = cpebble.NoSync
	}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	store := &Store{db: db, writeOptions: writeOptions, sourceStoreID: sourceStoreID, maxEventBytes: opts.MaxEventBytes, changeLogRetention: opts.ChangeLogRetention, periodNow: opts.PeriodNow, backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel, blockCacheBytes: cacheBytes, statsStop: make(chan struct{}), markerWake: make(chan struct{}, 1), changeSignal: make(chan struct{}), datasetExists: opts.DatasetExists}
	store.startPeriodFinalizer()
	store.startCacheStats(cacheStatsFirstDelay, cacheStatsInterval)
	return store, nil
}

// DiskSpaceUsage returns the on-disk size of the store, including files that
// compaction has made obsolete but not yet deleted.
func (s *Store) DiskSpaceUsage() uint64 {
	if s == nil || s.db == nil {
		return 0
	}
	return s.db.Metrics().DiskSpaceUsage()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.maintenanceMu.Lock()
	s.closing = true
	s.maintenanceMu.Unlock()
	s.statsOnce.Do(func() {
		if s.statsStop != nil {
			close(s.statsStop)
		}
	})
	if s.backgroundCancel != nil {
		s.backgroundCancel()
	}
	s.maintenanceWG.Wait()
	return s.db.Close()
}

func (s *Store) compactAsync(start, end []byte) {
	s.maintenanceMu.Lock()
	if s.closing {
		s.maintenanceMu.Unlock()
		return
	}
	s.maintenanceWG.Add(1)
	s.maintenanceMu.Unlock()
	go func() {
		defer s.maintenanceWG.Done()
		_ = s.db.Compact(start, end, true)
	}()
}

func validateUpsert(row *pb.RowFieldUpsert) error {
	if row == nil || row.GetKey() == nil {
		return invalid("row key is required")
	}
	if len(row.GetFields()) == 0 && len(row.GetAttributes()) == 0 {
		return invalid("at least one field or attribute is required")
	}
	seen := make(map[string]struct{}, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field == nil || field.GetFieldId() == "" || field.GetValue() == nil {
			return invalid("field_id and value are required")
		}
		if _, ok := seen[field.GetFieldId()]; ok {
			return invalidf("duplicate field_id %q", field.GetFieldId())
		}
		seen[field.GetFieldId()] = struct{}{}
	}
	for name, value := range row.GetAttributes() {
		if name == "" || value == nil {
			return invalid("attribute key and value are required")
		}
	}
	return nil
}

// pebbleReader 是 *pebble.DB 与 *pebble.Snapshot 共有的只读接口。
type pebbleReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *cpebble.IterOptions) (*cpebble.Iterator, error)
}

func (s *Store) resolveMaxRecordVersion(reader pebbleReader, key *pb.RowKey) (*pb.RowKey, error) {
	// The tuple codec preserves UTF-8 byte order, so the last key under each
	// namespace prefix contains that namespace's largest record version.
	base, err := recordBasePrefix(key)
	if err != nil {
		return nil, err
	}
	max := ""
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		iter, err := reader.NewIter(&cpebble.IterOptions{LowerBound: append([]byte{namespace}, base...), UpperBound: nextPrefix(append([]byte{namespace}, base...))})
		if err != nil {
			return nil, err
		}
		if valid := iter.Last(); valid {
			parts, ok := parseRecordValueKey(iter.Key())
			if ok && parts.version > max {
				max = parts.version
			}
		}
		iterErr := iter.Error()
		_ = iter.Close()
		if iterErr != nil {
			return nil, iterErr
		}
	}
	if max == "" {
		return key, nil
	}
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecord().GetRecordId(), Version: max}}}, nil
}

func recordBasePrefix(key *pb.RowKey) ([]byte, error) {
	record := key.GetRecord()
	if record == nil || record.GetRecordId() == "" {
		return nil, invalid("record key is required")
	}
	out := []byte{recordKind}
	out = appendRawPart(out, []byte(key.GetSpaceId()))
	out = appendRawPart(out, []byte(key.GetDatasetId()))
	out = appendRawPart(out, []byte(record.GetRecordId()))
	return out, nil
}

type recordKeyParts struct{ version string }

func parseRecordValueKey(key []byte) (recordKeyParts, bool) {
	if len(key) < 2 || (key[0] != fieldNamespace && key[0] != attributeNamespace) || key[1] != recordKind {
		return recordKeyParts{}, false
	}
	rest := key[2:]
	for i := 0; i < 3; i++ {
		_, next, err := decodePart(rest)
		if err != nil {
			return recordKeyParts{}, false
		}
		rest = next
	}
	version, _, err := decodePart(rest)
	if err == nil {
		return recordKeyParts{version: version}, true
	}
	return recordKeyParts{}, false
}
