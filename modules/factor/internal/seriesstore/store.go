package seriesstore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

const layoutVersion = "1"

// Options 控制本地存储。
type Options struct {
	Dir string
	// BlockCacheBytes 是 Pebble 块缓存大小；0 取 512MiB。
	BlockCacheBytes int64
	// DisableSync 关闭提交时的同步落盘，仅测试使用。
	DisableSync bool
}

// Store 是本地时序存储。可并发使用；写入按存储整体串行提交。
type Store struct {
	db      *pebble.DB
	cache   *pebble.Cache
	sync    *pebble.WriteOptions
	wmu     sync.Mutex
	schemas map[Source][]Field
	smu     sync.Mutex
	closed  sync.Once
	closeEr error
}

// quietLogger 丢弃 Pebble 的信息日志（启动时回放 WAL 等），只让致命错误中止进程。
type quietLogger struct{}

func (quietLogger) Infof(string, ...any) {}
func (quietLogger) Errorf(format string, args ...any) {
	log.Printf("seriesstore: "+format, args...)
}
func (quietLogger) Fatalf(format string, args ...any) {
	log.Fatalf("seriesstore: "+format, args...)
}

// Open 打开（必要时创建）本地存储。布局版本不一致时返回 ErrLayoutMismatch，不会改动目录。
func Open(opts Options) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("本地时序存储目录不能为空")
	}
	cacheBytes := opts.BlockCacheBytes
	if cacheBytes <= 0 {
		cacheBytes = 512 << 20
	}
	cache := pebble.NewCache(cacheBytes)
	popts := &pebble.Options{Cache: cache, MaxOpenFiles: 2048, Logger: quietLogger{}}
	popts.Levels = make([]pebble.LevelOptions, 7)
	for i := range popts.Levels {
		popts.Levels[i].FilterPolicy = bloom.FilterPolicy(10)
		popts.Levels[i].Compression = pebble.SnappyCompression
	}
	db, err := pebble.Open(opts.Dir, popts)
	if err != nil {
		cache.Unref()
		return nil, fmt.Errorf("打开本地时序存储 %s：%w", opts.Dir, err)
	}
	s := &Store{db: db, cache: cache, sync: pebble.Sync, schemas: map[Source][]Field{}}
	if opts.DisableSync {
		s.sync = pebble.NoSync
	}
	val, closer, err := db.Get([]byte("m/layout"))
	switch {
	case err == nil:
		got := string(val)
		_ = closer.Close()
		if got != layoutVersion {
			_ = s.Close()
			return nil, fmt.Errorf("%w：目录是 %s，程序是 %s", ErrLayoutMismatch, got, layoutVersion)
		}
	case errors.Is(err, pebble.ErrNotFound):
		if err := db.Set([]byte("m/layout"), []byte(layoutVersion), pebble.Sync); err != nil {
			_ = s.Close()
			return nil, err
		}
	default:
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭存储。
func (s *Store) Close() error {
	s.closed.Do(func() {
		s.closeEr = s.db.Close()
		if s.cache != nil {
			s.cache.Unref()
		}
	})
	return s.closeEr
}

// ---------------------------------------------------------------- 键

func checkSegment(name, value string) error {
	if value == "" || strings.ContainsAny(value, "/\x00") {
		return fmt.Errorf("%w：%s %q 为空或含有非法字符", ErrInvalidWrite, name, value)
	}
	return nil
}

func (src Source) check() error {
	for _, p := range [][2]string{{"space", src.SpaceID}, {"dataset", src.DatasetID}, {"freq", src.Freq}} {
		if err := checkSegment(p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

func blockPrefix(src Source, subject string) []byte {
	return []byte("k/" + src.SpaceID + "/" + src.DatasetID + "/" + src.Freq + "/" + subject + "/")
}

func blockKey(src Source, subject string, start int64) []byte {
	key := blockPrefix(src, subject)
	return binary.BigEndian.AppendUint64(key, uint64(start)^(1<<63))
}

func boundsPrefix(src Source) string { return "m/b/" + src.String() + "/" }

func boundsKey(src Source, subject, tag string) []byte {
	return []byte(boundsPrefix(src) + subject + "/" + tag)
}

func schemaKey(src Source) []byte  { return []byte("m/schema/" + src.String()) }
func syncKey(src Source) []byte    { return []byte("m/sync/" + src.String()) }
func pendingKey(src Source) []byte { return []byte("m/pending/" + src.String()) }

func prefixEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end[:i+1]
		}
	}
	return nil
}

type kvReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *pebble.IterOptions) (*pebble.Iterator, error)
}

func getBytes(r kvReader, key []byte) ([]byte, bool, error) {
	val, closer, err := r.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	out := append([]byte(nil), val...)
	_ = closer.Close()
	return out, true, nil
}

// ---------------------------------------------------------------- 字段表

type schemaRecord struct {
	Version int            `json:"version"`
	Fields  []schemaFieldJ `json:"fields"`
}

type schemaFieldJ struct {
	Name string `json:"name"`
	Type uint8  `json:"type"`
	Fill uint8  `json:"fill"`
}

func (s *Store) loadSchema(r kvReader, src Source) ([]Field, bool, error) {
	s.smu.Lock()
	cached, ok := s.schemas[src]
	s.smu.Unlock()
	if ok {
		return cached, true, nil
	}
	raw, found, err := getBytes(r, schemaKey(src))
	if err != nil || !found {
		return nil, false, err
	}
	var rec schemaRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false, fmt.Errorf("读取 %s 的字段表：%w", src, err)
	}
	fields := make([]Field, len(rec.Fields))
	for i, f := range rec.Fields {
		fields[i] = Field{Name: f.Name, Type: FieldType(f.Type), Fill: FillRule(f.Fill)}
	}
	s.smu.Lock()
	s.schemas[src] = fields
	s.smu.Unlock()
	return fields, true, nil
}

// Schema 返回数据源已记录的字段表。
func (s *Store) Schema(src Source) ([]Field, bool, error) {
	return s.loadSchema(s.db, src)
}

// mergeSchema 把写入带来的字段与已记录的字段表合并：已有字段不能少、类型不能变，可以新增字段；填充规则取最新。
// 返回新的字段表，以及是否需要重新记录。
func mergeSchema(old []Field, in []Field) ([]Field, bool, error) {
	seen := map[string]int{}
	for i, f := range in {
		if f.Name == "" || !f.Type.valid() {
			return nil, false, fmt.Errorf("%w：字段 %q 的名称或类型不合法", ErrInvalidWrite, f.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return nil, false, fmt.Errorf("%w：字段 %q 重复", ErrInvalidWrite, f.Name)
		}
		seen[f.Name] = i
	}
	for _, f := range old {
		i, ok := seen[f.Name]
		if !ok {
			return nil, false, fmt.Errorf("%w：写入缺少数据集字段 %q（导入必须带全部字段）", ErrSchema, f.Name)
		}
		if in[i].Type != f.Type {
			return nil, false, fmt.Errorf("%w：字段 %q 的类型从 %d 变成 %d", ErrSchema, f.Name, f.Type, in[i].Type)
		}
	}
	changed := len(old) != len(in)
	if !changed {
		for i := range old {
			if old[i] != in[i] {
				changed = true
				break
			}
		}
	}
	return append([]Field(nil), in...), changed, nil
}

func marshalSchema(fields []Field, version int) []byte {
	rec := schemaRecord{Version: version}
	for _, f := range fields {
		rec.Fields = append(rec.Fields, schemaFieldJ{Name: f.Name, Type: uint8(f.Type), Fill: uint8(f.Fill)})
	}
	raw, _ := json.Marshal(rec)
	return raw
}

// conform 把一个块（或一批行）按字段表 fields 重排：块里没有的字段读出为 NULL，多出的字段丢弃。
func conform(blk *Columns, fields []Field) *Columns {
	n := blk.Len()
	out := &Columns{Fields: append([]Field(nil), fields...), Times: blk.Times, Tags: blk.Tags, Filled: blk.Filled, Cols: make([]Column, len(fields))}
	for i, f := range fields {
		j := blk.FieldIndex(f.Name)
		if j >= 0 && blk.Fields[j].Type == f.Type {
			out.Cols[i] = blk.Cols[j]
			continue
		}
		col := Column{Null: make([]bool, n)}
		for r := range col.Null {
			col.Null[r] = true
		}
		switch f.Type {
		case TypeFloat64:
			col.F = make([]float64, n)
		case TypeInt64, TypeBool:
			col.I = make([]int64, n)
		case TypeString:
			col.S = make([]string, n)
		}
		out.Cols[i] = col
	}
	return out
}

// ---------------------------------------------------------------- 边界

type tagBounds struct {
	tag    string
	e, l   int64
	exists bool
}

func encodeBounds(e, l int64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b, uint64(e))
	binary.BigEndian.PutUint64(b[8:], uint64(l))
	return b
}

func decodeBounds(raw []byte) (int64, int64, bool) {
	if len(raw) != 16 {
		return 0, 0, false
	}
	return int64(binary.BigEndian.Uint64(raw)), int64(binary.BigEndian.Uint64(raw[8:])), true
}

// subjectTags 列出标的已有数据的各 series_tag 及其边界。
func subjectTags(r kvReader, src Source, subject string) ([]tagBounds, error) {
	prefix := []byte(boundsPrefix(src) + subject + "/")
	iter, err := r.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []tagBounds
	for ok := iter.First(); ok; ok = iter.Next() {
		e, l, valid := decodeBounds(iter.Value())
		if !valid {
			return nil, fmt.Errorf("%s 的边界记录已损坏", src)
		}
		out = append(out, tagBounds{tag: string(iter.Key()[len(prefix):]), e: e, l: l, exists: true})
	}
	return out, iter.Error()
}

// Bounds 返回标的本地数据的最早与最新时间（各 series_tag 取并集）；没有数据时 ok 为 false。
func (s *Store) Bounds(src Source, subject string) (Bounds, bool, error) {
	if err := src.check(); err != nil {
		return Bounds{}, false, err
	}
	tags, err := subjectTags(s.db, src, subject)
	if err != nil || len(tags) == 0 {
		return Bounds{}, false, err
	}
	e, l := tags[0].e, tags[0].l
	for _, t := range tags[1:] {
		e, l = min(e, t.e), max(l, t.l)
	}
	return Bounds{Earliest: time.Unix(e, 0).UTC(), Latest: time.Unix(l, 0).UTC()}, true, nil
}

// Latest 返回标的本地最新一根的时间。
func (s *Store) Latest(src Source, subject string) (time.Time, bool, error) {
	b, ok, err := s.Bounds(src, subject)
	return b.Latest, ok, err
}

// Earliest 返回标的本地最早一根的时间。
func (s *Store) Earliest(src Source, subject string) (time.Time, bool, error) {
	b, ok, err := s.Bounds(src, subject)
	return b.Earliest, ok, err
}

// Subjects 列出数据源里有本地数据的标的。
func (s *Store) Subjects(src Source) ([]string, error) {
	if err := src.check(); err != nil {
		return nil, err
	}
	prefix := []byte(boundsPrefix(src))
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	seen := map[string]struct{}{}
	for ok := iter.First(); ok; ok = iter.Next() {
		rest := string(iter.Key()[len(prefix):])
		if i := strings.IndexByte(rest, '/'); i > 0 {
			seen[rest[:i]] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for subject := range seen {
		out = append(out, subject)
	}
	sort.Strings(out)
	return out, iter.Error()
}

// ---------------------------------------------------------------- 同步位点与失效待办

// SyncCursor 返回数据源的同步位点（没有记录时 ok 为 false）。
func (s *Store) SyncCursor(src Source) ([]byte, bool, error) {
	return getBytes(s.db, syncKey(src))
}

// SetSyncCursor 单独设置同步位点（对账完成发布新基线时使用）。
func (s *Store) SetSyncCursor(src Source, cursor []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.db.Set(syncKey(src), cursor, s.sync)
}

// Pending 返回数据源待提交的失效重算区间。
func (s *Store) Pending(src Source) ([]PendingSpan, error) {
	return loadPending(s.db, src)
}

func loadPending(r kvReader, src Source) ([]PendingSpan, error) {
	raw, ok, err := getBytes(r, pendingKey(src))
	if err != nil || !ok {
		return nil, err
	}
	var out []PendingSpan
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("读取 %s 的失效重算待办：%w", src, err)
	}
	return out, nil
}

// ClearPending 清除已经成功提交的待办（按内容相等匹配，提交期间新增的待办保留）。
func (s *Store) ClearPending(src Source, done []PendingSpan) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	cur, err := loadPending(s.db, src)
	if err != nil {
		return err
	}
	var keep []PendingSpan
	for _, p := range cur {
		matched := false
		for _, d := range done {
			if p.Subject == d.Subject && p.From.Equal(d.From) && p.To.Equal(d.To) && p.Reason == d.Reason {
				matched = true
				break
			}
		}
		if !matched {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return s.db.Delete(pendingKey(src), s.sync)
	}
	raw, _ := json.Marshal(keep)
	return s.db.Set(pendingKey(src), raw, s.sync)
}

// ---------------------------------------------------------------- 读

// ReadRange 返回 [start, end) 内各标的的列式数据，按 (时间, 标签) 排序；没有数据的标的不在结果里。
// 一次调用在同一个 Pebble 快照上读，各标的版本一致。
func (s *Store) ReadRange(ctx context.Context, src Source, subjects []string, start, end time.Time) (map[string]*Columns, error) {
	if err := src.check(); err != nil {
		return nil, err
	}
	g, err := gridFor(src.Freq)
	if err != nil {
		return nil, err
	}
	snap := s.db.NewSnapshot()
	defer snap.Close()
	fields, ok, err := s.loadSchema(snap, src)
	if err != nil || !ok {
		return map[string]*Columns{}, err
	}
	out := make(map[string]*Columns, len(subjects))
	lo, hi := start.UTC().Unix(), end.UTC().Unix()
	for _, subject := range subjects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := checkSegment("subject", subject); err != nil {
			return nil, err
		}
		cols, err := readSubject(snap, src, g, fields, subject, lo, hi)
		if err != nil {
			return nil, err
		}
		if cols.Len() > 0 {
			out[subject] = cols
		}
	}
	return out, nil
}

func readSubject(r kvReader, src Source, g grid, fields []Field, subject string, lo, hi int64) (*Columns, error) {
	out := NewColumns(fields, 0)
	if lo >= hi {
		return out, nil
	}
	iter, err := r.NewIter(&pebble.IterOptions{
		LowerBound: blockKey(src, subject, g.blockStart(lo)),
		UpperBound: blockKey(src, subject, g.blockStart(hi-1)+1),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	for ok := iter.First(); ok; ok = iter.Next() {
		blk, err := decodeBlock(iter.Value())
		if err != nil {
			return nil, fmt.Errorf("%s/%s：%w", src, subject, err)
		}
		blk = conform(blk, fields)
		for i := 0; i < blk.Len(); i++ {
			if blk.Times[i] >= lo && blk.Times[i] < hi {
				out.copyRow(blk, i)
			}
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	if out.Len() > 1 && !sort.SliceIsSorted(out.Times, func(i, j int) bool { return out.Times[i] < out.Times[j] }) {
		out = out.sortRows()
	}
	return out, nil
}

// readBlockAt 读取 start 起点的块，不存在返回 nil。
func readBlockAt(r kvReader, src Source, subject string, start int64, fields []Field) (*Columns, error) {
	raw, ok, err := getBytes(r, blockKey(src, subject, start))
	if err != nil || !ok {
		return nil, err
	}
	blk, err := decodeBlock(raw)
	if err != nil {
		return nil, fmt.Errorf("%s/%s：%w", src, subject, err)
	}
	return conform(blk, fields), nil
}
