package logsink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type collected struct {
	mu       sync.Mutex
	identity Identity
	batches  [][]Record
	err      error
}

func (c *collected) deliver(_ context.Context, identity Identity, records []Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identity = identity
	c.batches = append(c.batches, append([]Record(nil), records...))
	return c.err
}

func (c *collected) records() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Record
	for _, batch := range c.batches {
		out = append(out, batch...)
	}
	return out
}

func testCore(t *testing.T) (*core, *sink) {
	t.Helper()
	s := newSink(time.Now)
	s.start()
	return &core{sink: s, level: zap.NewAtomicLevelAt(zap.InfoLevel)}, s
}

func entry(level zapcore.Level, caller, message string, at time.Time) zapcore.Entry {
	e := zapcore.Entry{Level: level, Time: at, Message: message}
	if caller != "" {
		file, line, _ := strings.Cut(caller, ":")
		var n int
		fmt.Sscan(line, &n)
		e.Caller = zapcore.EntryCaller{Defined: true, File: "/src/" + file, Line: n}
	}
	return e
}

var identity = Identity{ServiceName: "storage-node", InstanceID: "storage-node@storage", NodeID: "storage", BootID: "boot-1"}

// Error 和带事件名的日志进重要日志；Info、Warn 不进。累积的字段和单次调用的字段都带上，事件名单独取出。
func TestImportantLaneTakesErrorsAndEvents(t *testing.T) {
	c, s := testCore(t)
	target := &collected{}
	if !s.important.attach(identity, target.deliver) {
		t.Fatal("attach failed")
	}
	now := time.Unix(1700000000, 0)
	scoped := c.With([]zapcore.Field{zap.String("service_name", "storage-node")})
	_ = scoped.Write(entry(zapcore.InfoLevel, "view/a.go:10", "普通信息", now), nil)
	_ = scoped.Write(entry(zapcore.WarnLevel, "view/a.go:11", "警告", now), nil)
	_ = scoped.Write(entry(zapcore.ErrorLevel, "view/a.go:12", "写入失败", now), []zapcore.Field{zap.Int("rows", 42)})
	_ = scoped.Write(entry(zapcore.InfoLevel, "boot/b.go:5", "进程启动", now), []zapcore.Field{zap.String(EventField, "process_started")})
	c.Sync()

	got := target.records()
	if len(got) != 2 {
		t.Fatalf("重要日志 = %+v，应为 2 条", got)
	}
	if got[0].Message != "写入失败" || got[0].Level != "error" || got[0].Caller != "view/a.go:12" || got[0].Fields["rows"] != "42" || got[0].Fields["service_name"] != "storage-node" || got[0].Sequence != 1 {
		t.Fatalf("错误日志 = %+v", got[0])
	}
	if got[1].Event != "process_started" || got[1].Level != "info" || got[1].Sequence != 2 {
		t.Fatalf("关键事件 = %+v", got[1])
	}
	if _, kept := got[1].Fields[EventField]; kept {
		t.Fatal("事件名不应留在字段里")
	}
	if target.identity != identity {
		t.Fatalf("identity = %+v", target.identity)
	}
}

// 同一代码位置一分钟内只放行 10 条，其余合并；下一分钟的第一条带上被合并的条数。不同位置互不影响。
func TestSameCallerIsLimitedPerMinute(t *testing.T) {
	c, s := testCore(t)
	target := &collected{}
	s.important.attach(identity, target.deliver)
	start := time.Unix(1700000000, 0)
	for i := 0; i < 35; i++ {
		_ = c.Write(entry(zapcore.ErrorLevel, "loop/a.go:7", fmt.Sprintf("第 %d 次", i), start.Add(time.Duration(i)*time.Second)), nil)
	}
	_ = c.Write(entry(zapcore.ErrorLevel, "other/b.go:9", "别处的错误", start.Add(40*time.Second)), nil)
	_ = c.Write(entry(zapcore.ErrorLevel, "loop/a.go:7", "一分钟之后", start.Add(61*time.Second)), nil)
	c.Sync()

	var loop []Record
	for _, record := range target.records() {
		if record.Caller == "loop/a.go:7" {
			loop = append(loop, record)
		}
	}
	if len(loop) != 11 || len(target.records()) != 12 {
		t.Fatalf("同一位置放行 %d 条、共 %d 条，应为 11 和 12", len(loop), len(target.records()))
	}
	if loop[9].Message != "第 9 次" || loop[9].MergedCount != 0 {
		t.Fatalf("第 10 条 = %+v", loop[9])
	}
	if loop[10].Message != "一分钟之后" || loop[10].MergedCount != 25 {
		t.Fatalf("下一分钟的第一条 = %+v，应带上被合并的 25 条", loop[10])
	}
}

// 接收方接上之前的日志先留在队列里（最多 256 条），接上之后补发；只认第一次接入。
func TestRecordsWaitForAttach(t *testing.T) {
	c, s := testCore(t)
	now := time.Unix(1700000000, 0)
	for i := 0; i < importantQueueSize+20; i++ {
		_ = c.Write(entry(zapcore.ErrorLevel, fmt.Sprintf("early/a.go:%d", i+1), "启动早期的错误", now), nil)
	}
	c.Sync() // 没有接收方时不能卡住
	first, second := &collected{}, &collected{}
	if !s.important.attach(identity, first.deliver) || s.important.attach(identity, second.deliver) {
		t.Fatal("只应认第一次接入")
	}
	c.Sync()
	if got := len(first.records()); got != importantQueueSize {
		t.Fatalf("补发了 %d 条，应为队列上限 %d", got, importantQueueSize)
	}
	if len(second.records()) != 0 {
		t.Fatal("第二个接收方不应收到日志")
	}
}

// CLS 一路收不低于最低级别的全部日志，不做同位置合并；没接发送方时不入队。
func TestShipLaneTakesEverythingAtOrAboveMinLevel(t *testing.T) {
	c, s := testCore(t)
	now := time.Unix(1700000000, 0)
	_ = c.Write(entry(zapcore.InfoLevel, "a.go:1", "接上之前", now), nil)
	shipped := &collected{}
	s.ship.attach(identity, shipped.deliver)
	s.shipMinLevel.Store(int32(zapcore.WarnLevel))
	_ = c.Write(entry(zapcore.InfoLevel, "a.go:1", "低于最低级别", now), nil)
	for i := 0; i < 30; i++ {
		_ = c.Write(entry(zapcore.ErrorLevel, "a.go:2", "同一位置的错误", now), nil)
	}
	_ = c.Write(entry(zapcore.WarnLevel, "a.go:3", "警告", now), nil)
	// 关键事件是 Info 级，低于最低级别也要上报。
	_ = c.Write(entry(zapcore.InfoLevel, "a.go:4", "进程启动", now), []zapcore.Field{zap.String(EventField, "process_started")})
	c.Sync()
	got := shipped.records()
	if len(got) != 32 {
		t.Fatalf("CLS 收到 %d 条，应为 32", len(got))
	}
	if last := got[len(got)-1]; last.Event != "process_started" || last.Level != "info" {
		t.Fatalf("最后一条应是关键事件：%+v", last)
	}
}

// 接收方报错时这一批丢弃，不重试，后面的日志照常发送。
func TestDeliverFailureDoesNotBlockLaterRecords(t *testing.T) {
	c, s := testCore(t)
	target := &collected{err: errors.New("eventbus down")}
	s.important.attach(identity, target.deliver)
	now := time.Unix(1700000000, 0)
	_ = c.Write(entry(zapcore.ErrorLevel, "a.go:1", "第一条", now), nil)
	c.Sync()
	target.mu.Lock()
	target.err = nil
	target.mu.Unlock()
	_ = c.Write(entry(zapcore.ErrorLevel, "a.go:2", "第二条", now), nil)
	c.Sync()
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(target.batches) != 2 || target.batches[1][0].Message != "第二条" {
		t.Fatalf("batches = %+v", target.batches)
	}
}

// 超长的正文和字段按字节截断，不切断汉字，结果是合法的 UTF-8。
func TestBuildRecordTruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("错", 3000) // 9000 字节
	record := buildRecord(zapcore.Entry{Level: zapcore.ErrorLevel, Message: long + "\xff"}, []zapcore.Field{zap.String("detail", long)})
	if len(record.Message) > maxMessageBytes || len(record.Message)%3 != 0 || !strings.HasPrefix(long, record.Message) {
		t.Fatalf("message bytes = %d", len(record.Message))
	}
	if got := record.Fields["detail"]; len(got) > maxFieldValueBytes || len(got)%3 != 0 {
		t.Fatalf("field bytes = %d", len(got))
	}
	var fields []zapcore.Field
	for i := 0; i < maxFields+10; i++ {
		fields = append(fields, zap.Int(fmt.Sprintf("k%02d", i), i))
	}
	if got := len(buildRecord(zapcore.Entry{}, fields).Fields); got != maxFields {
		t.Fatalf("fields = %d, want %d", got, maxFields)
	}
}

// Fatal 写完之后进程就退出了：这一条和它前面攒着的记录要在写日志的调用里发完，不能等后台的下一拍。
func TestFatalFlushesBeforeReturning(t *testing.T) {
	c, s := testCore(t)
	target := &collected{}
	s.important.attach(identity, target.deliver)
	now := time.Unix(1700000000, 0)
	_ = c.Write(entry(zapcore.ErrorLevel, "a.go:1", "退出前的错误", now), nil)
	_ = c.Write(entry(zapcore.FatalLevel, "a.go:2", "初始化失败", now), nil)
	got := target.records()
	if len(got) != 2 || got[1].Level != "fatal" || got[1].Message != "初始化失败" {
		t.Fatalf("Fatal 返回时已发出的记录 = %+v", got)
	}
}

// 关键事件不受同一代码位置的限流：生命周期审计都从同一行写出，一分钟内再多也要留下。
func TestEventsBypassTheCallerLimit(t *testing.T) {
	c, s := testCore(t)
	target := &collected{}
	s.important.attach(identity, target.deliver)
	now := time.Unix(1700000000, 0)
	for i := 0; i < 25; i++ {
		_ = c.Write(entry(zapcore.InfoLevel, "lifecycle/lifecycle.go:535", fmt.Sprintf("lifecycle_pause #%d", i), now), []zapcore.Field{zap.String(EventField, "lifecycle_pause")})
	}
	c.Sync()
	if got := len(target.records()); got != 25 {
		t.Fatalf("关键事件进了 %d 条，应为 25", got)
	}
}

// 日志里可能有非 UTF-8 字节（Storage 的日志就有）：正文、字段的键和值都要变成合法的 UTF-8，
// 否则整批事件在编码时失败，一起丢掉。
func TestBuildRecordReplacesInvalidUTF8(t *testing.T) {
	record := buildRecord(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "坏字节\xff\xfe结尾"},
		[]zapcore.Field{zap.String("k\xff", "v\xfe"), zap.String(EventField, "e\xff")})
	values := []string{record.Message, record.Event}
	for key, value := range record.Fields {
		values = append(values, key, value)
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			t.Fatalf("不是合法的 UTF-8：%q", value)
		}
	}
	if record.Message != "坏字节?结尾" || record.Fields["k?"] != "v?" {
		t.Fatalf("record = %+v", record)
	}
}
