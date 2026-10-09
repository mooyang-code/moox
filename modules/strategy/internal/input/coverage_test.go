package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/events"
	"trpc.group/trpc-go/trpc-go/client"
)

// metadataStub 返回预设的 View、一页列、标的绑定与标签。
type metadataStub struct {
	storagepb.MetadataClientProxy
	view     *storagepb.View
	subjects []*storagepb.Subject
	tagCode  commonpb.ErrorCode
}

func success() *commonpb.RetInfo { return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS} }

func (s *metadataStub) GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error) {
	return &storagepb.GetViewRsp{RetInfo: success(), View: s.view}, nil
}

func (s *metadataStub) ListViewColumns(context.Context, *storagepb.ListViewColumnsReq, ...client.Option) (*storagepb.ListViewColumnsRsp, error) {
	return &storagepb.ListViewColumnsRsp{RetInfo: success(), Columns: []*storagepb.ViewColumn{{ColumnName: "close"}}, PageResult: &commonpb.PageResult{}}, nil
}

func (s *metadataStub) ListDatasetSubjects(_ context.Context, _ *storagepb.ListDatasetSubjectsReq, _ ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	bindings := make([]*storagepb.DatasetSubject, 0, len(s.subjects))
	for _, subject := range s.subjects {
		bindings = append(bindings, &storagepb.DatasetSubject{SubjectId: subject.GetSubjectId(), Status: "active"})
	}
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: success(), DatasetSubjects: bindings, PageResult: &commonpb.PageResult{}}, nil
}

func (s *metadataStub) ListSubjects(context.Context, *storagepb.ListSubjectsReq, ...client.Option) (*storagepb.ListSubjectsRsp, error) {
	return &storagepb.ListSubjectsRsp{RetInfo: success(), Subjects: s.subjects, PageResult: &commonpb.PageResult{}}, nil
}

func (s *metadataStub) GetTag(_ context.Context, req *storagepb.GetTagReq, _ ...client.Option) (*storagepb.GetTagRsp, error) {
	if s.tagCode != commonpb.ErrorCode_SUCCESS {
		return &storagepb.GetTagRsp{RetInfo: &commonpb.RetInfo{Code: s.tagCode, Msg: "tag not found"}}, nil
	}
	return &storagepb.GetTagRsp{RetInfo: success(), Tag: &storagepb.Tag{TagId: req.GetTagId(), MarketType: "spot"}}, nil
}

// coverageStub 按探测请求的 TotalMode 返回覆盖范围：NONE 时只有已缓存才返回。
type coverageStub struct {
	storagepb.DataViewClientProxy
	cached   bool
	requests []*storagepb.QueryTimeSeriesRowsReq
}

func (s *coverageStub) QueryTimeSeriesRows(_ context.Context, req *storagepb.QueryTimeSeriesRowsReq, _ ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	s.requests = append(s.requests, req)
	rsp := &storagepb.QueryTimeSeriesRowsRsp{RetInfo: success(), PageResult: &commonpb.PageResult{}, ServedSeriesBars: 5000}
	if s.cached || req.GetTotalMode() == commonpb.TotalMode_FORCE_EXACT {
		rsp.ServedIndexedFrom, rsp.ServedIndexedTo = "2026-09-01T00:00:00.000000000Z", "2026-09-30T23:00:00.000000000Z"
	}
	return rsp, nil
}

// Metadata 的 View 记录不维护覆盖范围（生产上恒为空）：GetView 以 DataView 服务该 View 的活动索引统计为准。
// 探测查询固定活动索引、只带时间范围、不命中任何行；统计未缓存时再要求现算一次。
func TestGetViewReadsCoverageFromDataView(t *testing.T) {
	metadata := &metadataStub{view: &storagepb.View{ViewId: "view", DatasetId: "ds", Freq: "1h", Status: "active", ActiveIndexId: "idx_a", IndexedFrom: "2001-01-01T00:00:00Z"}}
	cached := &coverageStub{cached: true}
	info, err := (&RPCClient{Metadata: metadata, DataView: cached}).GetView(context.Background(), "space", "view")
	if err != nil {
		t.Fatal(err)
	}
	if !info.IndexedFrom.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) || !info.IndexedTo.Equal(time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)) || info.SeriesBars != 5000 {
		t.Fatalf("覆盖范围与保留根数应取 DataView 的统计：%s ~ %s bars=%d", info.IndexedFrom, info.IndexedTo, info.SeriesBars)
	}
	probe := cached.requests[0]
	if len(cached.requests) != 1 || probe.GetExpectedActiveIndexId() != "idx_a" || probe.GetLimit() != 1 || probe.GetTimeRange() == nil || len(probe.GetSelectors()) != 0 || probe.GetTotalMode() != commonpb.TotalMode_NONE {
		t.Fatalf("已缓存时只发一次不触发全量统计的探测：%v", cached.requests)
	}

	uncached := &coverageStub{}
	info, err = (&RPCClient{Metadata: metadata, DataView: uncached}).GetView(context.Background(), "space", "view")
	if err != nil || info.IndexedFrom.IsZero() || len(uncached.requests) != 2 || uncached.requests[1].GetTotalMode() != commonpb.TotalMode_FORCE_EXACT {
		t.Fatalf("统计未缓存时应要求现算：%v requests=%v err=%v", info.IndexedFrom, uncached.requests, err)
	}

	metadata.view.Status = "disabled"
	idle := &coverageStub{}
	if _, err := (&RPCClient{Metadata: metadata, DataView: idle}).GetView(context.Background(), "space", "view"); err != nil || len(idle.requests) != 0 {
		t.Fatalf("非活动 View 不应探测覆盖范围：requests=%d err=%v", len(idle.requests), err)
	}
}

// 序列标签只认标的显式声明的 series_tag：stockcn 标的只有 exchange 属性，按交易所派生的标签与行上的 default 不符，
// 会把全部行过滤掉。标签不存在时报“不存在”，不把传输失败说成拼写错误。
func TestSubjectSeriesTagAndTagErrors(t *testing.T) {
	metadata := &metadataStub{subjects: []*storagepb.Subject{
		{SubjectId: "600000.SH", Status: "active", Attributes: map[string]string{"exchange": "SH"}},
		{SubjectId: "BTC-USDT", Status: "active", Attributes: map[string]string{"series_tag": "venue:binance"}},
	}}
	subjects, err := (&RPCClient{Metadata: metadata}).ListDatasetSubjects(context.Background(), "space", "ds")
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]string{}
	for _, subject := range subjects {
		tags[subject.SubjectID] = subject.SeriesTag
	}
	if tags["600000.SH"] != "" || tags["BTC-USDT"] != "venue:binance" {
		t.Fatalf("序列标签应只取显式的 series_tag：%v", tags)
	}

	metadata.tagCode = commonpb.ErrorCode_NOT_FOUND
	if _, err := (&RPCClient{Metadata: metadata}).GetTag(context.Background(), "space", "missing"); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("标签不存在应返回 ErrTagNotFound：%v", err)
	}
	strategy := parseStrategy(t, strings.Replace(exampleDSL, "exclude_tags: [stablecoins]", "exclude_tags: [no_such_tag]", 1))
	if _, _, err := Resolve(context.Background(), newFakeClient("spot"), "space", "view_factor_1h", strategy); err == nil || !strings.Contains(err.Error(), "标签 no_such_tag 不存在（ID 区分大小写）") {
		t.Fatalf("引用不存在的标签应拒绝启用：%v", err)
	}
	client := newFakeClient("spot")
	client.tags["no_such_tag"] = nil
	unreachable := &tagFailingClient{fakeClient: client, err: &TransportError{Operation: "读取标签 no_such_tag", Err: errors.New("dial tcp 10.0.0.1:443: i/o timeout")}}
	if _, _, err := Resolve(context.Background(), unreachable, "space", "view_factor_1h", strategy); err == nil || strings.Contains(err.Error(), "不存在") || !strings.Contains(err.Error(), "暂时不可用") {
		t.Fatalf("读取标签的传输失败不应报成标签不存在：%v", err)
	}
}

// tagFailingClient 让读取标签返回给定错误。
type tagFailingClient struct {
	*fakeClient
	err error
}

func (c *tagFailingClient) GetTag(context.Context, string, string) (TagInfo, error) {
	return TagInfo{}, c.err
}

// 只有 Factor 的结果数据集由 factor_period.computed 宣布完成；Collector 的重采样 K 线虽然也带 source_dataset_id，
// 仍由 collector.period.completed 宣布，判错会让实例永远不被触发。
func TestResolveCompletionKindByDatasetRole(t *testing.T) {
	strategy := parseStrategy(t, exampleDSL)
	for _, tc := range []struct {
		role string
		want string
	}{
		{role: "factor_result", want: events.FactorPeriodComputed.Name()},
		{role: "kline_resample_result", want: events.CollectorPeriodCompleted.Name()},
	} {
		client := newFakeClient("spot")
		dataset := client.datasets["ds_factor"]
		dataset.Attributes = map[string]string{"source_dataset_id": "ds_kline", "dataset_role": tc.role}
		client.datasets["ds_factor"] = dataset
		resolved, _, err := Resolve(context.Background(), client, "space", "view_factor_1h", strategy)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.CompletionKind != tc.want || resolved.SourceDatasetID != "ds_kline" {
			t.Fatalf("dataset_role=%s 的完成事件应为 %s：%+v", tc.role, tc.want, resolved)
		}
	}
}

// 活跃序列可回溯的起点：索引最早时间可能来自已停更的序列（只会提前），以“最新一根往前 SeriesBars−1 根”为界。
// min_age_bars 超过每序列保留根数、或覆盖范围未知时，启用时就拒绝。
func TestCoverageStartAndAgeCoverage(t *testing.T) {
	resolved := Resolved{ViewID: "view", Calendar: DefaultCalendar, Bar: "1h"}
	view := ViewInfo{IndexedFrom: barStart.Add(-10000 * time.Hour), IndexedTo: barStart, SeriesBars: 100}
	if start := CoverageStart(view, resolved); !start.Equal(barStart.Add(-99 * time.Hour)) {
		t.Fatalf("活跃序列的覆盖起点应是最新一根往前 99 根：%s", start)
	}
	view.SeriesBars = 0
	if start := CoverageStart(view, resolved); !start.Equal(view.IndexedFrom) {
		t.Fatalf("保留根数未知时以索引统计为准：%s", start)
	}
	if !CoverageStart(ViewInfo{}, resolved).IsZero() {
		t.Fatal("覆盖范围未知时应返回零值")
	}
	view.SeriesBars = 100
	if err := checkAgeCoverage(view, resolved, 120); err == nil || !strings.Contains(err.Error(), "每个序列保留的 100 根") {
		t.Fatalf("min_age_bars 超过保留根数应拒绝：%v", err)
	}
	if err := checkAgeCoverage(view, resolved, 100); err != nil {
		t.Fatalf("恰好等于保留根数应允许：%v", err)
	}
	if err := checkAgeCoverage(ViewInfo{}, resolved, 10); err == nil || !strings.Contains(err.Error(), "覆盖范围未知") {
		t.Fatalf("覆盖范围未知时应拒绝启用：%v", err)
	}
}

// 回放终点截到最新一根之前（最新一根可能还没写完）；起点以活跃序列的覆盖起点为准，不被停更序列的旧行放宽。
func TestReplayWindowUsesActiveCoverage(t *testing.T) {
	resolved := Resolved{ViewID: "view", Calendar: DefaultCalendar, Bar: "1h"}
	view := ViewInfo{IndexedFrom: barStart.Add(-10000 * time.Hour), IndexedTo: barStart, SeriesBars: 100}
	end, err := ReplayWindow(resolved, nil, view, barStart.Add(-50*time.Hour), barStart.Add(10*time.Hour))
	if err != nil || !end.Equal(barStart) {
		t.Fatalf("终点应截到最新一根的 bar_start：%s err=%v", end, err)
	}
	if _, err := ReplayWindow(resolved, nil, view, barStart.Add(-500*time.Hour), barStart); err == nil || !strings.Contains(err.Error(), "可用起点为 "+barStart.Add(-99*time.Hour).Format(time.RFC3339)) {
		t.Fatalf("起点早于活跃序列的覆盖起点应给出可用起点：%v", err)
	}
	if _, err := ReplayWindow(resolved, nil, ViewInfo{}, barStart.Add(-5*time.Hour), barStart); err == nil || !strings.Contains(err.Error(), "覆盖范围未知") {
		t.Fatalf("覆盖范围未知时应拒绝回放：%v", err)
	}
}

// A 股日历只解析一次，交易日序号用二分查找；回溯过远（溢出）报错而不是得到一个错误的时间。
func TestStockCalendarCachedAndOverflowChecked(t *testing.T) {
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	first, err := FromStorageStart("cn_stock", "1d", day)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for i := 0; i < 2000; i++ {
		again, err := FromStorageStart("cn_stock", "1d", day)
		if err != nil || again.BarIndex != first.BarIndex {
			t.Fatalf("同一交易日的序号应稳定：%d / %d err=%v", again.BarIndex, first.BarIndex, err)
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("日历应只加载一次：2000 次换算耗时 %s", elapsed)
	}
	next, err := FromBarEnd("cn_stock", "1d", first.NextEnd)
	if err != nil || next.BarIndex != first.BarIndex+1 {
		t.Fatalf("下一个交易日的序号应加 1：%d / %d err=%v", next.BarIndex, first.BarIndex, err)
	}
	if _, err := HistoryStart(DefaultCalendar, "1d", barStart, 200000); err == nil {
		t.Fatal("回溯 20 万根日线超出时间范围，应报错")
	}
	if _, err := AdvanceBarEnd(DefaultCalendar, "1d", barStart, -200000); err == nil {
		t.Fatal("推进 20 万根日线超出时间范围，应报错")
	}
}

// 一次事件内多个实例绑定同一 View：元数据只读一次；遇到 ErrStale 后丢弃缓存的 View 重新读取。
func TestEventLoaderSharesMetadata(t *testing.T) {
	client := &countingClient{fakeClient: newFakeClient("spot")}
	loader := Loader{Client: client}.ForEvent()
	for i := 0; i < 3; i++ {
		if _, err := loader.Client.GetView(context.Background(), "space", "view_factor_1h"); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.Client.ListDatasetSubjects(context.Background(), "space", "ds_factor"); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.Client.ListTagMembers(context.Background(), "space", "majors"); err != nil {
			t.Fatal(err)
		}
	}
	if client.views != 1 || client.subjects != 1 || client.members != 1 {
		t.Fatalf("事件内元数据应只读一次：views=%d subjects=%d members=%d", client.views, client.subjects, client.members)
	}
	loader.Invalidate()
	if _, err := loader.Client.GetView(context.Background(), "space", "view_factor_1h"); err != nil || client.views != 2 {
		t.Fatalf("Invalidate 后应重新读取 View：%d err=%v", client.views, err)
	}
	if fresh := (Loader{Client: client}).ForEvent(); fresh.Client == loader.Client {
		t.Fatal("每个事件应有独立的缓存")
	}
}

// countingClient 统计元数据读取次数。
type countingClient struct {
	*fakeClient
	views, subjects, members int
}

func (c *countingClient) GetView(ctx context.Context, spaceID, viewID string) (ViewInfo, error) {
	c.views++
	return c.fakeClient.GetView(ctx, spaceID, viewID)
}

func (c *countingClient) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]Subject, error) {
	c.subjects++
	return c.fakeClient.ListDatasetSubjects(ctx, spaceID, datasetID)
}

func (c *countingClient) ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error) {
	c.members++
	return c.fakeClient.ListTagMembers(ctx, spaceID, tagID)
}

// 回放的分段读取遇到传输失败时退避重读同一段，不让一次网络抖动结束整个回放。
func TestRangeLoaderRetriesTransportErrors(t *testing.T) {
	previous := rangeRetryBackoff
	rangeRetryBackoff = time.Millisecond
	defer func() { rangeRetryBackoff = previous }()
	client := newFakeClient("spot")
	failures := 2
	client.rows = func(query Query) ([]Row, uint64, error) {
		if failures > 0 {
			failures--
			return nil, 0, &TransportError{Operation: "读取 View view_factor_1h 的行", Err: errors.New("connection reset")}
		}
		return rowsAt(query.Start, map[string]map[string]float64{"ETH-USDT": {"close": 10}}), 3, nil
	}
	loader := &RangeLoader{Client: client, SpaceID: "space", View: client.views["view_factor_1h"], Subjects: []Subject{subject("ETH-USDT", true)}, Columns: []string{"close"}}
	rows, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour))
	if err != nil || len(rows.Bars) != 1 || len(client.queries) != 3 {
		t.Fatalf("两次传输失败后应重读成功：bars=%d queries=%d err=%v", len(rows.Bars), len(client.queries), err)
	}
	failures = 10
	client.queries = nil
	if _, err := loader.Load(context.Background(), barStart, barStart.Add(time.Hour)); err == nil || len(client.queries) != 1+maxRangeTransportRetries {
		t.Fatalf("持续失败时应在有限次重试后放弃：queries=%d err=%v", len(client.queries), err)
	}
}
