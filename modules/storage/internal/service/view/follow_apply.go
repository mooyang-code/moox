package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/follow"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/frequency"
	"trpc.group/trpc-go/trpc-go/client"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// followReadPageSize 是按时间范围回读一页的行数（PrimaryStore 区间读取的上限）。
	followReadPageSize = 2000
	// followReadSelectors 是一次回读最多带的标的数（PrimaryStore 的上限）；更多标的共用一个时间窗时不带标的、按时间索引读。
	followReadSelectors = 100
	// followWriteBatchRows 是回读的行攒够多少就先写一批；只有最后一批带游标。
	followWriteBatchRows = 20000
	// followReadTimeout 限制回读的一次调用。
	followReadTimeout = 30 * time.Second
)

// FollowOptions 是 View 跟随变更日志的依赖。
type FollowOptions struct {
	// Changes 读取 PrimaryStore 的变更日志。
	Changes follow.Reader
	// Range 按时间范围回读时序行（PrimaryStore.ReadTimeSeriesRows）。
	Range TimeSeriesRangeReader
	// Publisher 发布 ViewDataReady；Reconnect 在发布因连接问题失败后替换它的连接，可以为空。
	Publisher ReadyEventPublisher
	Reconnect func(context.Context) error
	// Wait、PersistInterval 见 follow.Options，0 取默认值。
	Wait            time.Duration
	PersistInterval time.Duration
}

// StartFollowing 让每个已建好索引的 View 各起一个协程跟随变更日志；之后建好或挂接的 View 自动加入。返回停止函数。
func (s *Service) StartFollowing(ctx context.Context, opts FollowOptions) (func(), error) {
	if s == nil {
		return nil, errors.New("View 服务未初始化")
	}
	if opts.Changes == nil || opts.Range == nil || opts.Publisher == nil {
		return nil, errors.New("View 跟随变更日志需要变更日志读取、时序回读与就绪事件发布器")
	}
	s.mu.RLock()
	missingPrimary := s.primary == nil || s.primaryAuth == nil
	s.mu.RUnlock()
	if missingPrimary {
		return nil, errors.New("View 跟随变更日志需要先设置 Primary 读取客户端与鉴权")
	}
	manager, err := follow.NewManager(ctx, follow.Options{
		Reader: opts.Changes, Auth: s.primaryAuthSnapshot, Wait: opts.Wait, PersistInterval: opts.PersistInterval,
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.follow = manager
	s.primaryRange = opts.Range
	s.readyPublisher = opts.Publisher
	s.readyReconnect = opts.Reconnect
	refs := make([]viewRef, 0, len(s.views))
	for ref := range s.views {
		refs = append(refs, ref)
	}
	s.mu.Unlock()
	s.metrics.SetFollowLagSource(func() []observability.ViewFollowLag {
		lags := manager.Lags(time.Now())
		out := make([]observability.ViewFollowLag, 0, len(lags))
		for _, lag := range lags {
			out = append(out, observability.ViewFollowLag{SpaceID: lag.Key.SpaceID, ViewID: lag.Key.ViewID, Changes: lag.Changes, Seconds: lag.Seconds})
		}
		return out
	})
	for _, ref := range refs {
		s.ensureFollower(ref)
	}
	return func() {
		s.metrics.SetFollowLagSource(nil)
		manager.Close()
		s.mu.Lock()
		if s.follow == manager {
			s.follow = nil
			s.readyPublisher = nil
			s.readyReconnect = nil
		}
		s.mu.Unlock()
	}, nil
}

func (s *Service) followManager() *follow.Manager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.follow
}

// ensureFollower 给已有活动索引的 View 起跟随器（已经在跟随时不动）。
func (s *Service) ensureFollower(ref viewRef) {
	manager := s.followManager()
	if manager == nil {
		return
	}
	s.mu.RLock()
	view := s.catalogViews[ref]
	runtime := s.views[ref]
	s.mu.RUnlock()
	if view == nil || runtime == nil || runtime.queryState().active == "" || strings.TrimSpace(view.GetDatasetId()) == "" {
		return
	}
	manager.Ensure(follow.Key{SpaceID: ref.spaceID, ViewID: ref.viewID, DatasetID: strings.TrimSpace(view.GetDatasetId())}, followTarget{s: s, ref: ref})
}

// followTarget 把一个 View 的应用端交给跟随器。
type followTarget struct {
	s   *Service
	ref viewRef
}

func (t followTarget) LoadState(ctx context.Context) (viewindex.FollowState, error) {
	indexID, engine, err := t.s.followIndex(t.ref)
	if err != nil {
		return viewindex.FollowState{}, err
	}
	stats, err := restoreIndexStats(ctx, engine, indexID)
	if err != nil {
		return viewindex.FollowState{}, err
	}
	if !stats.Exists {
		return viewindex.FollowState{}, fmt.Errorf("View 的活动索引 %q 不存在", indexID)
	}
	return stats.Follow, nil
}

func (t followTarget) SaveState(ctx context.Context, state viewindex.FollowState) error {
	indexID, _, err := t.s.followIndex(t.ref)
	if err != nil {
		return err
	}
	return t.s.saveFollowState(ctx, indexID, state)
}

func (t followTarget) ApplyRows(ctx context.Context, changes []*pb.DatasetChange, cursor viewindex.ChangeCursor) (bool, error) {
	return t.s.applyFollowRows(ctx, t.ref, changes, cursor)
}

func (t followTarget) PublishMarker(ctx context.Context, change *pb.DatasetChange) error {
	return t.s.publishFollowMarker(ctx, t.ref, change)
}

func (t followTarget) Rebuild(ctx context.Context) (viewindex.FollowState, error) {
	return t.s.rebuildFollowIndex(ctx, t.ref)
}

// followIndex 返回 View 当前的活动索引与引擎。
func (s *Service) followIndex(ref viewRef) (string, viewindex.Engine, error) {
	s.mu.RLock()
	runtime := s.views[ref]
	s.mu.RUnlock()
	if runtime == nil {
		return "", nil, fmt.Errorf("View %s/%s 没有运行时", ref.spaceID, ref.viewID)
	}
	indexID := runtime.queryState().active
	if indexID == "" {
		return "", nil, fmt.Errorf("View %s/%s 没有活动索引", ref.spaceID, ref.viewID)
	}
	engine, err := s.engineFor(indexID)
	return indexID, engine, err
}

// saveFollowState 持有索引写锁落盘跟随进度（索引元数据只有一行，与写入、删除最旧数据、加列都要串行）。
func (s *Service) saveFollowState(ctx context.Context, indexID string, state viewindex.FollowState) error {
	engine, err := s.engineFor(indexID)
	if err != nil {
		return err
	}
	writer, ok := engine.(viewindex.FollowStateWriter)
	if !ok {
		return fmt.Errorf("View 索引引擎 %s 不能记录跟随进度", engine.Engine())
	}
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	return writer.SaveFollowState(ctx, indexID, state)
}

// followRead 是一次按时间范围的回读：subjects 为空表示数据集里该时间窗的全部标的。
type followRead struct {
	subjects   []string
	start, end time.Time
}

// applyFollowRows 按一组连续行变更回读 Primary 的整行写入索引。行变更只记“哪些行被写过”，值以 Primary 为准；
// 回读放在运行时锁之外，只有写入索引时才持锁，并在锁内取当前的列契约（期间可能原地加了列）。
func (s *Service) applyFollowRows(ctx context.Context, ref viewRef, changes []*pb.DatasetChange, cursor viewindex.ChangeCursor) (bool, error) {
	applyStart := time.Now()
	s.mu.RLock()
	view := s.catalogViews[ref]
	reader, rangeReader := s.primary, s.primaryRange
	s.mu.RUnlock()
	indexID, _, err := s.followIndex(ref)
	if err != nil {
		return false, err
	}
	if view == nil {
		return false, fmt.Errorf("View %s/%s 不在目录中", ref.spaceID, ref.viewID)
	}
	s.mu.RLock()
	fields := viewColumnFields(s.schemas[indexID].Columns)
	s.mu.RUnlock()
	if len(fields) == 0 {
		return false, nil
	}
	auth := s.primaryAuthSnapshot()
	datasetID := strings.TrimSpace(view.GetDatasetId())
	var pending []viewindex.RowWrite
	rows, persisted := 0, false
	flush := func(withCursor bool) error {
		if len(pending) == 0 {
			return nil
		}
		var at *viewindex.ChangeCursor
		if withCursor {
			at = &cursor
		}
		wrote, err := s.writeFollowRows(ctx, ref, indexID, datasetID, pending, at)
		if err != nil {
			return err
		}
		rows += len(pending)
		persisted = wrote && withCursor
		pending = pending[:0]
		return nil
	}
	if view.GetFreq() != "" {
		if rangeReader == nil {
			return false, errors.New("回读时序行需要 Primary 时序读取客户端")
		}
		for _, read := range s.planFollowReads(view, changes) {
			if err := readFollowRange(ctx, rangeReader, auth, view, read, fields, func(batch []viewindex.RowWrite) error {
				pending = append(pending, batch...)
				if len(pending) >= followWriteBatchRows {
					return flush(false)
				}
				return nil
			}); err != nil {
				return false, err
			}
		}
	} else {
		if reader == nil {
			return false, errors.New("回读记录行需要 Primary 读取客户端")
		}
		records, err := readFollowRecords(ctx, reader, auth, view, changes, fields)
		if err != nil {
			return false, err
		}
		pending = append(pending, records...)
	}
	if err := flush(true); err != nil {
		return false, err
	}
	if total := time.Since(applyStart); total >= slowIndexApplyLog {
		log.Infof("storage view 跟随应用行变更 view=%s/%s 条目=%d 序号=%d～%d 写入行=%d 耗时=%s",
			ref.spaceID, ref.viewID, len(changes), changes[0].GetSequence(), changes[len(changes)-1].GetSequence(), rows, total.Round(time.Millisecond))
	}
	return persisted, nil
}

// writeFollowRows 持有运行时锁写入一批行；cursor 非空时与这批行同一次提交落盘。行全部过期时不写，返回假。
func (s *Service) writeFollowRows(ctx context.Context, ref viewRef, indexID, datasetID string, writes []viewindex.RowWrite, cursor *viewindex.ChangeCursor) (bool, error) {
	s.mu.RLock()
	runtime := s.views[ref]
	s.mu.RUnlock()
	if runtime == nil {
		return false, fmt.Errorf("View %s/%s 没有运行时", ref.spaceID, ref.viewID)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != indexID {
		return false, fmt.Errorf("View %s/%s 的活动索引已变化", ref.spaceID, ref.viewID)
	}
	kept := s.dropExpiredWrites(indexID, writes)
	if len(kept) == 0 {
		return false, nil
	}
	engine, err := s.engineFor(indexID)
	if err != nil {
		return false, err
	}
	s.mu.RLock()
	schema := s.schemas[indexID]
	s.mu.RUnlock()
	writeCtx, cancel, err := liveIndexWriteContext(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	if err := s.writeIndex(writeCtx, indexID, engine, viewindex.ViewIndexWriteBatch{
		RowWrites: kept, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite, Cursor: cursor,
	}); err != nil {
		log.Errorf("storage view 索引写入失败 space=%s view=%s index=%s dataset=%s: %v", ref.spaceID, ref.viewID, indexID, datasetID, err)
		return false, err
	}
	written := make([]*pb.RowFieldUpsert, 0, len(kept))
	for _, write := range kept {
		written = append(written, &pb.RowFieldUpsert{Key: write.Key.Key})
	}
	// Monitor 的 K 线新鲜度与到达延迟靠 View 的输出水位。
	s.observeViewWatermark(indexID, datasetID, written, false)
	return true, nil
}

// planFollowReads 把一组行变更里本 View 频率的时间范围转成回读请求：
//   - 日志区间两端都含、读取接口左闭右开，结束时间加一个频率步长；
//   - 先按 View 的 max_age 截掉已经过期的部分；
//   - 同一标的重叠或相接的范围合并，时间窗相同的标的合成一次读取，标的超过上限时不带标的、按时间索引读。
func (s *Service) planFollowReads(view *pb.View, changes []*pb.DatasetChange) []followRead {
	type span struct{ start, end time.Time }
	freq := view.GetFreq()
	cutoff := s.maxAgeCutoff(view.GetSpaceId(), freq)
	spans := make(map[string][]span)
	for _, change := range changes {
		for _, item := range change.GetRanges() {
			if !strings.EqualFold(strings.TrimSpace(item.GetFreq()), freq) || item.GetSubjectId() == "" {
				continue
			}
			start, startErr := time.Parse(time.RFC3339Nano, item.GetStartTime())
			end, endErr := time.Parse(time.RFC3339Nano, item.GetEndTime())
			if startErr != nil || endErr != nil {
				log.Warnf("storage view 跳过无法解析的变更范围 view=%s/%s 序号=%d 标的=%s 起=%q 止=%q", view.GetSpaceId(), view.GetViewId(), change.GetSequence(), item.GetSubjectId(), item.GetStartTime(), item.GetEndTime())
				continue
			}
			start, end = start.UTC(), followRangeEnd(freq, end.UTC())
			if !cutoff.IsZero() {
				if !end.After(cutoff) {
					continue
				}
				if start.Before(cutoff) {
					start = cutoff
				}
			}
			spans[item.GetSubjectId()] = append(spans[item.GetSubjectId()], span{start: start, end: end})
		}
	}
	groups := make(map[span][]string)
	for subject, list := range spans {
		sort.Slice(list, func(i, j int) bool { return list[i].start.Before(list[j].start) })
		current := list[0]
		for _, next := range list[1:] {
			if !next.start.After(current.end) {
				if next.end.After(current.end) {
					current.end = next.end
				}
				continue
			}
			groups[current] = append(groups[current], subject)
			current = next
		}
		groups[current] = append(groups[current], subject)
	}
	keys := make([]span, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].start.Equal(keys[j].start) {
			return keys[i].start.Before(keys[j].start)
		}
		return keys[i].end.Before(keys[j].end)
	})
	var reads []followRead
	for _, key := range keys {
		subjects := groups[key]
		if len(subjects) > followReadSelectors {
			reads = append(reads, followRead{start: key.start, end: key.end})
			continue
		}
		sort.Strings(subjects)
		reads = append(reads, followRead{subjects: subjects, start: key.start, end: key.end})
	}
	return reads
}

// followRangeEnd 返回包含 end 这一根的左闭右开结束时间：加一个频率步长（月按日历），频率不认识时只加 1 纳秒。
func followRangeEnd(freq string, end time.Time) time.Time {
	if parsed, err := frequency.Parse(freq); err == nil {
		if next := parsed.BarEnd(end); next.After(end) {
			return next
		}
	}
	return end.Add(time.Nanosecond)
}

// readFollowRange 按页回读一个时间窗的行，逐页交给 emit。
func readFollowRange(ctx context.Context, reader TimeSeriesRangeReader, auth *pb.AuthInfo, view *pb.View, read followRead, fields []string, emit func([]viewindex.RowWrite) error) error {
	spaceID, datasetID, freq := view.GetSpaceId(), strings.TrimSpace(view.GetDatasetId()), view.GetFreq()
	selectors := make([]*pb.TimeSeriesSelector, 0, len(read.subjects))
	for _, subject := range read.subjects {
		selectors = append(selectors, &pb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subject, Freq: freq})
	}
	cursor := ""
	for {
		pageCtx, cancel := context.WithTimeout(ctx, followReadTimeout)
		rsp, err := reader.ReadTimeSeriesRows(pageCtx, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID, Selectors: selectors, ColumnNames: fields,
			TimeRange: &pb.TimeRange{StartTime: read.start.Format(time.RFC3339Nano), EndTime: read.end.Format(time.RFC3339Nano)},
			Page:      &pb.Page{Size: followReadPageSize, Cursor: cursor},
		}, client.WithTimeout(followReadTimeout))
		cancel()
		if err == nil {
			err = requireSuccess(rsp.GetRetInfo())
		}
		if err != nil {
			return fmt.Errorf("回读 %s/%s [%s, %s) 的行：%w", spaceID, datasetID, read.start.Format(time.RFC3339Nano), read.end.Format(time.RFC3339Nano), err)
		}
		writes := make([]viewindex.RowWrite, 0, len(rsp.GetRows()))
		for _, row := range rsp.GetRows() {
			key := row.GetKey()
			if key == nil || !strings.EqualFold(strings.TrimSpace(key.GetFreq()), freq) {
				continue
			}
			writes = append(writes, viewindex.RowWrite{
				Key: viewindex.RowKey{Key: &pb.RowKey{
					SpaceId: spaceID, DatasetId: datasetID,
					Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()}},
				}},
				Fields: row.GetFields(),
			})
		}
		if len(writes) > 0 {
			if err := emit(writes); err != nil {
				return err
			}
		}
		cursor = rsp.GetPageResult().GetNextCursor()
		if cursor == "" {
			return nil
		}
	}
}

// readFollowRecords 按记录键回读一组行变更里的记录（只返回仍存在的）。
func readFollowRecords(ctx context.Context, reader FieldReader, auth *pb.AuthInfo, view *pb.View, changes []*pb.DatasetChange, fields []string) ([]viewindex.RowWrite, error) {
	spaceID, datasetID := view.GetSpaceId(), strings.TrimSpace(view.GetDatasetId())
	seen := make(map[[2]string]struct{})
	var keys []*pb.RowKey
	for _, change := range changes {
		for _, record := range change.GetRecords() {
			id := [2]string{record.GetRecordId(), record.GetVersion()}
			if _, ok := seen[id]; ok || id[0] == "" {
				continue
			}
			seen[id] = struct{}{}
			keys = append(keys, &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: id[0], Version: id[1]}}})
		}
	}
	chunk := primaryPointReadChunkSize(len(fields), len(keys))
	writes := make([]viewindex.RowWrite, 0, len(keys))
	for from := 0; from < len(keys); from += chunk {
		to := min(from+chunk, len(keys))
		readCtx, cancel := context.WithTimeout(ctx, followReadTimeout)
		rsp, err := reader.ReadRows(readCtx, &pb.PrimaryReadRowsReq{AuthInfo: auth, Keys: keys[from:to], ColumnNames: fields}, client.WithTimeout(followReadTimeout))
		cancel()
		if err == nil {
			err = requireSuccess(rsp.GetRetInfo())
		}
		if err != nil {
			return nil, fmt.Errorf("回读 %s/%s 的记录：%w", spaceID, datasetID, err)
		}
		for _, row := range rsp.GetRows() {
			if row.GetKey().GetRecord() == nil {
				continue
			}
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: row.GetKey()}, Fields: row.GetFields()})
		}
	}
	return writes, nil
}
