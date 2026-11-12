package report

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"google.golang.org/protobuf/proto"
)

// maxLogEventBytes 是一个日志事件里记录的总大小上限（压缩前）。一批太大时拆成几个事件，免得超过 EventBus 的单条消息上限。
const maxLogEventBytes = 256 << 10

// attachLogSink 把本进程的重要日志（Error 及以上、关键事件）接到 EventBus：和指标快照用同一份地址与凭据，发给 Monitor。
// 进程没有装配 moox 输出（main 没有引入 logcls），或者已经有别的接收方（Monitor 直接写自己的库）时，什么都不做。
func attachLogSink(cfg Config) {
	if !logsink.Enabled() {
		return
	}
	publisher := &logPublisher{cfg: cfg, connector: connectLogs, now: time.Now}
	logsink.Attach(logsink.Identity{
		ServiceName: cfg.ServiceName, InstanceID: cfg.InstanceID, NodeID: cfg.NodeID, BootID: cfg.BootID,
	}, publisher.deliver)
}

// logPublisher 用自己的一条连接发布日志事件，不和指标上报共用：Handler 在发布失败时会关掉它的连接重连，共用会互相打断。
// 断线由底层客户端自己重连；这里只在发布失败后丢掉连接，下一批重新建立。
//
// 这条路径上不能写 Error 级别的日志：它们会重新进入重要日志的队列。
type logPublisher struct {
	cfg       Config
	connector Connector
	now       func() time.Time

	mu     sync.Mutex
	client Publisher
	batch  atomic.Uint64
}

func connectLogs(ctx context.Context, cfg Config) (Publisher, error) {
	return connectAs(ctx, cfg, "moox-"+cfg.ServiceName+"-logs")
}

func (p *logPublisher) publisher(ctx context.Context) (Publisher, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	client, err := p.connector(ctx, p.cfg)
	if err != nil {
		return nil, err
	}
	p.client = client
	return client, nil
}

func (p *logPublisher) invalidate(client Publisher) {
	p.mu.Lock()
	if p.client == client {
		p.client = nil
	}
	p.mu.Unlock()
	if closer, ok := client.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func (p *logPublisher) deliver(ctx context.Context, identity logsink.Identity, records []logsink.Record) error {
	client, err := p.publisher(ctx)
	if err != nil {
		return err
	}
	for _, chunk := range splitLogRecords(records) {
		payload := &metricspb.LogReport{
			ServiceName: identity.ServiceName, InstanceId: identity.InstanceID, NodeId: identity.NodeID, BootId: identity.BootID,
			Records: chunk,
		}
		// 事件号是“启动号 + 批次序号”，和指标快照的做法一致：进程重启后序号从头开始，靠启动号区分。
		options := events.PublishOptions{
			EventID:    fmt.Sprintf("%s-log-%020d", identity.BootID, p.batch.Add(1)),
			OccurredAt: p.now().UTC(), SpaceID: p.cfg.SpaceID,
			SubjectID: identity.ServiceName + "/" + identity.InstanceID,
		}
		if _, err := client.Publish(ctx, events.ObservabilityLogReported, payload, options); err != nil {
			p.invalidate(client)
			return err
		}
	}
	return nil
}

// splitLogRecords 把一批记录转成协议消息，并按条数与总大小拆成几组。
func splitLogRecords(records []logsink.Record) [][]*metricspb.LogRecord {
	var chunks [][]*metricspb.LogRecord
	var current []*metricspb.LogRecord
	size := 0
	for _, record := range records {
		item := &metricspb.LogRecord{
			Sequence: record.Sequence, TimeUnixNano: record.Time.UnixNano(), Level: record.Level, Event: record.Event,
			Message: record.Message, Caller: record.Caller, Fields: record.Fields, MergedCount: record.MergedCount,
		}
		itemSize := proto.Size(item)
		if len(current) > 0 && (len(current) == events.MaxLogReportRecords || size+itemSize > maxLogEventBytes) {
			chunks = append(chunks, current)
			current, size = nil, 0
		}
		current = append(current, item)
		size += itemSize
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}
