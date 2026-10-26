package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// 本地写入接口（设计 7.1）：导入服务通过它把历史数据写进因子机的本地时序存储。本地存储同一时刻只能由引擎进程打开，
// 所以写入只能经过引擎。接口挂在健康检查端口上（只监听本机，沿用健康检查的签名鉴权），引擎处于未就绪状态时也可以写入。
//
//	POST /v1/series/write    用一批行整段替换标的在 [from, to) 内的数据
//	GET  /v1/series/fields   数据集的字段表（名称、类型），导入服务据此解析文件
//	GET  /v1/series/subject  标的是否在数据集的标签范围内（导入不自动绑定标的）
//	GET  /v1/series/bounds   标的在本地的最早与最新时间
//	GET  /v1/series/sources  已准备的数据源及其状态

const (
	maxWriteBody   = 64 << 20
	fieldsCacheTTL = time.Minute
)

// seriesStorage 是本地写入接口用到的 Storage 能力。
type seriesStorage interface {
	ListColumns(ctx context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

type localWriter struct {
	data    *localdata.Data
	storage seriesStorage
	sets    interface{ TaskIDs() []string }
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

// routes 返回要挂到健康检查端口上的路由。
func (w *localWriter) routes() []factorhealth.Route {
	return []factorhealth.Route{
		{Path: "/v1/series/write", Handler: http.HandlerFunc(w.handleWrite)},
		{Path: "/v1/series/fields", Handler: http.HandlerFunc(w.handleFields)},
		{Path: "/v1/series/subject", Handler: http.HandlerFunc(w.handleSubject)},
		{Path: "/v1/series/bounds", Handler: http.HandlerFunc(w.handleBounds)},
		{Path: "/v1/series/sources", Handler: http.HandlerFunc(w.handleSources)},
	}
}

// WriteRequest 是 POST /v1/series/write 的请求体。每列按字段名给出：null 标记与按类型的值数组。
type WriteRequest struct {
	Space   string `json:"space"`
	Dataset string `json:"dataset"`
	Freq    string `json:"freq"`
	Subject string `json:"subject"`
	// From、To 是整段替换的区间（RFC3339，左闭右开）。
	From string `json:"from"`
	To   string `json:"to"`
	// AllowMissing 允许中间缺的周期按字段填充规则写成缺失行；否则缺期时拒绝。
	AllowMissing bool `json:"allow_missing"`
	// RecordPending 把改变的区间记入失效重算待办（带 --recalc 的导入）。
	RecordPending bool   `json:"record_pending"`
	Reason        string `json:"reason"`
	// Times 是各行的 UTC 秒；Tags 是各行的 series_tag。
	Times []int64            `json:"times"`
	Tags  []string           `json:"tags"`
	Cols  map[string]WireCol `json:"cols"`
}

// WireCol 是一列的传输格式：F 用于 double，I 用于 int 与 bool，S 用于其余类型。
type WireCol struct {
	Null []bool    `json:"null,omitempty"`
	F    []float64 `json:"f,omitempty"`
	I    []int64   `json:"i,omitempty"`
	S    []string  `json:"s,omitempty"`
}

// WriteResponse 是写入结果。
type WriteResponse struct {
	Written     int    `json:"written"`
	Filled      int    `json:"filled"`
	Removed     int    `json:"removed"`
	ChangedFrom string `json:"changed_from,omitempty"`
	ChangedTo   string `json:"changed_to,omitempty"`
	Earliest    string `json:"earliest,omitempty"`
	Latest      string `json:"latest,omitempty"`
}

func (w *localWriter) handleWrite(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(rw, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var req WriteRequest
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxWriteBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Errorf("解析请求：%w", err))
		return
	}
	resp, status, err := w.write(r.Context(), req)
	if err != nil {
		writeJSONError(rw, status, err)
		return
	}
	writeJSON(rw, http.StatusOK, resp)
}

func (w *localWriter) write(ctx context.Context, req WriteRequest) (WriteResponse, int, error) {
	src := seriesstore.Source{SpaceID: strings.TrimSpace(req.Space), DatasetID: strings.TrimSpace(req.Dataset), Freq: strings.TrimSpace(req.Freq)}
	subject := strings.TrimSpace(req.Subject)
	if src.SpaceID == "" || src.DatasetID == "" || src.Freq == "" || subject == "" {
		return WriteResponse{}, http.StatusBadRequest, errors.New("space、dataset、freq、subject 都是必填项")
	}
	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		return WriteResponse{}, http.StatusBadRequest, fmt.Errorf("from：%w", err)
	}
	to, err := time.Parse(time.RFC3339, req.To)
	if err != nil {
		return WriteResponse{}, http.StatusBadRequest, fmt.Errorf("to：%w", err)
	}
	fields, err := w.datasetFields(ctx, src)
	if err != nil {
		return WriteResponse{}, http.StatusBadGateway, err
	}
	rows, err := columnsFromWire(fields, req)
	if err != nil {
		return WriteResponse{}, http.StatusBadRequest, err
	}
	opts := seriesstore.WriteOptions{FillGaps: req.AllowMissing, RecordPending: req.RecordPending, PendingReason: req.Reason}
	if opts.RecordPending && opts.PendingReason == "" {
		opts.PendingReason = "import"
	}
	results, err := w.data.WriteLocal(ctx, src, []seriesstore.SubjectWrite{{Subject: subject, From: from, To: to, Rows: rows}}, opts)
	if err != nil {
		var gap *seriesstore.GapError
		switch {
		case errors.As(err, &gap):
			return WriteResponse{}, http.StatusUnprocessableEntity, err
		case errors.Is(err, seriesstore.ErrInvalidWrite), errors.Is(err, seriesstore.ErrSchema):
			return WriteResponse{}, http.StatusBadRequest, err
		}
		return WriteResponse{}, http.StatusInternalServerError, err
	}
	res := results[0]
	out := WriteResponse{Written: res.Written, Filled: res.Filled, Removed: res.Removed}
	if !res.Changed.Empty() {
		out.ChangedFrom, out.ChangedTo = res.Changed.From.UTC().Format(time.RFC3339), res.Changed.To.UTC().Format(time.RFC3339)
	}
	if !res.Earliest.IsZero() {
		out.Earliest, out.Latest = res.Earliest.UTC().Format(time.RFC3339), res.Latest.UTC().Format(time.RFC3339)
	}
	return out, http.StatusOK, nil
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

// columnsFromWire 按数据集字段表把传输格式转成列式行；必须带全部字段。
func columnsFromWire(fields []seriesstore.Field, req WriteRequest) (*seriesstore.Columns, error) {
	n := len(req.Times)
	if len(req.Tags) != n {
		return nil, fmt.Errorf("tags 有 %d 项，times 有 %d 项", len(req.Tags), n)
	}
	for name := range req.Cols {
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
		out.Times = append(out.Times, req.Times[i])
		out.Tags = append(out.Tags, req.Tags[i])
		out.Filled = append(out.Filled, false)
	}
	for fi, f := range fields {
		col, ok := req.Cols[f.Name]
		if !ok {
			return nil, fmt.Errorf("缺少数据集字段 %q（导入必须带全部字段）", f.Name)
		}
		if len(col.Null) != n {
			return nil, fmt.Errorf("字段 %s 的 null 标记有 %d 项，期望 %d 项", f.Name, len(col.Null), n)
		}
		dst := &out.Cols[fi]
		dst.Null = append(dst.Null, col.Null...)
		switch f.Type {
		case seriesstore.TypeFloat64:
			if len(col.F) != n {
				return nil, fmt.Errorf("字段 %s 的 f 有 %d 项，期望 %d 项", f.Name, len(col.F), n)
			}
			dst.F = append(dst.F, col.F...)
		case seriesstore.TypeInt64, seriesstore.TypeBool:
			if len(col.I) != n {
				return nil, fmt.Errorf("字段 %s 的 i 有 %d 项，期望 %d 项", f.Name, len(col.I), n)
			}
			dst.I = append(dst.I, col.I...)
		case seriesstore.TypeString:
			if len(col.S) != n {
				return nil, fmt.Errorf("字段 %s 的 s 有 %d 项，期望 %d 项", f.Name, len(col.S), n)
			}
			dst.S = append(dst.S, col.S...)
		}
	}
	return out, nil
}

func (w *localWriter) handleFields(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := seriesstore.Source{SpaceID: q.Get("space"), DatasetID: q.Get("dataset"), Freq: q.Get("freq")}
	if src.SpaceID == "" || src.DatasetID == "" {
		writeJSONError(rw, http.StatusBadRequest, errors.New("space、dataset 是必填项"))
		return
	}
	fields, err := w.datasetFields(r.Context(), src)
	if err != nil {
		writeJSONError(rw, http.StatusBadGateway, err)
		return
	}
	type item struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Fill string `json:"fill"`
	}
	columns, err := w.storage.ListColumns(r.Context(), src.SpaceID, src.DatasetID)
	if err != nil {
		writeJSONError(rw, http.StatusBadGateway, err)
		return
	}
	valueTypes := seriessync.ValueTypes(columns)
	fills := map[seriesstore.FillRule]string{seriesstore.FillPrev: "prev", seriesstore.FillPrevClose: "prev_close", seriesstore.FillZero: "zero"}
	out := make([]item, 0, len(fields))
	for _, f := range fields {
		out = append(out, item{Name: f.Name, Type: valueTypes[f.Name], Fill: fills[f.Fill]})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"fields": out})
}

func (w *localWriter) handleSubject(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	space, dataset, subject := q.Get("space"), q.Get("dataset"), q.Get("subject")
	if space == "" || dataset == "" || subject == "" {
		writeJSONError(rw, http.StatusBadRequest, errors.New("space、dataset、subject 都是必填项"))
		return
	}
	subjects, err := w.storage.ListDatasetSubjects(r.Context(), space, dataset)
	if err != nil {
		writeJSONError(rw, http.StatusBadGateway, err)
		return
	}
	in := false
	for _, s := range subjects {
		if s == subject {
			in = true
			break
		}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"in_scope": in})
}

func (w *localWriter) handleBounds(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := seriesstore.Source{SpaceID: q.Get("space"), DatasetID: q.Get("dataset"), Freq: q.Get("freq")}
	subject := q.Get("subject")
	if src.SpaceID == "" || src.DatasetID == "" || src.Freq == "" || subject == "" {
		writeJSONError(rw, http.StatusBadRequest, errors.New("space、dataset、freq、subject 都是必填项"))
		return
	}
	b, ok, err := w.data.Store().Bounds(src, subject)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err)
		return
	}
	out := map[string]any{"exists": ok}
	if ok {
		out["earliest"], out["latest"] = b.Earliest.UTC().Format(time.RFC3339), b.Latest.UTC().Format(time.RFC3339)
	}
	writeJSON(rw, http.StatusOK, out)
}

func (w *localWriter) handleSources(rw http.ResponseWriter, _ *http.Request) {
	type item struct {
		Source  string `json:"source"`
		TaskID  string `json:"task_id"`
		Blocked string `json:"blocked,omitempty"`
	}
	var out []item
	for _, info := range w.data.Sources() {
		it := item{Source: info.Source.String(), TaskID: info.TaskID}
		if err := w.data.Blocked(info.Source); err != nil {
			it.Blocked = err.Error()
		}
		out = append(out, it)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"sources": out})
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func writeJSONError(rw http.ResponseWriter, status int, err error) {
	writeJSON(rw, status, map[string]string{"error": err.Error()})
}
