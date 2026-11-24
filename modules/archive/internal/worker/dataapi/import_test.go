package dataapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// fakeSink 按 run_id 返回预设的错误；"ok" 运行接受写入。
type fakeSink struct {
	errs     map[string]error
	got      *storagepb.PrimaryUpsertFieldsReq
	finished []string
}

func (f *fakeSink) Write(_ context.Context, runID string, req *storagepb.PrimaryUpsertFieldsReq) (ImportResult, error) {
	if err := f.errs[runID]; err != nil {
		return ImportResult{}, err
	}
	f.got = req
	return ImportResult{Added: uint64(len(req.GetRows())), Same: 1}, nil
}

func (f *fakeSink) Finish(runID string) error {
	if err := f.errs[runID]; err != nil {
		return err
	}
	f.finished = append(f.finished, runID)
	return nil
}

func post(t *testing.T, ts *httptest.Server, path string, body []byte) (int, []byte) {
	t.Helper()
	resp, err := http.Post(ts.URL+path, "application/x-protobuf", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestImportHandlersMapSinkErrorsToStatusCodes(t *testing.T) {
	sink := &fakeSink{errs: map[string]error{
		"missing": fmt.Errorf("%w：missing", ErrImportNotFound),
		"full":    ErrDiskInsufficient,
		"bad":     fmt.Errorf("%w：列 x 不在任务的列里", ErrInvalidBatch),
		"broken":  fmt.Errorf("磁盘写失败"),
	}}
	ts := httptest.NewServer(New(Options{Imports: sink}))
	t.Cleanup(ts.Close)
	req := &storagepb.PrimaryUpsertFieldsReq{SpaceId: space, DatasetId: dataset, Rows: []*storagepb.RowFieldUpsert{{}, {}}}
	body, err := proto.Marshal(req)
	require.NoError(t, err)

	status, raw := post(t, ts, "/v1/import/rows?run=ok", body)
	require.Equal(t, http.StatusOK, status, string(raw))
	var result ImportResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Equal(t, ImportResult{Added: 2, Same: 1}, result)
	require.Equal(t, dataset, sink.got.GetDatasetId())

	for run, want := range map[string]int{"missing": http.StatusNotFound, "full": http.StatusInsufficientStorage, "bad": http.StatusBadRequest, "broken": http.StatusInternalServerError} {
		status, _ := post(t, ts, "/v1/import/rows?run="+run, body)
		require.Equal(t, want, status, run)
	}
	status, _ = post(t, ts, "/v1/import/rows", body)
	require.Equal(t, http.StatusBadRequest, status, "缺 run")
	status, _ = post(t, ts, "/v1/import/rows?run=ok", []byte("not protobuf \xff\xff"))
	require.Equal(t, http.StatusBadRequest, status, "请求体不是 protobuf")
	status, _ = post(t, ts, "/v1/import/rows?run=ok", make([]byte, MaxImportBodyBytes+1))
	require.Equal(t, http.StatusRequestEntityTooLarge, status, "请求体超过上限")

	status, _ = post(t, ts, "/v1/import/finish?run=ok", nil)
	require.Equal(t, http.StatusAccepted, status)
	require.Equal(t, []string{"ok"}, sink.finished)
	status, _ = post(t, ts, "/v1/import/finish?run=missing", nil)
	require.Equal(t, http.StatusNotFound, status)

	// 只读的 GET 不能用来写，没有配 Imports 时写接口一律 404。
	resp, err := http.Get(ts.URL + "/v1/import/rows?run=ok")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	bare := httptest.NewServer(New(Options{}))
	t.Cleanup(bare.Close)
	status, _ = post(t, bare, "/v1/import/rows?run=ok", body)
	require.Equal(t, http.StatusNotFound, status)
}
