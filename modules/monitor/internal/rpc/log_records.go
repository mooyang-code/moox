package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

const defaultLogRecordPage = 100

// keywordSearchWindow 是带关键字、又没有指定起点的查询默认回看的时间。
const keywordSearchWindow = 7 * 24 * time.Hour

// QueryLogRecords 查询各服务上报的重要日志（Error 及以上、关键事件），新的在前，用 before_id 向更早翻页。
func (s *Service) QueryLogRecords(ctx context.Context, req *monitorpb.QueryLogRecordsReq) (*monitorpb.QueryLogRecordsRsp, error) {
	if s.logs == nil {
		return &monitorpb.QueryLogRecordsRsp{RetInfo: inner(errors.New("重要日志不可用"))}, nil
	}
	filter := store.LogRecordFilter{
		HostID: req.GetHostId(), Component: req.GetComponent(), Level: req.GetLevel(), Keyword: req.GetKeyword(), BeforeID: req.GetBeforeId(),
	}
	if level := strings.ToLower(strings.TrimSpace(req.GetLevel())); level != "" {
		switch level {
		case "debug", "info", "warn", "error", "fatal":
		default:
			return &monitorpb.QueryLogRecordsRsp{RetInfo: invalid(fmt.Errorf("level 只能是 debug、info、warn、error、fatal：%q", req.GetLevel()))}, nil
		}
	}
	var err error
	if filter.Start, err = parseLogTime("start_at", req.GetStartAt()); err != nil {
		return &monitorpb.QueryLogRecordsRsp{RetInfo: invalid(err)}, nil
	}
	if filter.End, err = parseLogTime("end_at", req.GetEndAt()); err != nil {
		return &monitorpb.QueryLogRecordsRsp{RetInfo: invalid(err)}, nil
	}
	// 关键字要逐行匹配正文，不走索引：没有给起点时只查最近 7 天，免得表很大时长时间占着 Monitor 唯一的数据库连接。
	if strings.TrimSpace(filter.Keyword) != "" && filter.Start.IsZero() {
		filter.Start = time.Now().Add(-keywordSearchWindow)
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultLogRecordPage
	}
	if limit > store.MaxLogRecordPage {
		limit = store.MaxLogRecordPage
	}
	// 多取一条，用来判断后面还有没有。
	filter.Limit = limit + 1
	records, err := s.logs.Query(ctx, filter)
	if err != nil {
		return &monitorpb.QueryLogRecordsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &monitorpb.QueryLogRecordsRsp{RetInfo: success(), HasMore: len(records) > limit}
	if rsp.HasMore {
		records = records[:limit]
	}
	rsp.Records = make([]*monitorpb.LogRecord, 0, len(records))
	for _, record := range records {
		rsp.Records = append(rsp.Records, logRecordPB(record))
	}
	return rsp, nil
}

func parseLogTime(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s 不是 RFC 3339 时间：%q", name, value)
	}
	return parsed, nil
}

func logRecordPB(record domain.LogRecord) *monitorpb.LogRecord {
	out := &monitorpb.LogRecord{
		Id: record.ID, HostId: record.HostID, Component: record.Component, InstanceId: record.InstanceID, Level: record.Level,
		Event: record.Event, Message: record.Message, Caller: record.Caller, MergedCount: int32(record.MergedCount),
		LoggedAt: record.LoggedAt.UTC().Format(time.RFC3339Nano),
	}
	if strings.TrimSpace(record.Fields) != "" && record.Fields != "{}" {
		_ = json.Unmarshal([]byte(record.Fields), &out.Fields)
	}
	return out
}
