package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// 本地写入（设计 7.1）：导入服务通过 EngineControl 的 WriteSeries、GetSeriesFields、CheckSeriesSubject、GetSeriesBounds
// 把历史数据写进因子机的本地时序存储。本地存储同一时刻只能由引擎进程打开，所以写入只能经过引擎；引擎未就绪时也可以写入。
// 失败都放在 ret_info 里：请求不合法、缺周期、字段对不上为 INVALID_PARAM，读不到数据集字段、写本地存储失败为 INNER_ERR。

const fieldsCacheTTL = time.Minute

// seriesStorage 是本地写入用到的 Storage 能力。
type seriesStorage interface {
	ListColumns(ctx context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

type localWriter struct {
	data    *localdata.Data
	storage seriesStorage
	now     func() time.Time

	mu     sync.Mutex
	fields map[seriesstore.Source]cachedFields
}

type cachedFields struct {
	fields []seriesstore.Field
	at     time.Time
}

func newLocalWriter(data *localdata.Data, storage seriesStorage) *localWriter {
	return &localWriter{data: data, storage: storage, now: time.Now, fields: map[seriesstore.Source]cachedFields{}}
}

// writeError 是带返回码的失败。
type writeError struct {
	code commonpb.ErrorCode
	err  error
}

func (e *writeError) Error() string { return e.err.Error() }

func invalidWrite(format string, args ...any) error {
	return &writeError{code: commonpb.ErrorCode_INVALID_PARAM, err: fmt.Errorf(format, args...)}
}

func innerWrite(err error) error { return &writeError{code: commonpb.ErrorCode_INNER_ERR, err: err} }

// writeRetInfo 把本地写入的错误换成返回码：没有标明的按内部错误。
func writeRetInfo(err error) *commonpb.RetInfo {
	var failure *writeError
	if errors.As(err, &failure) {
		return &commonpb.RetInfo{Code: failure.code, Msg: failure.Error()}
	}
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: err.Error()}
}

// WriteSeries 用一批行整段替换标的在 [from, to) 内的数据。
func (w *localWriter) WriteSeries(ctx context.Context, req *factorpb.WriteSeriesReq) (*factorpb.WriteSeriesRsp, error) {
	rsp, err := w.write(ctx, req)
	if err != nil {
		return &factorpb.WriteSeriesRsp{RetInfo: writeRetInfo(err)}, nil
	}
	rsp.RetInfo = replaySuccess()
	return rsp, nil
}

func (w *localWriter) write(ctx context.Context, req *factorpb.WriteSeriesReq) (*factorpb.WriteSeriesRsp, error) {
	src := seriesstore.Source{SpaceID: strings.TrimSpace(req.GetSpaceId()), DatasetID: strings.TrimSpace(req.GetDatasetId()), Freq: strings.TrimSpace(req.GetFreq())}
	subject := strings.TrimSpace(req.GetSubjectId())
	if src.SpaceID == "" || src.DatasetID == "" || src.Freq == "" || subject == "" {
		return nil, invalidWrite("space_id、dataset_id、freq、subject_id 都是必填项")
	}
	// 本地写入接口是换算边界：请求里的区间与行时间是真实时间，本地存储是轴上时间。
	axis, err := periodclock.AxisFor(src.SpaceID, src.Freq)
	if err != nil {
		return nil, invalidWrite("%w", err)
	}
	from, err := time.Parse(time.RFC3339, req.GetFrom())
	if err == nil {
		from, err = axis.ToAxis(from)
	}
	if err != nil {
		return nil, invalidWrite("from：%w", err)
	}
	to, err := time.Parse(time.RFC3339, req.GetTo())
	if err == nil {
		to, err = axis.EndToAxis(to)
	}
	if err != nil {
		return nil, invalidWrite("to：%w", err)
	}
	fields, err := w.datasetFields(ctx, src)
	if err != nil {
		return nil, innerWrite(err)
	}
	rows, err := columnsFromWire(fields, req)
	if err != nil {
		return nil, invalidWrite("%w", err)
	}
	for i, at := range rows.Times {
		x, err := axis.ToAxis(time.Unix(at, 0))
		if err != nil {
			return nil, invalidWrite("第 %d 行的时间：%w", i, err)
		}
		rows.Times[i] = x.Unix()
	}
	opts := seriesstore.WriteOptions{FillGaps: req.GetAllowMissing(), RecordPending: req.GetRecordPending(), PendingReason: req.GetReason()}
	if opts.RecordPending && opts.PendingReason == "" {
		opts.PendingReason = "import"
	}
	scoped, err := w.data.UnifiedDataset(ctx, src)
	if err != nil {
		return nil, innerWrite(err)
	}
	results, err := w.data.WriteLocal(ctx, src, []seriesstore.SubjectWrite{{Subject: subject, From: from, To: to, Rows: rows, Scoped: scoped}}, opts)
	if err != nil {
		var gap *seriesstore.GapError
		if errors.As(err, &gap) || errors.Is(err, seriesstore.ErrInvalidWrite) || errors.Is(err, seriesstore.ErrSchema) {
			return nil, invalidWrite("%w", err)
		}
		return nil, innerWrite(err)
	}
	res := results[0]
	out := &factorpb.WriteSeriesRsp{Written: int32(res.Written), Filled: int32(res.Filled), Removed: int32(res.Removed)}
	if !res.Changed.Empty() {
		changedTo, err := axis.FromAxisEnd(res.Changed.To)
		if err != nil {
			return nil, innerWrite(err)
		}
		out.ChangedFrom, out.ChangedTo = axis.Format(res.Changed.From), changedTo.Format(time.RFC3339)
	}
	if !res.Earliest.IsZero() {
		out.Earliest, out.Latest = axis.Format(res.Earliest), axis.Format(res.Latest)
	}
	return out, nil
}

// datasetFields 返回数据集的本地字段表（含类型与填充规则），缓存 1 分钟。
func (w *localWriter) datasetFields(ctx context.Context, src seriesstore.Source) ([]seriesstore.Field, error) {
	w.mu.Lock()
	if c, ok := w.fields[src]; ok && w.now().Sub(c.at) < fieldsCacheTTL {
		w.mu.Unlock()
		return c.fields, nil
	}
	w.mu.Unlock()
	columns, err := w.storage.ListColumns(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return nil, fmt.Errorf("读取数据集 %s/%s 的字段：%w", src.SpaceID, src.DatasetID, err)
	}
	fields := seriessync.FieldsFromColumns(columns)
	if len(fields) == 0 {
		return nil, fmt.Errorf("数据集 %s/%s 没有可用的字段", src.SpaceID, src.DatasetID)
	}
	w.mu.Lock()
	w.fields[src] = cachedFields{fields: fields, at: w.now()}
	w.mu.Unlock()
	return fields, nil
}

// columnsFromWire 按数据集字段表把请求里的列转成列式行；必须带全部字段。
func columnsFromWire(fields []seriesstore.Field, req *factorpb.WriteSeriesReq) (*seriesstore.Columns, error) {
	times, tags, cols := req.GetTimes(), req.GetTags(), req.GetCols()
	n := len(times)
	if len(tags) != n {
		return nil, fmt.Errorf("tags 有 %d 项，times 有 %d 项", len(tags), n)
	}
	for name := range cols {
		known := false
		for _, f := range fields {
			known = known || f.Name == name
		}
		if !known {
			return nil, fmt.Errorf("列 %q 不是数据集字段", name)
		}
	}
	out := seriesstore.NewColumns(fields, n)
	for i := 0; i < n; i++ {
		out.Times = append(out.Times, times[i])
		out.Tags = append(out.Tags, tags[i])
		out.Filled = append(out.Filled, false)
	}
	for fi, f := range fields {
		col, ok := cols[f.Name]
		if !ok {
			return nil, fmt.Errorf("缺少数据集字段 %q（导入必须带全部字段）", f.Name)
		}
		if len(col.GetNull()) != n {
			return nil, fmt.Errorf("字段 %s 的 null 标记有 %d 项，期望 %d 项", f.Name, len(col.GetNull()), n)
		}
		dst := &out.Cols[fi]
		dst.Null = append(dst.Null, col.GetNull()...)
		switch f.Type {
		case seriesstore.TypeFloat64:
			if len(col.GetF()) != n {
				return nil, fmt.Errorf("字段 %s 的 f 有 %d 项，期望 %d 项", f.Name, len(col.GetF()), n)
			}
			dst.F = append(dst.F, col.GetF()...)
		case seriesstore.TypeInt64, seriesstore.TypeBool:
			if len(col.GetI()) != n {
				return nil, fmt.Errorf("字段 %s 的 i 有 %d 项，期望 %d 项", f.Name, len(col.GetI()), n)
			}
			dst.I = append(dst.I, col.GetI()...)
		case seriesstore.TypeString:
			if len(col.GetS()) != n {
				return nil, fmt.Errorf("字段 %s 的 s 有 %d 项，期望 %d 项", f.Name, len(col.GetS()), n)
			}
			dst.S = append(dst.S, col.GetS()...)
		}
	}
	return out, nil
}

// GetSeriesFields 返回数据集的字段表（名称、Storage 字段类型、填充规则）。
func (w *localWriter) GetSeriesFields(ctx context.Context, req *factorpb.GetSeriesFieldsReq) (*factorpb.GetSeriesFieldsRsp, error) {
	src := seriesstore.Source{SpaceID: strings.TrimSpace(req.GetSpaceId()), DatasetID: strings.TrimSpace(req.GetDatasetId()), Freq: strings.TrimSpace(req.GetFreq())}
	if src.SpaceID == "" || src.DatasetID == "" {
		return &factorpb.GetSeriesFieldsRsp{RetInfo: writeRetInfo(invalidWrite("space_id、dataset_id 是必填项"))}, nil
	}
	fields, err := w.datasetFields(ctx, src)
	if err != nil {
		return &factorpb.GetSeriesFieldsRsp{RetInfo: writeRetInfo(err)}, nil
	}
	columns, err := w.storage.ListColumns(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return &factorpb.GetSeriesFieldsRsp{RetInfo: writeRetInfo(err)}, nil
	}
	valueTypes := seriessync.ValueTypes(columns)
	fills := map[seriesstore.FillRule]string{seriesstore.FillPrev: "prev", seriesstore.FillPrevClose: "prev_close", seriesstore.FillZero: "zero"}
	out := &factorpb.GetSeriesFieldsRsp{RetInfo: replaySuccess()}
	for _, f := range fields {
		out.Fields = append(out.Fields, &factorpb.SeriesField{Name: f.Name, Type: valueTypes[f.Name], Fill: fills[f.Fill]})
	}
	return out, nil
}

// CheckSeriesSubject 判断标的是否在数据集的标签范围内。
func (w *localWriter) CheckSeriesSubject(ctx context.Context, req *factorpb.CheckSeriesSubjectReq) (*factorpb.CheckSeriesSubjectRsp, error) {
	space, dataset, subject := strings.TrimSpace(req.GetSpaceId()), strings.TrimSpace(req.GetDatasetId()), strings.TrimSpace(req.GetSubjectId())
	if space == "" || dataset == "" || subject == "" {
		return &factorpb.CheckSeriesSubjectRsp{RetInfo: writeRetInfo(invalidWrite("space_id、dataset_id、subject_id 都是必填项"))}, nil
	}
	subjects, err := w.storage.ListDatasetSubjects(ctx, space, dataset)
	if err != nil {
		return &factorpb.CheckSeriesSubjectRsp{RetInfo: writeRetInfo(err)}, nil
	}
	in := false
	for _, s := range subjects {
		in = in || s == subject
	}
	return &factorpb.CheckSeriesSubjectRsp{RetInfo: replaySuccess(), InScope: in}, nil
}

// GetSeriesBounds 返回标的在本地时序存储里的最早与最新时间。
func (w *localWriter) GetSeriesBounds(_ context.Context, req *factorpb.GetSeriesBoundsReq) (*factorpb.GetSeriesBoundsRsp, error) {
	src := seriesstore.Source{SpaceID: strings.TrimSpace(req.GetSpaceId()), DatasetID: strings.TrimSpace(req.GetDatasetId()), Freq: strings.TrimSpace(req.GetFreq())}
	subject := strings.TrimSpace(req.GetSubjectId())
	if src.SpaceID == "" || src.DatasetID == "" || src.Freq == "" || subject == "" {
		return &factorpb.GetSeriesBoundsRsp{RetInfo: writeRetInfo(invalidWrite("space_id、dataset_id、freq、subject_id 都是必填项"))}, nil
	}
	b, ok, err := w.data.Store().Bounds(src, subject)
	if err != nil {
		return &factorpb.GetSeriesBoundsRsp{RetInfo: writeRetInfo(invalidWrite("%w", err))}, nil
	}
	out := &factorpb.GetSeriesBoundsRsp{RetInfo: replaySuccess(), Exists: ok}
	if ok {
		out.Earliest, out.Latest = src.Format(b.Earliest), src.Format(b.Latest)
	}
	return out, nil
}
