package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// writeStorage 只提供数据集字段；本地写入接口不会读 Storage 的行。
type writeStorage struct {
	localdata.Storage
}

func (writeStorage) GetDataset(context.Context, string, string) (storageio.DatasetInfo, error) {
	return storageio.DatasetInfo{Attributes: map[string]string{"market_type": "spot"}}, nil
}

func (writeStorage) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return []storageio.ColumnInfo{
		{ColumnName: "close", ValueType: storageio.ColumnTypeDouble},
		{ColumnName: "trades", ValueType: storageio.ColumnTypeInt},
		{ColumnName: "note", ValueType: storageio.ColumnTypeString},
	}, nil
}

func (writeStorage) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"BTC", "ETH"}, nil
}

func (contractStorage) GetDataset(context.Context, string, string) (storageio.DatasetInfo, error) {
	return storageio.DatasetInfo{Attributes: map[string]string{"market_type": "spot"}}, nil
}

func (contractStorage) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"BTCUSDT"}, nil
}

// newTestWriter 造本地写入器。
func newTestWriter(t *testing.T) (*localWriter, *seriesstore.Store) {
	t.Helper()
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	data := localdata.New(localdata.Config{Store: store, Storage: writeStorage{}})
	return newLocalWriter(data, writeStorage{}), store
}

func wireRequest(minutes ...int) *factorpb.WriteSeriesReq {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	req := &factorpb.WriteSeriesReq{SpaceId: "crypto", DatasetId: "spot", Freq: "1m", SubjectId: "BTC",
		Cols: map[string]*factorpb.SeriesColumn{"close": {}, "trades": {}, "note": {}}}
	req.From = t0.Add(time.Duration(minutes[0]) * time.Minute).Format(time.RFC3339)
	req.To = t0.Add(time.Duration(minutes[len(minutes)-1]+1) * time.Minute).Format(time.RFC3339)
	for _, m := range minutes {
		req.Times = append(req.Times, t0.Add(time.Duration(m)*time.Minute).Unix())
		req.Tags = append(req.Tags, "")
		c, tr, n := req.Cols["close"], req.Cols["trades"], req.Cols["note"]
		c.Null, c.F = append(c.Null, false), append(c.F, float64(100+m))
		tr.Null, tr.I = append(tr.Null, false), append(tr.I, int64(m))
		n.Null, n.S = append(n.Null, false), append(n.S, "x")
	}
	return req
}

// control 把本地写入器装进 EngineControl 的实现，测试经服务接口调用（与网关转发到的是同一组方法）。
func control(w *localWriter) factorpb.EngineControlService { return &engineControl{localWriter: w} }

// roundTrip 让请求过一遍 protobuf 编码，和经网关到达引擎时一样。
func roundTrip[T proto.Message](t *testing.T, in T, out T) T {
	t.Helper()
	raw, err := proto.Marshal(in)
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, out))
	return out
}

func post(t *testing.T, w *localWriter, req *factorpb.WriteSeriesReq) *factorpb.WriteSeriesRsp {
	t.Helper()
	rsp, err := control(w).WriteSeries(context.Background(), roundTrip(t, req, &factorpb.WriteSeriesReq{}))
	require.NoError(t, err)
	return rsp
}

func TestLocalWriteStoresRowsAndReportsBounds(t *testing.T) {
	w, store := newTestWriter(t)
	out := post(t, w, wireRequest(0, 1, 2))
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	require.EqualValues(t, 3, out.GetWritten())
	require.Equal(t, "2026-10-01T00:00:00Z", out.GetEarliest())
	require.Equal(t, "2026-10-01T00:02:00Z", out.GetLatest())

	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	got, err := store.ReadRange(context.Background(), src, []string{"BTC"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 3, got["BTC"].Len())

	// 紧接着的下一批：连续写入，不是缺期。
	out = post(t, w, wireRequest(3, 4))
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())

	bounds, err := control(w).GetSeriesBounds(context.Background(), &factorpb.GetSeriesBoundsReq{SpaceId: "crypto", DatasetId: "spot", Freq: "1m", SubjectId: "BTC"})
	require.NoError(t, err)
	require.True(t, bounds.GetExists())
	require.Equal(t, "2026-10-01T00:04:00Z", bounds.GetLatest())

	bounds, err = control(w).GetSeriesBounds(context.Background(), &factorpb.GetSeriesBoundsReq{SpaceId: "crypto", DatasetId: "spot", Freq: "1m", SubjectId: "ETH"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, bounds.GetRetInfo().GetCode())
	require.False(t, bounds.GetExists())
}

func TestLocalWriteRejectsGapsUnlessAllowedAndRecordsPending(t *testing.T) {
	w, store := newTestWriter(t)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, post(t, w, wireRequest(0, 1)).GetRetInfo().GetCode())

	// 与已有数据之间缺 t2：拒绝并说明缺的周期。
	out := post(t, w, wireRequest(3, 4))
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, out.GetRetInfo().GetCode())
	require.Contains(t, out.GetRetInfo().GetMsg(), "缺少周期")

	req := wireRequest(3, 4)
	req.AllowMissing = true
	out = post(t, w, req)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	require.EqualValues(t, 1, out.GetFilled())

	// 带 record_pending 重新导入已有区间：失效区间记入待办。
	redo := wireRequest(0, 1)
	redo.RecordPending, redo.Reason = true, "import"
	out = post(t, w, redo)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	pending, err := store.Pending(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "import", pending[0].Reason)
}

func TestLocalWriteValidatesRequest(t *testing.T) {
	w, _ := newTestWriter(t)
	invalid := func(req *factorpb.WriteSeriesReq, contains string) {
		t.Helper()
		out := post(t, w, req)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
		require.Contains(t, out.GetRetInfo().GetMsg(), contains)
	}
	missing := wireRequest(0, 1)
	delete(missing.Cols, "note")
	invalid(missing, "缺少数据集字段")

	unknown := wireRequest(0, 1)
	unknown.Cols["extra"] = &factorpb.SeriesColumn{}
	invalid(unknown, "不是数据集字段")

	short := wireRequest(0, 1)
	short.Cols["close"].F = short.Cols["close"].F[:1]
	invalid(short, "字段 close 的 f")

	bad := wireRequest(0, 1)
	bad.From = "yesterday"
	invalid(bad, "from")

	invalid(&factorpb.WriteSeriesReq{}, "必填")

	// 区间没有按频率对齐：参数错误。
	misaligned := wireRequest(0, 1)
	misaligned.From = "2026-10-01T00:00:30Z"
	invalid(misaligned, "from")
}

// contractStorage 的字段覆盖四种列类型：double、int、bool、string。
type contractStorage struct{ localdata.Storage }

func (contractStorage) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return []storageio.ColumnInfo{
		{ColumnName: "close", ValueType: storageio.ColumnTypeDouble},
		{ColumnName: "trades", ValueType: storageio.ColumnTypeInt},
		{ColumnName: "closed", ValueType: storageio.ColumnTypeBool},
		{ColumnName: "note", ValueType: storageio.ColumnTypeString},
	}, nil
}

// 四种列类型与空值经 protobuf 编码后原样写进本地存储（bool 按 i 传）。
func TestLocalWriteStoresEveryColumnType(t *testing.T) {
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	defer store.Close()
	data := localdata.New(localdata.Config{Store: store, Storage: contractStorage{}})
	w := newLocalWriter(data, contractStorage{})
	out := post(t, w, &factorpb.WriteSeriesReq{
		SpaceId: "crypto", DatasetId: "spot", Freq: "1m", SubjectId: "BTCUSDT", From: "2026-10-01T00:00:00Z", To: "2026-10-01T00:02:00Z",
		AllowMissing: true, RecordPending: true, Reason: "import", Times: []int64{1790812800, 1790812860}, Tags: []string{"", ""},
		Cols: map[string]*factorpb.SeriesColumn{
			"close":  {Null: []bool{false, true}, F: []float64{100.5, 0}},
			"trades": {Null: []bool{false, false}, I: []int64{7, 8}},
			"closed": {Null: []bool{false, false}, I: []int64{1, 0}},
			"note":   {Null: []bool{false, false}, S: []string{"a", "b"}},
		},
	})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	require.EqualValues(t, 2, out.GetWritten())

	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	got, err := store.ReadRange(context.Background(), src, []string{"BTCUSDT"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	cols := got["BTCUSDT"]
	require.Equal(t, 2, cols.Len())
	closeIdx := cols.FieldIndex("close")
	require.Equal(t, 100.5, cols.ValueAt(closeIdx, 0).F)
	require.True(t, cols.ValueAt(closeIdx, 1).Null)
	require.Equal(t, int64(1), cols.ValueAt(cols.FieldIndex("closed"), 0).I)
	require.Equal(t, "b", cols.ValueAt(cols.FieldIndex("note"), 1).S)
}

func TestSeriesFieldsAndSubjectMethods(t *testing.T) {
	w, _ := newTestWriter(t)
	ctx := context.Background()
	fields, err := control(w).GetSeriesFields(ctx, &factorpb.GetSeriesFieldsReq{SpaceId: "crypto", DatasetId: "spot", Freq: "1m"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, fields.GetRetInfo().GetCode())
	var got []string
	for _, f := range fields.GetFields() {
		got = append(got, f.GetName()+":"+f.GetType()+":"+f.GetFill())
	}
	require.Equal(t, []string{"close:double:prev_close", "note:string:prev", "trades:int:zero"}, got)

	fields, _ = control(w).GetSeriesFields(ctx, &factorpb.GetSeriesFieldsReq{})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, fields.GetRetInfo().GetCode())

	subject, _ := control(w).CheckSeriesSubject(ctx, &factorpb.CheckSeriesSubjectReq{SpaceId: "crypto", DatasetId: "spot", SubjectId: "BTC"})
	require.True(t, subject.GetInScope())
	subject, _ = control(w).CheckSeriesSubject(ctx, &factorpb.CheckSeriesSubjectReq{SpaceId: "crypto", DatasetId: "spot", SubjectId: "DOGE"})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, subject.GetRetInfo().GetCode())
	require.False(t, subject.GetInScope())
	subject, _ = control(w).CheckSeriesSubject(ctx, &factorpb.CheckSeriesSubjectReq{SpaceId: "crypto", DatasetId: "spot"})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, subject.GetRetInfo().GetCode())
}
