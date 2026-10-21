package target

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/seriesformat"
	"github.com/mooyang-code/moox/packages/seriesimport/runner"
	"github.com/stretchr/testify/require"
)

func sampleChunk() runner.Chunk {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fields := []seriesformat.Field{
		{Name: "close", Type: seriesformat.TypeDouble}, {Name: "trades", Type: seriesformat.TypeInt},
		{Name: "closed", Type: seriesformat.TypeBool}, {Name: "note", Type: seriesformat.TypeString},
	}
	b := seriesformat.NewBatch(fields, 2)
	b.Times = []time.Time{t0, t0.Add(time.Minute)}
	b.Tags = []string{"", ""}
	b.Cols = []seriesformat.Column{
		{Null: []bool{false, true}, F: []float64{100.5, 0}},
		{Null: []bool{false, false}, I: []int64{7, 8}},
		{Null: []bool{false, false}, I: []int64{1, 0}},
		{Null: []bool{false, false}, S: []string{"a", "b"}},
	}
	return runner.Chunk{Space: "crypto", Dataset: "spot", Freq: "1m", Subject: "BTCUSDT", From: t0, To: t0.Add(2 * time.Minute), Fields: fields, Batch: b, AllowMissing: true, RecordPending: true}
}

// 请求体的格式是与引擎本地写入接口的契约：引擎侧的测试用同一份文件解码（DisallowUnknownFields）。
func TestWriteRequestMatchesGoldenContract(t *testing.T) {
	got, err := json.MarshalIndent(wireRequest(sampleChunk()), "", "  ")
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/write_request.json")
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}

func TestFactorTargetPostsSignedRequest(t *testing.T) {
	var seen *http.Request
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"written":2}`))
	}))
	defer srv.Close()

	tgt := &Factor{Endpoint: srv.URL + "/", AccessKey: "ak", SecretKey: "secret-secret-secret"}
	require.Equal(t, "factor", tgt.Name())
	require.NoError(t, tgt.Write(context.Background(), sampleChunk()))
	require.Equal(t, http.MethodPost, seen.Method)
	require.Equal(t, "/v1/series/write", seen.URL.Path)
	require.Equal(t, "BTCUSDT", body["subject"])

	// 签名头可以被健康检查的鉴权验证：版本/密钥ID/时间戳/随机数/签名。
	parts := strings.Split(seen.Header.Get("X-Moox-Health-Auth"), "/")
	require.Len(t, parts, 5)
	require.Equal(t, "moox-health-v1", parts[0])
	require.Equal(t, "ak", parts[1])
	var ts int64
	require.NoError(t, json.Unmarshal([]byte(parts[2]), &ts))
	require.NoError(t, requestauth.Verify("secret-secret-secret", requestauth.Material{Method: http.MethodPost, Path: "/v1/series/write", Timestamp: ts, Nonce: parts[3]}, parts[4]))
}

func TestFactorTargetReportsEngineErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"标的 BTC 的数据不连续，缺少周期 [2026-10-01T00:02:00Z]"}`))
	}))
	defer srv.Close()
	tgt := &Factor{Endpoint: srv.URL, AccessKey: "ak", SecretKey: "secret-secret-secret"}
	err := tgt.Write(context.Background(), sampleChunk())
	require.ErrorContains(t, err, "HTTP 422")
	require.ErrorContains(t, err, "数据不连续")

	require.ErrorContains(t, (&Factor{Endpoint: srv.URL}).Write(context.Background(), sampleChunk()), "缺少因子引擎本地写入接口的鉴权密钥")
}
