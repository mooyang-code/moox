package runner

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/objectstore/fakes3"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

const remoteBucket = "moox-archive-1250000000"

// withRemote 给测试环境接上一个假 S3（路径风格、HTTP）。
func withRemote(t *testing.T, e *env) *fakes3.Server {
	t.Helper()
	srv := fakes3.New(t, remoteBucket, false)
	plain := false
	cfg := objectstore.Config{Endpoint: "cos.ap-nanjing.myqcloud.com", Region: "ap-nanjing", Bucket: remoteBucket, URLStyle: objectstore.URLStylePath, UseTLS: &plain, CredentialsFile: "x"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	client, err := objectstore.New(cfg, objectstore.Credentials{AccessKeyID: "AKIDtest", SecretAccessKey: "secret"},
		objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() }))
	if err != nil {
		t.Fatal(err)
	}
	e.remote = client
	return srv
}

func countRequests(srv *fakes3.Server, method, contains string) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Method == method && strings.Contains(r.Path, contains) {
			n++
		}
	}
	return n
}

// finalDay 把 10-10 的分区一路导出并定稿（补拉一周前的区间），返回它的登记。
func finalDay(t *testing.T, e *env) *archivepb.PartitionReport {
	t.Helper()
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day.Add(time.Hour), 30, 1)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	p := report(out, "2026-10-10")
	if p == nil || p.GetStatus() != "final" {
		t.Fatalf("应已定稿: %v", p)
	}
	return p
}

func lateRow(e *env) {
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(e.src.rows, rows("ETH", day.Add(2*time.Hour), 1, 9)...)
	e.now = e.now.Add(time.Hour)
}

func rangeOfDay(a *archivepb.RunAssignment) {
	a.Kind, a.RangeStart, a.RangeEnd = "range", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z"
}

func TestExportedPartitionIsUploadedInTheSameRunAndReported(t *testing.T) {
	e := newEnv(t)
	srv := withRemote(t, e)
	p := finalDay(t, e)
	sha := p.GetFileSha256()
	wantKey := "crypto/collect/kline-1m/2026/20261010." + sha[:12] + ".csv"
	if p.GetRemoteKey() != wantKey || p.GetRemoteSha256() != sha || p.GetRemoteVerifiedAt() == "" || p.GetRemoteUploadedAt() == "" || p.GetRemoteError() != "" || p.GetRemoteEtag() == "" {
		t.Fatalf("上报的远端字段 = %v", p)
	}
	obj := srv.Object(objectstore.DefaultPrefix + wantKey)
	local, _ := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(p.GetFilePath())))
	if obj == nil || string(obj.Body) != string(local) || obj.ContentType != "text/csv; charset=utf-8" || obj.Meta["sha256"] != sha || obj.Meta["partition"] != "2026-10-10" {
		t.Fatalf("远端对象 = %+v", obj)
	}
	meta := srv.Object(objectstore.DefaultPrefix + "crypto/collect/kline-1m/dataset.json")
	if meta == nil || meta.ContentType != "application/json" {
		t.Fatalf("dataset.json 应一并上传: %+v", meta)
	}
	// 没有变化的下一次运行：不再上传分区，dataset.json 内容没变也不重传。
	srv.Reset()
	e.now = e.now.Add(time.Hour)
	out := e.run(2000, nil, nil)
	if out.Uploaded != 0 || countRequests(srv, http.MethodPut, "") != 0 {
		t.Fatalf("没有变化不应上传: %+v %v", out, srv.Requests())
	}
}

func TestUploadFailureKeepsTheRunSuccessfulAndBacksOff(t *testing.T) {
	e := newEnv(t)
	srv := withRemote(t, e)
	srv.Hook = func(r *http.Request, _ string) (int, string, bool) {
		if r.Method == http.MethodPut {
			return http.StatusInternalServerError, "InternalError", true
		}
		return 0, "", false
	}
	p := finalDay(t, e)
	// finalDay 的运行：分区上传失败，运行仍成功，消息写明。
	state := partitionState(t, e, "2026-10-10")
	if state.Remote.Attempts != 1 || state.Remote.Error == "" || !state.Remote.RetryAt.Equal(e.now.Add(time.Minute)) || p.GetRemoteError() == "" {
		t.Fatalf("失败后 = %+v / %v", state.Remote, p)
	}
	srv.Reset()
	e.now = e.now.Add(30 * time.Second)
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || countRequests(srv, http.MethodPut, "") != 0 {
		t.Fatalf("退避期间不重试: %+v", out)
	}
	srv.Hook = nil
	e.now = e.now.Add(2 * time.Minute)
	out = e.run(2000, nil, nil)
	got := report(out, "2026-10-10")
	if out.Uploaded != 1 || got.GetRemoteError() != "" || got.GetRemoteSha256() != got.GetFileSha256() || out.Message != "" {
		t.Fatalf("到时刻重试成功后清掉错误: %+v %v", out, got)
	}
	if state := partitionState(t, e, "2026-10-10"); state.Remote.Attempts != 0 || !state.Remote.RetryAt.IsZero() || !state.Replicated() {
		t.Fatalf("成功后 = %+v", state.Remote)
	}
}

func TestUploadFailureMessageAndStopAfterTheFirstRemoteFailure(t *testing.T) {
	e := newEnv(t)
	// 先在没有对象存储时导出两天，再接上对象存储：两个都是遗留的待上传分区。
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(rows("BTC", day.Add(time.Hour), 3, 1), rows("BTC", day.AddDate(0, 0, 1).Add(time.Hour), 3, 1)...)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	srv := withRemote(t, e)
	srv.Hook = func(r *http.Request, _ string) (int, string, bool) {
		if r.Method == http.MethodPut {
			return http.StatusForbidden, "AccessDenied", true
		}
		return 0, "", false
	}
	e.now = e.now.Add(time.Hour)
	out := e.run(2000, nil, nil)
	if out.Status != "succeeded" || !strings.Contains(out.Message, "1 个分区上传对象存储失败：上传") || !strings.Contains(out.Message, "访问被拒绝") {
		t.Fatalf("out = %+v", out)
	}
	if n := countRequests(srv, http.MethodPut, ""); n != 1 {
		t.Fatalf("一次失败后本次运行不再传后面的分区，实际 PUT %d 次", n)
	}
}

// 补传预算按一次运行累计：本次运行导出的分区不受限制，遗留的分区在第一次调用之后累计超过 2 分钟就留给之后的运行。
func TestBacklogBudgetIsCountedPerRunAndSkipsOnlyOlderPartitions(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	e.src.rows = append(rows("BTC", day.Add(time.Hour), 3, 1), rows("BTC", day.AddDate(0, 0, 1).Add(time.Hour), 3, 1)...)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	e.run(2000, func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-10T00:00:00Z" }, nil)
	srv := withRemote(t, e)
	r := e.runner(2000)
	s := &runState{r: r, task: staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Purpose: "collect", DirName: "kline-1m"}, exported: map[string]bool{"2026-10-11": true}}
	out := &Outcome{}
	// 第一次调用记下起点；时钟走过 2 分钟后再调用：只传本次导出的 10-11，遗留的 10-10 留到以后。
	s.upload.backlogSince = e.now.Add(-backlogUploadBudget)
	s.uploadPending(context.Background(), out)
	if out.Uploaded != 1 || countRequests(srv, http.MethodPut, "20261011") != 1 || countRequests(srv, http.MethodPut, "20261010") != 0 {
		t.Fatalf("预算用完后只传本次导出的分区: uploaded=%d %v", out.Uploaded, srv.Requests())
	}
	// 新的一次运行重新计预算：遗留的分区也传上去。
	s2 := &runState{r: r, task: s.task, exported: map[string]bool{}}
	s2.uploadPending(context.Background(), out)
	if out.Uploaded != 2 || countRequests(srv, http.MethodPut, "20261010") != 1 {
		t.Fatalf("下一次运行应补传遗留分区: uploaded=%d", out.Uploaded)
	}
	if s2.upload.backlogSince.IsZero() {
		t.Fatal("第一次调用应记下补传预算的起点")
	}
}

func TestReexportUploadsANewVersionAndKeepsTheOldObject(t *testing.T) {
	e := newEnv(t)
	srv := withRemote(t, e)
	first := finalDay(t, e)
	lateRow(e)
	out := e.run(2000, rangeOfDay, nil)
	second := report(out, "2026-10-10")
	if second.GetFileSha256() == first.GetFileSha256() || second.GetRemoteKey() == first.GetRemoteKey() || second.GetRemoteSha256() != second.GetFileSha256() || out.Uploaded != 1 {
		t.Fatalf("重导出后应上传新版本: %v", second)
	}
	for _, key := range []string{first.GetRemoteKey(), second.GetRemoteKey()} {
		if srv.Object(objectstore.DefaultPrefix+key) == nil {
			t.Fatalf("对象 %s 不应被覆盖或删除", key)
		}
	}
}

func TestReadBackFetchesAMissingFileFromTheObjectStore(t *testing.T) {
	e := newEnv(t)
	withRemote(t, e)
	p := finalDay(t, e)
	path := filepath.Join(e.root, filepath.FromSlash(p.GetFilePath()))
	orig, _ := os.ReadFile(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	lateRow(e)
	out := e.run(2000, rangeOfDay, nil)
	got := report(out, "2026-10-10")
	if out.Status != "succeeded" || !strings.Contains(out.Message, "分区 2026-10-10 已从对象存储取回") || got.GetRowCount() != 31 {
		t.Fatalf("out = %+v %v", out, got)
	}
	rewritten := readFile(t, e.root, got.GetFilePath())
	for _, line := range strings.Split(strings.TrimSpace(string(orig)), "\n")[1:] {
		if !strings.Contains(rewritten, line) {
			t.Fatalf("取回合并后丢了原有的行 %q", line)
		}
	}
}

func TestReadBackKeepsAMismatchedFileAndFetchesTheRemoteCopy(t *testing.T) {
	e := newEnv(t)
	withRemote(t, e)
	p := finalDay(t, e)
	path := filepath.Join(e.root, filepath.FromSlash(p.GetFilePath()))
	if err := os.WriteFile(path, []byte("data_time,subject_id,series_tag,open,close\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lateRow(e)
	out := e.run(2000, rangeOfDay, nil)
	if out.Status != "succeeded" || report(out, "2026-10-10").GetRowCount() != 31 {
		t.Fatalf("out = %+v", out)
	}
	bad, _ := filepath.Glob(path + ".bad-*")
	if len(bad) != 1 {
		t.Fatalf("校验不符的本地文件应改名保留: %v", bad)
	}
	if raw, _ := os.ReadFile(bad[0]); string(raw) != "data_time,subject_id,series_tag,open,close\n" {
		t.Fatal("保留的文件内容应原样")
	}
}

func TestReadBackDoesNotFetchOnOtherErrorsOrWithoutARemoteCopy(t *testing.T) {
	t.Run("权限错误", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root 不受文件权限限制")
		}
		e := newEnv(t)
		srv := withRemote(t, e)
		p := finalDay(t, e)
		path := filepath.Join(e.root, filepath.FromSlash(p.GetFilePath()))
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		srv.Reset()
		lateRow(e)
		out := e.run(2000, rangeOfDay, nil)
		if out.Status != "failed" || countRequests(srv, http.MethodGet, "") != 0 {
			t.Fatalf("权限错误不取回、运行失败: %+v", out)
		}
		if bad, _ := filepath.Glob(path + ".bad-*"); len(bad) != 0 {
			t.Fatal("权限错误不改名")
		}
	})
	t.Run("远端也没有", func(t *testing.T) {
		e := newEnv(t)
		srv := withRemote(t, e)
		p := finalDay(t, e)
		_ = os.Remove(filepath.Join(e.root, filepath.FromSlash(p.GetFilePath())))
		srv.Delete(objectstore.DefaultPrefix + p.GetRemoteKey())
		lateRow(e)
		out := e.run(2000, rangeOfDay, nil)
		if out.Status != "failed" || !strings.Contains(out.Message, "从对象存储取回") || !strings.Contains(out.Message, "对象不存在") {
			t.Fatalf("out = %+v", out)
		}
	})
	t.Run("没有配置对象存储", func(t *testing.T) {
		e := newEnv(t)
		p := finalDay(t, e)
		if p.GetRemoteKey() != "" {
			t.Fatalf("没配对象存储时远端字段应为空: %v", p)
		}
		_ = os.Remove(filepath.Join(e.root, filepath.FromSlash(p.GetFilePath())))
		lateRow(e)
		out := e.run(2000, rangeOfDay, nil)
		if out.Status != "failed" || !strings.Contains(out.Message, "本地文件缺失且没有可用的远端副本") {
			t.Fatalf("out = %+v", out)
		}
	})
}

// 新机器：暂存库与本地文件都没有，按 mgr 下发的登记（带远端字段）恢复，读回时从对象存储取回。
func TestRestoredRegistrationFetchesFromTheObjectStoreOnANewMachine(t *testing.T) {
	old := newEnv(t)
	srv := withRemote(t, old)
	registered := finalDay(t, old)
	fresh := newEnv(t)
	fresh.remote = old.remote
	fresh.src.rows = append(old.src.rows, rows("ETH", time.Date(2026, 10, 10, 2, 0, 0, 0, time.UTC), 1, 9)...)
	fresh.now = old.now.Add(time.Hour)
	out := fresh.run(2000, func(a *archivepb.RunAssignment) {
		rangeOfDay(a)
		a.RegisteredPartitions = []*archivepb.PartitionReport{registered}
	}, nil)
	got := report(out, "2026-10-10")
	if out.Status != "succeeded" || got.GetRowCount() != 31 || !strings.Contains(out.Message, "已从对象存储取回") {
		t.Fatalf("out = %+v %v", out, got)
	}
	// 新版本随即上传；旧对象保留。
	if got.GetRemoteSha256() != got.GetFileSha256() || srv.Object(objectstore.DefaultPrefix+registered.GetRemoteKey()) == nil {
		t.Fatalf("新版本 = %v", got)
	}
}

// 恢复出来的、远端不是当前版本的分区：本地没有文件时传不了，记“本地文件不存在”。
func TestRestoredPartitionWithoutALocalFileReportsWhyItCannotUpload(t *testing.T) {
	old := newEnv(t)
	registered := finalDay(t, old) // 没有对象存储：远端字段为空
	fresh := newEnv(t)
	withRemote(t, fresh)
	fresh.now = old.now.Add(time.Hour)
	out := fresh.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.RangeStart, a.RangeEnd = "range", "2026-10-12T00:00:00Z", "2026-10-12T01:00:00Z"
		a.RegisteredPartitions = []*archivepb.PartitionReport{registered}
	}, nil)
	if !strings.Contains(out.Message, "本地文件不存在") {
		t.Fatalf("out = %+v", out)
	}
	if p := partitionState(t, fresh, "2026-10-10"); p.Remote.Error != "本地文件不存在" || p.Remote.Attempts != 1 {
		t.Fatalf("恢复的分区 = %+v", p.Remote)
	}
}

func TestDatasetJSONIsUploadedOnlyWhenItChangedAndAPartitionUploaded(t *testing.T) {
	e := newEnv(t)
	srv := withRemote(t, e)
	finalDay(t, e)
	metaKey := objectstore.DefaultPrefix + "crypto/collect/kline-1m/dataset.json"
	before := string(srv.Object(metaKey).Body)
	// 数据集说明变了，但本次没有分区上传：不传。
	srv.Reset()
	e.now = e.now.Add(time.Hour)
	e.run(2000, func(a *archivepb.RunAssignment) { a.DatasetMetaJson = `{"name":"改名了"}` }, nil)
	if countRequests(srv, http.MethodPut, "dataset.json") != 0 {
		t.Fatal("没有分区上传成功时不传 dataset.json")
	}
	// 有分区上传成功：内容变了才传。
	lateRow(e)
	e.run(2000, func(a *archivepb.RunAssignment) { rangeOfDay(a); a.DatasetMetaJson = `{"name":"改名了"}` }, nil)
	if countRequests(srv, http.MethodPut, "dataset.json") != 1 || string(srv.Object(metaKey).Body) == before || !strings.Contains(string(srv.Object(metaKey).Body), "改名了") {
		t.Fatal("内容变了且有分区上传成功时应上传 dataset.json")
	}
}
