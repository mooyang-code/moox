package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// writeStorage 只提供数据集字段；本地写入接口不会读 Storage 的行。
type writeStorage struct{ localdata.Storage }

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

func (contractStorage) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"BTCUSDT"}, nil
}

func newTestWriter(t *testing.T) (*localWriter, *seriesstore.Store) {
	t.Helper()
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	data := localdata.New(localdata.Config{Store: store, Storage: writeStorage{}, Clock: periodclock.Continuous{}})
	return newLocalWriter(data, writeStorage{}), store
}

func wireRequest(minutes ...int) WriteRequest {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	req := WriteRequest{Space: "crypto", Dataset: "spot", Freq: "1m", Subject: "BTC", Cols: map[string]WireCol{"close": {}, "trades": {}, "note": {}}}
	req.From = t0.Add(time.Duration(minutes[0]) * time.Minute).Format(time.RFC3339)
	req.To = t0.Add(time.Duration(minutes[len(minutes)-1]+1) * time.Minute).Format(time.RFC3339)
	for _, m := range minutes {
		req.Times = append(req.Times, t0.Add(time.Duration(m)*time.Minute).Unix())
		req.Tags = append(req.Tags, "")
		c, tr, n := req.Cols["close"], req.Cols["trades"], req.Cols["note"]
		c.Null, c.F = append(c.Null, false), append(c.F, float64(100+m))
		tr.Null, tr.I = append(tr.Null, false), append(tr.I, int64(m))
		n.Null, n.S = append(n.Null, false), append(n.S, "x")
		req.Cols["close"], req.Cols["trades"], req.Cols["note"] = c, tr, n
	}
	return req
}

func post(t *testing.T, w *localWriter, req WriteRequest) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	w.handleWrite(rec, httptest.NewRequest(http.MethodPost, "/v1/series/write", bytes.NewReader(body)))
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return rec.Code, out
}

func TestLocalWriteStoresRowsAndReportsBounds(t *testing.T) {
	w, store := newTestWriter(t)
	code, out := post(t, w, wireRequest(0, 1, 2))
	require.Equal(t, http.StatusOK, code, out)
	require.EqualValues(t, 3, out["written"])
	require.Equal(t, "2026-10-01T00:00:00Z", out["earliest"])
	require.Equal(t, "2026-10-01T00:02:00Z", out["latest"])

	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	got, err := store.ReadRange(context.Background(), src, []string{"BTC"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 3, got["BTC"].Len())

	// 紧接着的下一批：连续写入，不是缺期。
	code, out = post(t, w, wireRequest(3, 4))
	require.Equal(t, http.StatusOK, code, out)

	rec := httptest.NewRecorder()
	w.handleBounds(rec, httptest.NewRequest(http.MethodGet, "/v1/series/bounds?space=crypto&dataset=spot&freq=1m&subject=BTC", nil))
	var bounds map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &bounds))
	require.Equal(t, true, bounds["exists"])
	require.Equal(t, "2026-10-01T00:04:00Z", bounds["latest"])

	rec = httptest.NewRecorder()
	w.handleBounds(rec, httptest.NewRequest(http.MethodGet, "/v1/series/bounds?space=crypto&dataset=spot&freq=1m&subject=ETH", nil))
	require.Contains(t, rec.Body.String(), `"exists":false`)
}

func TestLocalWriteRejectsGapsUnlessAllowedAndRecordsPending(t *testing.T) {
	w, store := newTestWriter(t)
	code, _ := post(t, w, wireRequest(0, 1))
	require.Equal(t, http.StatusOK, code)

	// 与已有数据之间缺 t2：拒绝并说明缺的周期。
	code, out := post(t, w, wireRequest(3, 4))
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Contains(t, out["error"], "缺少周期")

	req := wireRequest(3, 4)
	req.AllowMissing = true
	code, out = post(t, w, req)
	require.Equal(t, http.StatusOK, code, out)
	require.EqualValues(t, 1, out["filled"])

	// 带 record_pending 重新导入已有区间：失效区间记入待办。
	redo := wireRequest(0, 1)
	redo.RecordPending, redo.Reason = true, "import"
	code, out = post(t, w, redo)
	require.Equal(t, http.StatusOK, code, out)
	pending, err := store.Pending(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "import", pending[0].Reason)
}

func TestLocalWriteValidatesRequest(t *testing.T) {
	w, _ := newTestWriter(t)
	missing := wireRequest(0, 1)
	delete(missing.Cols, "note")
	code, out := post(t, w, missing)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, out["error"], "缺少数据集字段")

	unknown := wireRequest(0, 1)
	unknown.Cols["extra"] = WireCol{}
	code, out = post(t, w, unknown)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, out["error"], "不是数据集字段")

	short := wireRequest(0, 1)
	c := short.Cols["close"]
	c.F = c.F[:1]
	short.Cols["close"] = c
	code, _ = post(t, w, short)
	require.Equal(t, http.StatusBadRequest, code)

	bad := wireRequest(0, 1)
	bad.From = "yesterday"
	code, _ = post(t, w, bad)
	require.Equal(t, http.StatusBadRequest, code)

	empty := WriteRequest{}
	code, _ = post(t, w, empty)
	require.Equal(t, http.StatusBadRequest, code)

	rec := httptest.NewRecorder()
	w.handleWrite(rec, httptest.NewRequest(http.MethodGet, "/v1/series/write", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	// 未知字段的请求体被拒绝。
	rec = httptest.NewRecorder()
	w.handleWrite(rec, httptest.NewRequest(http.MethodPost, "/v1/series/write", bytes.NewReader([]byte(`{"bogus":1}`))))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	// 区间没有按频率对齐：参数错误。
	misaligned := wireRequest(0, 1)
	misaligned.From = "2026-10-01T00:00:30Z"
	code, _ = post(t, w, misaligned)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestSourcesEndpointListsPreparedSources(t *testing.T) {
	w, _ := newTestWriter(t)
	rec := httptest.NewRecorder()
	w.handleSources(rec, httptest.NewRequest(http.MethodGet, "/v1/series/sources", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"sources":null}`, rec.Body.String())
}

// contractStorage 的字段与导入服务契约样例（modules/importer/target/testdata/write_request.json）一致。
type contractStorage struct{ localdata.Storage }

func (contractStorage) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return []storageio.ColumnInfo{
		{ColumnName: "close", ValueType: storageio.ColumnTypeDouble},
		{ColumnName: "trades", ValueType: storageio.ColumnTypeInt},
		{ColumnName: "closed", ValueType: storageio.ColumnTypeBool},
		{ColumnName: "note", ValueType: storageio.ColumnTypeString},
	}, nil
}

// 导入服务发来的请求体（同一份契约样例）能被本地写入接口原样解码并写入，格式变了两边的测试一起失败。
func TestLocalWriteAcceptsImporterContractSample(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/seriesimport/target/testdata/write_request.json")
	require.NoError(t, err)
	var req WriteRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&req))

	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	defer store.Close()
	data := localdata.New(localdata.Config{Store: store, Storage: contractStorage{}, Clock: periodclock.Continuous{}})
	w := newLocalWriter(data, contractStorage{})
	resp, status, err := w.write(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 2, resp.Written)

	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	got, err := store.ReadRange(context.Background(), src, []string{"BTCUSDT"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	cols := got["BTCUSDT"]
	require.Equal(t, 2, cols.Len())
	closeIdx := cols.FieldIndex("close")
	require.Equal(t, 100.5, cols.ValueAt(closeIdx, 0).F)
	require.True(t, cols.ValueAt(closeIdx, 1).Null)
	require.Equal(t, int64(1), cols.ValueAt(cols.FieldIndex("closed"), 0).I)
}

func TestFieldsAndSubjectEndpoints(t *testing.T) {
	w, _ := newTestWriter(t)
	rec := httptest.NewRecorder()
	w.handleFields(rec, httptest.NewRequest(http.MethodGet, "/v1/series/fields?space=crypto&dataset=spot&freq=1m", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"fields":[{"name":"close","type":"double","fill":"prev_close"},{"name":"note","type":"string","fill":"prev"},{"name":"trades","type":"int","fill":"zero"}]}`, rec.Body.String())

	rec = httptest.NewRecorder()
	w.handleFields(rec, httptest.NewRequest(http.MethodGet, "/v1/series/fields", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	w.handleSubject(rec, httptest.NewRequest(http.MethodGet, "/v1/series/subject?space=crypto&dataset=spot&subject=BTC", nil))
	require.JSONEq(t, `{"in_scope":true}`, rec.Body.String())
	rec = httptest.NewRecorder()
	w.handleSubject(rec, httptest.NewRequest(http.MethodGet, "/v1/series/subject?space=crypto&dataset=spot&subject=DOGE", nil))
	require.JSONEq(t, `{"in_scope":false}`, rec.Body.String())
	rec = httptest.NewRecorder()
	w.handleSubject(rec, httptest.NewRequest(http.MethodGet, "/v1/series/subject?space=crypto&dataset=spot", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
