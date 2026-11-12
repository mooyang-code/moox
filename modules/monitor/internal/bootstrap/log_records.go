package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// 重要日志按日志时间保留 30 天，并且最多保留这么多行（全系统 Error 级日志一天只有几条，上限只防异常刷量）。
	logRecordRetention = 30 * 24 * time.Hour
	logRecordMaxRows   = 200000
	// 时间戳偏离当前时间太远的记录按当前时间入库，免得一条错误的时间戳被清理策略立刻删掉或长期留着。
	logRecordMaxClockSkew = 24 * time.Hour
)

var logRecordsIngested = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "moox_monitor_log_records_total",
	Help: "Important log records received by Monitor: result=stored (new rows), duplicate (already stored), failed (database write failed).",
}, []string{"result"})

func init() {
	prometheus.MustRegister(logRecordsIngested)
}

// logObservabilityRoute 把各服务上报的一批重要日志写进库。同一条记录重复投递时只保留一条。
// 写库失败返回错误，由消费者按次数重投；这里不写 Error 日志（Monitor 自己的 Error 也会进这张表）。
func logObservabilityRoute(repository *store.LogRecordRepository) func(context.Context, *eventpb.EventMessage, *metricspb.LogReport) error {
	return func(ctx context.Context, _ *eventpb.EventMessage, payload *metricspb.LogReport) error {
		if repository == nil {
			return nil
		}
		identity := logsink.Identity{ServiceName: payload.GetServiceName(), InstanceID: payload.GetInstanceId(), NodeID: payload.GetNodeId(), BootID: payload.GetBootId()}
		records := make([]domain.LogRecord, 0, len(payload.GetRecords()))
		now := time.Now()
		for _, item := range payload.GetRecords() {
			records = append(records, logRecordRow(identity, logsink.Record{
				Sequence: item.GetSequence(), Time: time.Unix(0, item.GetTimeUnixNano()), Level: item.GetLevel(), Event: item.GetEvent(),
				Message: item.GetMessage(), Caller: item.GetCaller(), Fields: item.GetFields(), MergedCount: item.GetMergedCount(),
			}, now))
		}
		return storeLogRecords(ctx, repository, records)
	}
}

// attachOwnLogs 让 Monitor 自己的重要日志直接写库。日志输出包已经在后台协程里攒批（每秒一批），不会每条日志抢一次库连接。
func attachOwnLogs(repository *store.LogRecordRepository) {
	if repository == nil {
		return
	}
	cfg := report.DefaultConfig("monitor", "monitor")
	identity := logsink.Identity{ServiceName: cfg.ServiceName, InstanceID: cfg.InstanceID, NodeID: cfg.NodeID, BootID: cfg.BootID}
	if strings.TrimSpace(identity.BootID) == "" {
		// 没有部署环境变量时（本地运行）用启动时刻区分每次启动，保证记录号不重复。
		identity.BootID = "local-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	logsink.Attach(identity, ownLogDeliver(repository))
}

// ownLogWriteTimeout 是 Monitor 把自己的重要日志写进库的最长等待。
const ownLogWriteTimeout = 30 * time.Second

// ownLogDeliver 把 Monitor 自己的一批重要日志直接写库。
//
// 不沿用日志输出包给的 5 秒期限：Monitor 只有一条数据库连接，刚启动时它被一批启动查询占着（线上实测 6.5 秒），
// 5 秒一到这批日志就被丢掉，启动记录因此进不了库。这里等得久一些；等待期间新的日志留在输出包的队列里。
func ownLogDeliver(repository *store.LogRecordRepository) logsink.Deliver {
	return func(ctx context.Context, identity logsink.Identity, batch []logsink.Record) error {
		records := make([]domain.LogRecord, 0, len(batch))
		now := time.Now()
		for _, item := range batch {
			records = append(records, logRecordRow(identity, item, now))
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ownLogWriteTimeout)
		defer cancel()
		return storeLogRecords(ctx, repository, records)
	}
}

func storeLogRecords(ctx context.Context, repository *store.LogRecordRepository, records []domain.LogRecord) error {
	stored, err := repository.InsertBatch(ctx, records)
	if err != nil {
		logRecordsIngested.WithLabelValues("failed").Add(float64(len(records)))
		return errors.New("store log records: " + err.Error())
	}
	logRecordsIngested.WithLabelValues("stored").Add(float64(stored))
	logRecordsIngested.WithLabelValues("duplicate").Add(float64(int64(len(records)) - stored))
	return nil
}

func logRecordRow(identity logsink.Identity, record logsink.Record, now time.Time) domain.LogRecord {
	loggedAt := record.Time
	if loggedAt.Before(now.Add(-logRecordMaxClockSkew)) || loggedAt.After(now.Add(logRecordMaxClockSkew)) {
		loggedAt = now
	}
	fields := "{}"
	if len(record.Fields) > 0 {
		if encoded, err := json.Marshal(record.Fields); err == nil {
			fields = string(encoded)
		}
	}
	return domain.LogRecord{
		RecordKey: identity.InstanceID + "|" + identity.BootID + "|" + strconv.FormatUint(record.Sequence, 10),
		HostID:    identity.NodeID, Component: identity.ServiceName, InstanceID: identity.InstanceID,
		Level: strings.ToLower(strings.TrimSpace(record.Level)), Event: record.Event, Message: record.Message, Caller: record.Caller,
		Fields: fields, MergedCount: int(record.MergedCount), LoggedAt: loggedAt.UTC(),
	}
}
