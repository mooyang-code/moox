package dataapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/objectstore/fakes3"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	"github.com/stretchr/testify/require"
)

const (
	space   = "crypto"
	dataset = "dataset_spot_1m"
	bucketN = "moox-archive-1250000000"
)

var day0 = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

type env struct {
	t      *testing.T
	store  *staging.Store
	files  export.Files
	s3     *fakes3.Server
	remote *objectstore.Client
	task   staging.Task
	opened []*staging.Store
	mu     sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	store, err := staging.Open(filepath.Join(root, ".state", "staging.db"), staging.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	s3 := fakes3.New(t, bucketN, false)
	cfg := objectstore.Config{Endpoint: "cos.ap-nanjing.myqcloud.com", Region: "ap-nanjing", Bucket: bucketN, URLStyle: objectstore.URLStylePath, UseTLS: new(bool), CredentialsFile: "x"}
	require.NoError(t, cfg.Validate())
	remote, err := objectstore.New(cfg, objectstore.Credentials{AccessKeyID: "AKIDtestaccesskey0001", SecretAccessKey: "secret"},
		objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return s3.Transport() }))
	require.NoError(t, err)
	task := staging.Task{SpaceID: space, DatasetID: dataset, Purpose: "collect", DirName: "binance-spot-kline-1m",
		Columns: []staging.Column{{Name: "close", Type: staging.TypeDouble}, {Name: "closed", Type: staging.TypeBool}}}
	require.NoError(t, store.BindTask(t.Context(), task, "task-1"))
	require.NoError(t, store.EnsureTable(t.Context(), task))
	return &env{t: t, store: store, files: export.Files{Root: root, Format: export.FormatCSVGz}, s3: s3, remote: remote, task: task}
}

// server 建数据端口；记下每次只读打开的库，供断言“用后关闭”。
func (e *env) server(rate int64, sleepFn func(context.Context, time.Duration) error, now func() time.Time) *httptest.Server {
	srv := New(Options{StatePath: e.store.Path(), Files: e.files, Remote: e.remote, BytesPerSecond: rate, Sleep: sleepFn, Now: now,
		OpenStore: func(path string) (*staging.Store, error) {
			s, err := staging.OpenReadOnly(path)
			if err == nil {
				e.mu.Lock()
				e.opened = append(e.opened, s)
				e.mu.Unlock()
			}
			return s, err
		}})
	ts := httptest.NewServer(srv)
	e.t.Cleanup(ts.Close)
	return ts
}

func (e *env) assertStoresClosed() {
	e.mu.Lock()
	defer e.mu.Unlock()
	require.NotEmpty(e.t, e.opened)
	for _, s := range e.opened {
		_, _, err := s.Partition(context.Background(), space, dataset, "x")
		require.Error(e.t, err, "只读连接没有关闭")
	}
}

// partition 暂存一行并把分区登记为已导出；replicated 为真时再登记远端副本已上传并校验。返回分区键。
func (e *env) partition(content []byte, replicated bool) string {
	ctx := e.t.Context()
	_, err := e.store.Stage(ctx, e.task, []staging.Row{{SubjectID: "BTCUSDT", DataTime: day0, Values: []any{1.0, true}}},
		func(at time.Time) staging.PartitionRef {
			return staging.PartitionRef{Key: "2026-10-15", Start: day0, End: day0.AddDate(0, 0, 1)}
		}, day0.Add(time.Hour))
	require.NoError(e.t, err)
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	rel, err := e.files.RelPath(e.task, "2026-10-15")
	require.NoError(e.t, err)
	require.NoError(e.t, e.store.MarkExported(ctx, space, dataset, "2026-10-15", day0.Add(2*time.Hour), rel, sha))
	if replicated {
		key, err := objectstore.VersionedKey(rel, sha)
		require.NoError(e.t, err)
		ok, err := e.store.MarkUploaded(ctx, space, dataset, "2026-10-15", sha, staging.Remote{Key: key, SHA256: sha, ETag: "e", UploadedAt: day0.Add(3 * time.Hour), VerifiedAt: day0.Add(3 * time.Hour)})
		require.NoError(e.t, err)
		require.True(e.t, ok)
		e.s3.Put(objectstore.DefaultPrefix+key, content)
	}
	return "2026-10-15"
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func get(t *testing.T, ts *httptest.Server, path string, query url.Values) (*http.Response, []byte, error) {
	t.Helper()
	resp, err := http.Get(ts.URL + path + "?" + query.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

func partitionQuery(key string) url.Values {
	return url.Values{"space": {space}, "dataset": {dataset}, "partition": {key}}
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestDownloadStreamsVerifiedRemoteCopyWithHeaders(t *testing.T) {
	e := newEnv(t)
	content := randomBytes(t, 200<<10)
	key := e.partition(content, true)
	ts := e.server(2<<20, noSleep, time.Now)
	resp, body, err := get(t, ts, "/v1/partition", partitionQuery(key))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, bytes.Equal(body, content))
	sum := sha256.Sum256(content)
	require.Equal(t, "application/gzip", resp.Header.Get("Content-Type"))
	require.Equal(t, "204800", resp.Header.Get("Content-Length"))
	require.Equal(t, `attachment; filename="binance-spot-kline-1m_2026-10-15.csv.gz"`, resp.Header.Get("Content-Disposition"))
	require.Equal(t, hex.EncodeToString(sum[:]), resp.Header.Get(SHA256Header))
	require.Empty(t, resp.Header.Get("Content-Encoding"))
	e.assertStoresClosed()
}

func TestDownloadRejectsUnknownUnreplicatedAndBadParams(t *testing.T) {
	e := newEnv(t)
	ts := e.server(2<<20, noSleep, time.Now)
	resp, body, _ := get(t, ts, "/v1/partition", partitionQuery("2026-10-15"))
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, string(body), "该分区不在当前执行器的登记里")

	key := e.partition([]byte("local only"), false)
	resp, body, _ = get(t, ts, "/v1/partition", partitionQuery(key))
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, string(body), "远端副本未就绪")

	for _, query := range []url.Values{
		{"space": {"../x"}, "dataset": {dataset}, "partition": {key}},
		{"space": {space}, "dataset": {""}, "partition": {key}},
		{"space": {space}, "dataset": {dataset}, "partition": {".."}},
	} {
		resp, _, _ = get(t, ts, "/v1/partition", query)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, query.Encode())
	}

	// 没有配置对象存储时一律 409。
	noRemote := httptest.NewServer(New(Options{StatePath: e.store.Path(), Files: e.files, BytesPerSecond: 2 << 20}))
	t.Cleanup(noRemote.Close)
	resp, body, _ = get(t, noRemote, "/v1/partition", partitionQuery(key))
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, string(body), "没有配置对象存储")
}

// 远端内容与登记的 sha256 不符：最后 64KB 不写出，客户端拿到长度不足的响应并报错；小于 64KB 的文件一个字节都拿不到。
func TestDownloadWithholdsTheTailWhenSHADiffers(t *testing.T) {
	for _, size := range []int{300 << 10, 10 << 10} {
		e := newEnv(t)
		content := randomBytes(t, size)
		key := e.partition(content, true)
		e.s3.MutateGet = func(_ string, body []byte) []byte {
			changed := bytes.Clone(body)
			changed[len(changed)-1] ^= 0xff
			return changed
		}
		ts := e.server(2<<20, noSleep, time.Now)
		resp, body, err := get(t, ts, "/v1/partition", partitionQuery(key))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Error(t, err, "长度不足的响应必须报错")
		want := max(size-holdBytes, 0)
		require.Equal(t, want, len(body), "size %d", size)
		require.True(t, bytes.Equal(body, content[:want]))
	}
}

// 限速：令牌桶容量一秒的量，超出部分按速率睡眠补足，睡眠总时长 = (大小 − 容量) / 速率。
func TestDownloadIsRateLimited(t *testing.T) {
	e := newEnv(t)
	content := randomBytes(t, 300<<10)
	key := e.partition(content, true)
	// 假时钟：睡眠让时钟前进同样的时长，读时钟不前进。
	var mu sync.Mutex
	var slept time.Duration
	clock := time.Now()
	ts := e.server(100<<10, func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept += d
		clock = clock.Add(d)
		mu.Unlock()
		return nil
	}, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	})
	resp, body, err := get(t, ts, "/v1/partition", partitionQuery(key))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, body, len(content))
	mu.Lock()
	defer mu.Unlock()
	require.InDelta(t, (2 * time.Second).Seconds(), slept.Seconds(), 0.6, "slept %s", slept)
}

// 同时最多 2 个下载，第 3 个返回 429；前面的结束后又能下载。
func TestDownloadConcurrencyLimit(t *testing.T) {
	e := newEnv(t)
	content := randomBytes(t, 1<<10)
	key := e.partition(content, true)
	entered, release := make(chan struct{}, 4), make(chan struct{})
	e.s3.Hook = func(r *http.Request, _ string) (int, string, bool) {
		if r.Method == http.MethodGet {
			entered <- struct{}{}
			<-release
		}
		return 0, "", false
	}
	ts := e.server(2<<20, noSleep, time.Now)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			resp, _, _ := get(t, ts, "/v1/partition", partitionQuery(key))
			codes <- resp.StatusCode
		})
	}
	<-entered
	<-entered
	resp, body, _ := get(t, ts, "/v1/partition", partitionQuery(key))
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Contains(t, string(body), "上限")
	close(release)
	wg.Wait()
	close(codes)
	for code := range codes {
		require.Equal(t, http.StatusOK, code)
	}
	e.s3.Hook = nil
	resp, _, _ = get(t, ts, "/v1/partition", partitionQuery(key))
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

type recentBody struct {
	Columns []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Rows        [][]*string `json:"rows"`
	StagedUntil string      `json:"staged_until"`
	Msg         string      `json:"msg"`
}

func TestRecentRowsReadDatasetJSONAndPage(t *testing.T) {
	e := newEnv(t)
	ts := e.server(2<<20, noSleep, time.Now)
	query := url.Values{"space": {space}, "dataset": {dataset}, "subject": {"BTCUSDT"}}
	resp, body, _ := get(t, ts, "/v1/recent", query)
	require.Equal(t, http.StatusConflict, resp.StatusCode, "还没有 dataset.json")
	require.Contains(t, string(body), "dataset.json")

	meta := e.files.BuildDatasetMeta(export.DatasetMetaInput{SpaceID: space, DatasetID: dataset, Freq: "1m", Purpose: e.task.Purpose, DirName: e.task.DirName,
		PartitionUnit: "day", Timezone: "UTC", Columns: e.task.Columns}, day0)
	require.NoError(t, e.files.WriteDatasetJSON(meta))
	var rows []staging.Row
	for i := range 5 {
		values := []any{float64(i), i%2 == 0}
		if i == 4 {
			values = []any{nil, nil}
		}
		rows = append(rows, staging.Row{SubjectID: "BTCUSDT", DataTime: day0.Add(time.Duration(i) * time.Minute), Values: values})
	}
	rows = append(rows, staging.Row{SubjectID: "ETHUSDT", DataTime: day0.Add(time.Hour), Values: []any{9.0, true}})
	_, err := e.store.Stage(t.Context(), e.task, rows, func(time.Time) staging.PartitionRef {
		return staging.PartitionRef{Key: "2026-10-15", Start: day0, End: day0.AddDate(0, 0, 1)}
	}, day0.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, e.store.AdvanceWatermark(t.Context(), space, dataset, day0.Add(2*time.Hour)))

	query.Set("limit", "3")
	resp, body, _ = get(t, ts, "/v1/recent", query)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var got recentBody
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got.Columns, 5)
	require.Equal(t, "closed", got.Columns[4].Name)
	require.Equal(t, "bool", got.Columns[4].Type)
	require.Equal(t, "2026-10-15T02:00:00Z", got.StagedUntil)
	require.Len(t, got.Rows, 3)
	require.Equal(t, "2026-10-15T00:04:00Z", *got.Rows[0][0])
	require.Nil(t, got.Rows[0][3])
	require.Nil(t, got.Rows[0][4])
	require.Equal(t, "3.0", *got.Rows[1][3])
	require.Equal(t, "false", *got.Rows[1][4])
	require.Equal(t, "", *got.Rows[1][2])

	query.Set("before", *got.Rows[2][0])
	query.Set("limit", "500")
	resp, body, _ = get(t, ts, "/v1/recent", query)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got = recentBody{}
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got.Rows, 2)
	require.Equal(t, "2026-10-15T00:01:00Z", *got.Rows[0][0])
	require.Equal(t, "true", *got.Rows[1][4])

	for name, change := range map[string]func(url.Values){
		"limit too large": func(q url.Values) { q.Set("limit", "501") },
		"limit zero":      func(q url.Values) { q.Set("limit", "0") },
		"bad before":      func(q url.Values) { q.Set("before", "yesterday") },
		"no subject":      func(q url.Values) { q.Del("subject") },
		"bad space":       func(q url.Values) { q.Set("space", ".state") },
	} {
		q := url.Values{"space": {space}, "dataset": {dataset}, "subject": {"BTCUSDT"}}
		change(q)
		resp, _, _ = get(t, ts, "/v1/recent", q)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode, name)
	}
	resp, body, _ = get(t, ts, "/v1/recent", url.Values{"space": {space}, "dataset": {"other"}, "subject": {"BTCUSDT"}})
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Contains(t, string(body), "不在当前执行器上")
	e.assertStoresClosed()
}
