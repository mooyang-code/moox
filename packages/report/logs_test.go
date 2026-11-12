package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

type logPublish struct {
	event   events.Event
	payload *metricspb.LogReport
	options events.PublishOptions
}

type fakeLogClient struct {
	published []logPublish
	err       error
	closed    int
}

func (c *fakeLogClient) Publish(_ context.Context, event events.Event, payload proto.Message, options events.PublishOptions) (*jetstream.PublishAck, error) {
	if c.err != nil {
		return nil, c.err
	}
	c.published = append(c.published, logPublish{event, payload.(*metricspb.LogReport), options})
	return &jetstream.PublishAck{}, nil
}

func (c *fakeLogClient) Close() error { c.closed++; return nil }

var logIdentity = logsink.Identity{ServiceName: "trade", InstanceID: "trade@overseas", NodeID: "overseas", BootID: "boot-7"}

// 一批日志发成一个事件：事件号带启动号，主题是“服务/实例”，记录原样带上；事件能通过注册表的校验。
func TestLogPublisherPublishesOneEventPerBatch(t *testing.T) {
	client := &fakeLogClient{}
	connects := 0
	publisher := &logPublisher{
		cfg:       Config{ServiceName: "trade", SpaceID: DefaultSpace},
		connector: func(context.Context, Config) (Publisher, error) { connects++; return client, nil },
		now:       func() time.Time { return time.Unix(1700000100, 0) },
	}
	records := []logsink.Record{
		{Sequence: 1, Time: time.Unix(1700000000, 5), Level: "error", Message: "下单失败", Caller: "order/submit.go:88", Fields: map[string]string{"account": "a1"}},
		{Sequence: 2, Time: time.Unix(1700000001, 0), Level: "info", Event: "process_started", Message: "启动", MergedCount: 3},
	}
	if err := publisher.deliver(context.Background(), logIdentity, records); err != nil {
		t.Fatal(err)
	}
	if err := publisher.deliver(context.Background(), logIdentity, records[:1]); err != nil {
		t.Fatal(err)
	}
	if connects != 1 || len(client.published) != 2 {
		t.Fatalf("connects = %d, events = %d", connects, len(client.published))
	}
	first := client.published[0]
	if first.event.Name() != events.ObservabilityLogReported.Name() || first.options.SubjectID != "trade/trade@overseas" || first.options.SpaceID != DefaultSpace {
		t.Fatalf("event = %s, options = %+v", first.event.Name(), first.options)
	}
	if first.options.EventID != "boot-7-log-00000000000000000001" || client.published[1].options.EventID != "boot-7-log-00000000000000000002" {
		t.Fatalf("event ids = %q, %q", first.options.EventID, client.published[1].options.EventID)
	}
	got := first.payload
	if got.GetBootId() != "boot-7" || got.GetNodeId() != "overseas" || len(got.GetRecords()) != 2 {
		t.Fatalf("payload = %v", got)
	}
	if r := got.GetRecords()[0]; r.GetSequence() != 1 || r.GetTimeUnixNano() != time.Unix(1700000000, 5).UnixNano() || r.GetCaller() != "order/submit.go:88" || r.GetFields()["account"] != "a1" {
		t.Fatalf("record = %v", r)
	}
	if r := got.GetRecords()[1]; r.GetEvent() != "process_started" || r.GetMergedCount() != 3 {
		t.Fatalf("record = %v", r)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Encode(first.event, first.payload, first.options); err != nil {
		t.Fatalf("事件没有通过校验：%v", err)
	}
}

// 发布失败后丢掉这条连接，下一批重新连接。
func TestLogPublisherReconnectsAfterFailure(t *testing.T) {
	broken, healthy := &fakeLogClient{err: errors.New("publish timeout")}, &fakeLogClient{}
	clients := []*fakeLogClient{broken, healthy}
	publisher := &logPublisher{
		cfg: Config{ServiceName: "trade", SpaceID: DefaultSpace}, now: time.Now,
		connector: func(context.Context, Config) (Publisher, error) {
			next := clients[0]
			clients = clients[1:]
			return next, nil
		},
	}
	records := []logsink.Record{{Sequence: 1, Time: time.Unix(1700000000, 0), Level: "error", Message: "x"}}
	if err := publisher.deliver(context.Background(), logIdentity, records); err == nil {
		t.Fatal("发布失败应返回错误")
	}
	if broken.closed != 1 {
		t.Fatalf("失败的连接应被关闭一次，实际 %d 次", broken.closed)
	}
	if err := publisher.deliver(context.Background(), logIdentity, records); err != nil || len(healthy.published) != 1 {
		t.Fatalf("重连后应发布成功：err = %v, events = %d", err, len(healthy.published))
	}
}

// 记录很大或很多时拆成几个事件：每个不超过条数上限，总大小不超过上限。
func TestSplitLogRecordsRespectsCountAndSize(t *testing.T) {
	var many []logsink.Record
	for i := 0; i < events.MaxLogReportRecords+5; i++ {
		many = append(many, logsink.Record{Sequence: uint64(i + 1), Time: time.Unix(1700000000, 0), Level: "error", Message: "x"})
	}
	if chunks := splitLogRecords(many); len(chunks) != 2 || len(chunks[0]) != events.MaxLogReportRecords || len(chunks[1]) != 5 {
		t.Fatalf("按条数拆分：%d 组", len(chunks))
	}
	big := strings.Repeat("x", 4000)
	var large []logsink.Record
	for i := 0; i < 50; i++ {
		large = append(large, logsink.Record{Sequence: uint64(i + 1), Time: time.Unix(1700000000, 0), Level: "error", Message: big, Fields: map[string]string{"a": big[:1000], "b": big[:1000], "c": big[:1000]}})
	}
	chunks := splitLogRecords(large)
	total := 0
	for _, chunk := range chunks {
		size := 0
		for _, record := range chunk {
			size += proto.Size(record)
		}
		if size > maxLogEventBytes {
			t.Fatalf("一组 %d 字节，超过上限", size)
		}
		total += len(chunk)
	}
	if len(chunks) < 2 || total != 50 {
		t.Fatalf("按大小拆分：%d 组、共 %d 条", len(chunks), total)
	}
}

// 常驻服务的 NewHandler 要把重要日志接到 EventBus：去掉这一步，所有服务的重要日志会悄悄消失。
func TestNewHandlerAttachesImportantLogs(t *testing.T) {
	// 装配 moox 输出，相当于服务读完日志配置之后的状态。
	if err := log.GetWriter(logsink.WriterName).Setup(logsink.WriterName, &log.Decoder{OutputConfig: &log.OutputConfig{}}); err != nil {
		t.Fatal(err)
	}
	if !logsink.Enabled() {
		t.Fatal("moox 输出没有装配")
	}
	if _, err := NewHandler(validConfig("attach-test")); err != nil {
		t.Fatal(err)
	}
	// 接收方只认第一次：NewHandler 已经接上的话，这里再接不会生效。
	if logsink.Attach(logIdentity, func(context.Context, logsink.Identity, []logsink.Record) error { return nil }) {
		t.Fatal("NewHandler 没有接上重要日志")
	}
}
