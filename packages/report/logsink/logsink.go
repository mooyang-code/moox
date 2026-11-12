// Package logsink 是 tRPC 日志的一个输出（名字是 moox），和文件输出并存：
//
//   - Error 及以上级别的日志，以及用 Important 写出的关键事件，交给“重要日志”的接收方（各服务是经 EventBus 发给 Monitor，
//     Monitor 自己是直接写库）。
//   - 不低于设定级别（默认 warn）的日志和全部关键事件交给 CLS 的发送方（没有配置 CLS 时这一路关闭）。
//
// 两路各有一个有上限的队列，写日志的调用只做一次不阻塞的入队，网络都在后台协程里；队列满了就丢并计数。
// 本包内部出错时不能再调 tRPC 日志（失败日志会重新进队列），只计数。
//
// 用法：服务的 main 空白引入 clsreporter/logcls（它调用 InstallDefaultFactory）。之后进程按 trpc_go.yaml 装配默认日志时，
// 本包会在配置的输出之外自动加上 moox 这一项，不需要改配置文件：线上的配置文件不随二进制更新，
// 要是靠配置开启，换二进制不改配置就不生效，改了配置再回滚二进制又会因为不认识这个输出而起不来。
// 没有调用 InstallDefaultFactory 的进程，本包什么都不做。
package logsink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/plugin"
)

const (
	// WriterName 是 trpc_go.yaml 里这个输出的名字。
	WriterName = "moox"
	// EventField 是关键事件名所在的日志字段；带这个字段的日志不论级别都算重要日志。
	EventField = "moox_event"

	// 一条记录的上限，与 packages/events 对日志事件的校验一致（那边按字节数拒收，这里按同样的上限截断）。
	maxMessageBytes    = 4096
	maxFields          = 32
	maxFieldKeyBytes   = 128
	maxFieldValueBytes = 1024

	importantQueueSize = 256
	importantBatchSize = 50
	importantInterval  = time.Second
	importantTimeout   = 5 * time.Second

	shipQueueSize = 4096
	shipBatchSize = 500
	shipInterval  = 2 * time.Second
	shipTimeout   = 5 * time.Second

	// 同一个代码位置每分钟最多有这么多条进“重要日志”，多出来的只计数，下一分钟的第一条带上被合并的条数。
	// 循环报错时库里不会被刷满，完整的记录在 CLS 和本机文件里。
	callerLimitPerMinute = 10
	callerLimitWindow    = time.Minute
	maxTrackedCallers    = 1024

	// flushTimeout 是进程退出时等队列发完的最长时间。
	flushTimeout = 2 * time.Second
	// failureReportInterval 是把发送失败写到标准错误的最小间隔。
	failureReportInterval = time.Minute
)

// Record 是一条日志。
type Record struct {
	// Sequence 只有重要日志才有：一次进程启动内递增，接收方用“启动号 + 序号”去重。
	Sequence uint64
	Time     time.Time
	Level    string
	// Event 是关键事件的名称；普通日志为空。
	Event   string
	Message string
	// Caller 是打日志的代码位置（包/文件:行号）。
	Caller string
	Fields map[string]string
	// MergedCount 大于 0 表示在这条之前，同一代码位置另有这么多条因为太密没有进重要日志。
	MergedCount uint32
}

// Identity 说明日志来自哪个进程。
type Identity struct {
	ServiceName string
	InstanceID  string
	NodeID      string
	BootID      string
}

// Deliver 把一批日志交给接收方。返回错误时这一批被丢弃并计数，不重试。
type Deliver func(ctx context.Context, identity Identity, records []Record) error

var (
	recordsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_logsink_records_total",
		Help: "Log records handed to the important-log target (path=important) or the CLS shipper (path=cls).",
	}, []string{"path"})
	droppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_logsink_dropped_total",
		Help: "Log records dropped because the queue was full (reason=queue_full) or delivery failed (reason=deliver_failed).",
	}, []string{"path", "reason"})
	mergedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "moox_logsink_merged_total",
		Help: "Important log records merged away because one code location logged more than the per-minute limit.",
	})
)

func init() {
	prometheus.MustRegister(recordsTotal, droppedTotal, mergedTotal)
	log.RegisterWriter(WriterName, writerFactory{})
}

// InstallDefaultFactory 接管默认日志（plugins.log.default）的装配：照常按配置创建各个输出，另外加上 moox 这一项。
// tRPC 允许替换默认的日志装配器（log.DefaultLogFactory 的注释写明可以换成自己的实现）。要在 trpc.NewServer 之前调用，
// 通常放在 init 里。配置里已经写了 writer: moox 时不重复添加。
func InstallDefaultFactory() {
	plugin.Register(defaultLoggerName, defaultFactory{})
}

const (
	logPluginType     = "log"
	defaultLoggerName = "default"
)

type defaultFactory struct{}

func (defaultFactory) Type() string { return logPluginType }

// Setup 与 tRPC 自带的日志装配器做同样的事（按配置建输出、注册日志对象），只多加一项输出。
func (defaultFactory) Setup(name string, decoder plugin.Decoder) error {
	if decoder == nil {
		return errors.New("log config decoder empty")
	}
	cfg := log.Config{}
	if err := decoder.Decode(&cfg); err != nil {
		return err
	}
	if len(cfg) == 0 {
		return errors.New("log config output empty")
	}
	// 没有配置 caller_skip 时用 2，与 tRPC 自带的装配器一致。
	callerSkip := 2
	listed := false
	for _, output := range cfg {
		if output.CallerSkip != 0 {
			callerSkip = output.CallerSkip
		}
		listed = listed || output.Writer == WriterName
	}
	if !listed {
		cfg = append(cfg, log.OutputConfig{Writer: WriterName, Level: "info"})
	}
	logger := log.NewZapLogWithCallerSkip(cfg, callerSkip)
	if logger == nil {
		return errors.New("new zap logger fail")
	}
	log.Register(name, logger)
	return nil
}

// current 是本进程唯一的输出；默认日志装配时（配置里列了 writer: moox，或调用过 InstallDefaultFactory）才创建。
var current atomic.Pointer[sink]

type writerFactory struct{}

func (writerFactory) Type() string { return "log" }

func (writerFactory) Setup(_ string, decoder plugin.Decoder) error {
	output, ok := decoder.(*log.Decoder)
	if !ok {
		return fmt.Errorf("logsink: unexpected log decoder %T", decoder)
	}
	level := zap.NewAtomicLevelAt(zap.InfoLevel)
	if output.OutputConfig != nil && strings.TrimSpace(output.OutputConfig.Level) != "" {
		name := strings.ToLower(strings.TrimSpace(output.OutputConfig.Level))
		if name == "trace" { // tRPC 有 trace 级别，zap 没有
			name = "debug"
		}
		if err := level.UnmarshalText([]byte(name)); err != nil {
			return fmt.Errorf("logsink: invalid level %q", output.OutputConfig.Level)
		}
	}
	s := current.Load()
	if s == nil {
		s = newSink(time.Now)
		if !current.CompareAndSwap(nil, s) {
			s = current.Load()
		} else {
			s.start()
			runSetupHooks()
		}
	}
	output.Core, output.ZapLevel = &core{sink: s, level: level}, level
	return nil
}

var (
	setupHooksMu sync.Mutex
	setupHooks   []func()
)

// OnSetup 登记一个在本输出装配好之后执行的函数（只执行一次）。日志插件在读配置时才装配，早于它的 init 里没法直接调用 SetShipper，
// CLS 的接入（clsreporter/logcls）用它推迟到装配之后。
func OnSetup(hook func()) {
	if hook == nil {
		return
	}
	setupHooksMu.Lock()
	setupHooks = append(setupHooks, hook)
	setupHooksMu.Unlock()
}

func runSetupHooks() {
	setupHooksMu.Lock()
	hooks := setupHooks
	setupHooks = nil
	setupHooksMu.Unlock()
	for _, hook := range hooks {
		hook()
	}
}

// Attach 设置重要日志的接收方。一个进程只认第一次：Monitor 先接上“直接写自己的库”，之后 report.NewHandler 的接入就不再生效。
// 进程没有装配这个输出时什么都不做。返回这次是否生效。
func Attach(identity Identity, deliver Deliver) bool {
	s := current.Load()
	if s == nil || deliver == nil {
		return false
	}
	return s.important.attach(identity, deliver)
}

// SetShipper 设置 CLS 一路的发送方：不低于 minLevel 的日志，以及不论级别的关键事件。同样只认第一次。
func SetShipper(identity Identity, minLevel zapcore.Level, deliver Deliver) bool {
	s := current.Load()
	if s == nil || deliver == nil {
		return false
	}
	if !s.ship.attach(identity, deliver) {
		return false
	}
	s.shipMinLevel.Store(int32(minLevel))
	return true
}

// Enabled 报告本进程是否装配了这个输出。
func Enabled() bool { return current.Load() != nil }

// Important 写一条关键事件：按 Info 级别写出并带上事件名，和 Error 一样进重要日志。
// event 用小写加下划线的短名字，例如 process_started、lifecycle_paused。
func Important(_ context.Context, event, format string, args ...any) {
	// 直接调用取出的日志对象，调用层数与 log.Infof 相同，记下的代码位置是 Important 的调用处。
	log.GetDefaultLogger().With(log.Field{Key: EventField, Value: event}).Infof(format, args...)
}

// Audit 把一条生命周期审计记录（暂停、恢复、重置等，形如 "lifecycle_pause module=monitor ..."）写成关键事件，
// 事件名取第一段。给 lifecycle.Options.Audit 用。
func Audit(record string) {
	name, _, _ := strings.Cut(strings.TrimSpace(record), " ")
	if name == "" {
		name = "lifecycle"
	}
	log.GetDefaultLogger().With(log.Field{Key: EventField, Value: name}).Info(record)
}

// lane 是一路队列：有上限的缓冲、一个后台协程、一个接收方。
type lane struct {
	path     string
	queue    chan Record
	batch    int
	interval time.Duration
	timeout  time.Duration

	mu       sync.Mutex
	identity Identity
	deliver  Deliver
	attached atomic.Bool
	// lastFailureReport 是上一次把发送失败写到标准错误的时刻（纳秒）。
	lastFailureReport atomic.Int64

	flush chan chan struct{}
}

func newLane(path string, size, batch int, interval, timeout time.Duration) *lane {
	return &lane{path: path, queue: make(chan Record, size), batch: batch, interval: interval, timeout: timeout, flush: make(chan chan struct{})}
}

func (l *lane) attach(identity Identity, deliver Deliver) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.deliver != nil {
		return false
	}
	l.identity, l.deliver = identity, deliver
	l.attached.Store(true)
	return true
}

// reportFailure 把发送失败写到标准错误，每分钟最多一行。这里不能用 tRPC 日志（会重新进队列）；
// 标准错误由主机守护收进 run/logs/<组件>.log，配置错了（密钥、主题、权限）时至少在那里看得到。
func (l *lane) reportFailure(err error, count int) {
	now := time.Now()
	if last := l.lastFailureReport.Load(); last != 0 && now.UnixNano()-last < int64(failureReportInterval) {
		return
	}
	l.lastFailureReport.Store(now.UnixNano())
	fmt.Fprintf(os.Stderr, "%s logsink: %s 一路发送失败，丢弃 %d 条：%v\n", now.Format(time.RFC3339), l.path, count, err)
}

// offer 不阻塞地入队；队列满了就丢并计数。
func (l *lane) offer(record Record) {
	select {
	case l.queue <- record:
	default:
		droppedTotal.WithLabelValues(l.path, "queue_full").Inc()
	}
}

// run 攒批发送。还没有接收方时不取队列里的东西：启动早期的日志先留在队列里，接上之后再发。
func (l *lane) run() {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	pending := make([]Record, 0, l.batch)
	send := func() {
		if len(pending) == 0 {
			return
		}
		l.mu.Lock()
		identity, deliver := l.identity, l.deliver
		l.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		err := deliver(ctx, identity, pending)
		cancel()
		if err != nil {
			droppedTotal.WithLabelValues(l.path, "deliver_failed").Add(float64(len(pending)))
			l.reportFailure(err, len(pending))
		} else {
			recordsTotal.WithLabelValues(l.path).Add(float64(len(pending)))
		}
		pending = make([]Record, 0, l.batch)
	}
	// drain 把队列里现有的全部取出并发送。
	drain := func() {
		for {
			select {
			case record := <-l.queue:
				pending = append(pending, record)
				if len(pending) >= l.batch {
					send()
				}
			default:
				send()
				return
			}
		}
	}
	for {
		if !l.attached.Load() {
			select {
			case <-ticker.C:
			case done := <-l.flush:
				// 可能刚好在等待期间接上了接收方：接上了就把攒着的发出去。
				if l.attached.Load() {
					drain()
				}
				close(done)
			}
			continue
		}
		select {
		case record := <-l.queue:
			pending = append(pending, record)
			if len(pending) >= l.batch {
				send()
			}
		case <-ticker.C:
			send()
		case done := <-l.flush:
			drain()
			close(done)
		}
	}
}

// flushNow 让后台协程把队列里现有的发完，最多等到 deadline。
func (l *lane) flushNow(deadline time.Time) {
	done := make(chan struct{})
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case l.flush <- done:
	case <-timer.C:
		return
	}
	select {
	case <-done:
	case <-timer.C:
	}
}

type callerWindow struct {
	start  time.Time
	count  int
	merged uint32
}

type sink struct {
	now       func() time.Time
	important *lane
	ship      *lane
	// shipMinLevel 是发往 CLS 的最低级别（zapcore.Level）。
	shipMinLevel atomic.Int32
	sequence     atomic.Uint64

	mu      sync.Mutex
	callers map[string]*callerWindow
}

func newSink(now func() time.Time) *sink {
	return &sink{
		now:       now,
		important: newLane("important", importantQueueSize, importantBatchSize, importantInterval, importantTimeout),
		ship:      newLane("cls", shipQueueSize, shipBatchSize, shipInterval, shipTimeout),
		callers:   map[string]*callerWindow{},
	}
}

func (s *sink) start() {
	go s.important.run()
	go s.ship.run()
}

// write 处理一条日志。在写日志的调用里执行，只做内存操作。
func (s *sink) write(entry zapcore.Entry, fields []zapcore.Field, event bool) {
	important, shipping := s.routes(entry.Level, event)
	if !shipping && !important {
		return
	}
	record := buildRecord(entry, fields)
	if shipping {
		s.ship.offer(record)
	}
	if !important {
		return
	}
	// 关键事件（暂停、恢复、重置等）本来就少，而且同一类事件都从同一行代码写出，不做同位置的限流；只限制普通的错误日志。
	if record.Event == "" {
		merged, ok := s.admit(record.Caller, record.Time)
		if !ok {
			mergedTotal.Inc()
			return
		}
		record.MergedCount = merged
	}
	record.Sequence = s.sequence.Add(1)
	s.important.offer(record)
}

// routes 判断一条日志的去处：是否进重要日志，是否发往 CLS。
// 关键事件不论级别两路都去：CLS 的最低级别默认是 warn，进程启动、暂停恢复这些 Info 级的事件仍然要有。
func (s *sink) routes(level zapcore.Level, event bool) (important, shipping bool) {
	important = level >= zapcore.ErrorLevel || event
	shipping = s.ship.attached.Load() && (event || level >= zapcore.Level(s.shipMinLevel.Load()))
	return important, shipping
}

// wants 报告这条日志有没有去处。
func (s *sink) wants(level zapcore.Level, event bool) bool {
	important, shipping := s.routes(level, event)
	return important || shipping
}

// admit 做同一代码位置的限流：返回这条是否放行，以及放行时要带上的“此前被合并的条数”。
func (s *sink) admit(caller string, at time.Time) (uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	window := s.callers[caller]
	if window == nil {
		if len(s.callers) >= maxTrackedCallers {
			// 位置太多时丢掉已经过期的窗口；都没过期就整体重来，宁可多放行几条，也不让这张表无限增长。
			for key, item := range s.callers {
				if at.Sub(item.start) >= callerLimitWindow && item.merged == 0 {
					delete(s.callers, key)
				}
			}
			if len(s.callers) >= maxTrackedCallers {
				s.callers = map[string]*callerWindow{}
			}
		}
		window = &callerWindow{start: at}
		s.callers[caller] = window
	}
	var merged uint32
	if at.Sub(window.start) >= callerLimitWindow {
		merged = window.merged
		window.start, window.count, window.merged = at, 0, 0
	}
	if window.count >= callerLimitPerMinute {
		window.merged++
		return 0, false
	}
	window.count++
	return merged, true
}

func (s *sink) flush() {
	deadline := time.Now().Add(flushTimeout)
	var wait sync.WaitGroup
	for _, l := range []*lane{s.important, s.ship} {
		wait.Add(1)
		go func(l *lane) {
			defer wait.Done()
			l.flushNow(deadline)
		}(l)
	}
	wait.Wait()
}

// hasEvent 报告这条日志是不是关键事件（带事件名字段）。
func hasEvent(fields []zapcore.Field) bool {
	for i := range fields {
		if fields[i].Key == EventField {
			return true
		}
	}
	return false
}

func buildRecord(entry zapcore.Entry, fields []zapcore.Field) Record {
	record := Record{Time: entry.Time, Level: entry.Level.String(), Message: truncate(entry.Message, maxMessageBytes)}
	if record.Time.IsZero() {
		record.Time = time.Now()
	}
	if entry.Caller.Defined {
		record.Caller = entry.Caller.TrimmedPath()
	}
	if len(fields) == 0 {
		return record
	}
	encoder := zapcore.NewMapObjectEncoder()
	for _, field := range fields {
		field.AddTo(encoder)
	}
	keys := make([]string, 0, len(encoder.Fields))
	for key := range encoder.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	record.Fields = make(map[string]string, len(keys))
	for _, key := range keys {
		value := fmt.Sprint(encoder.Fields[key])
		if key == EventField {
			record.Event = truncate(value, maxFieldKeyBytes)
			continue
		}
		if len(record.Fields) == maxFields {
			continue
		}
		record.Fields[truncate(key, maxFieldKeyBytes)] = truncate(value, maxFieldValueBytes)
	}
	return record
}

// truncate 把字符串截到不超过 limit 个字节，不切断一个 UTF-8 字符，并保证结果是合法的 UTF-8（protobuf 的 string 要求）。
func truncate(value string, limit int) string {
	value = strings.ToValidUTF8(value, "?")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// core 是交给 zap 的输出。With 累积的字段随每条日志一起处理。
type core struct {
	sink   *sink
	level  zap.AtomicLevel
	fields []zapcore.Field
}

func (c *core) Enabled(level zapcore.Level) bool { return c.level.Enabled(level) }

func (c *core) With(fields []zapcore.Field) zapcore.Core {
	merged := make([]zapcore.Field, 0, len(c.fields)+len(fields))
	merged = append(append(merged, c.fields...), fields...)
	return &core{sink: c.sink, level: c.level, fields: merged}
}

func (c *core) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *core) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// 先判断这条日志有没有去处，没有就直接返回：绝大多数是 Info，不值得为它们拼字段、做编码。
	event := hasEvent(c.fields) || hasEvent(fields)
	if !c.sink.wants(entry.Level, event) {
		return nil
	}
	all := fields
	if len(c.fields) > 0 {
		all = make([]zapcore.Field, 0, len(c.fields)+len(fields))
		all = append(append(all, c.fields...), fields...)
	}
	c.sink.write(entry, all, event)
	// 高于 Error 的级别（Fatal、Panic）写完之后进程就退出了：先把队列发出去，否则最想留下的那一条和它前面攒着的都会丢。
	if entry.Level > zapcore.ErrorLevel {
		c.sink.flush()
	}
	return nil
}

// Sync 在进程退出时由 tRPC 调用：最多等 2 秒，把两路队列里剩下的发出去。
func (c *core) Sync() error {
	c.sink.flush()
	return nil
}
