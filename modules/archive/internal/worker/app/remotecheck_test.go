package app

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/objectstore/fakes3"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

const checkBucket = "moox-archive-1250000000"

// withRemote 给 harness 接上假 S3（路径风格、HTTP），runner 与 app 共用一个客户端。
func withRemote(t *testing.T, h *harness) *fakes3.Server {
	t.Helper()
	srv := fakes3.New(t, checkBucket, false)
	plain := false
	cfg := objectstore.Config{Endpoint: "cos.ap-nanjing.myqcloud.com", Region: "ap-nanjing", Bucket: checkBucket, URLStyle: objectstore.URLStylePath,
		UseTLS: &plain, StorageClass: "STANDARD_IA", CredentialsFile: "x"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	client, err := objectstore.New(cfg, objectstore.Credentials{AccessKeyID: "AKIDtest", SecretAccessKey: "secret"},
		objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() }))
	if err != nil {
		t.Fatal(err)
	}
	h.app.opts.Remote = client
	h.app.opts.Runner.Remote = client
	return srv
}

func TestHeartbeatCarriesTheObjectStoreSummaryAndRunsReportUploads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.app.heartbeatOnce(ctx)
	if got := h.mgr.heartbeats[0].GetObjectStore(); got.GetEndpoint() != "" {
		t.Fatalf("没配对象存储时心跳里的摘要应为空: %v", got)
	}
	withRemote(t, h)
	h.app.heartbeatOnce(ctx)
	got := h.mgr.heartbeats[1].GetObjectStore()
	if got.GetEndpoint() != "cos.ap-nanjing.myqcloud.com" || got.GetBucket() != checkBucket || got.GetPrefix() != "moox-archive/" || got.GetStorageClass() != "STANDARD_IA" || got.GetUrlStyle() != "path" {
		t.Fatalf("心跳里的对象存储摘要 = %v", got)
	}
	// 昨天的分区在这次运行导出并上传，完成上报带上传数。
	day := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h.src.rows = append(h.src.rows, staging.Row{SubjectID: "BTC", DataTime: day.Add(time.Duration(i) * time.Minute), Values: []any{float64(i)}})
	}
	h.mgr.assignments = []*archivepb.RunAssignment{assignment("r1", func(a *archivepb.RunAssignment) { a.BackfillFrom = "2026-10-16T22:00:00Z" })}
	h.app.pullOnce(ctx)
	req := h.waitComplete()
	if req.GetStatus() != "succeeded" || req.GetPartitionsExported() != 1 || req.GetPartitionsUploaded() != 1 {
		t.Fatalf("complete = %v", req)
	}
}

// seedReplicated 准备一个已复制的分区：本地状态记成已上传，远端放 body（默认就是 content）。
func seedReplicated(t *testing.T, h *harness, srv *fakes3.Server, key string, content, body []byte) {
	t.Helper()
	ctx := context.Background()
	task := staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Columns: []staging.Column{{Name: "close", Type: staging.TypeDouble}}}
	if err := h.store.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", key)
	ref := staging.PartitionRef{Key: key, Start: start, End: start.AddDate(0, 0, 1)}
	if _, err := h.store.Stage(ctx, task, []staging.Row{{SubjectID: "BTC", DataTime: start, Values: []any{1.0}}}, func(time.Time) staging.PartitionRef { return ref }, h.now); err != nil {
		t.Fatal(err)
	}
	sha := sha256.Sum256(content)
	md := md5.Sum(content)
	rel := "crypto/collect/kline-1m/2026/" + key + ".csv"
	if err := h.store.MarkExported(ctx, "crypto", "ds_1m", key, h.now, rel, hex.EncodeToString(sha[:])); err != nil {
		t.Fatal(err)
	}
	remoteKey, _ := objectstore.VersionedKey(rel, hex.EncodeToString(sha[:]))
	if ok, err := h.store.MarkUploaded(ctx, "crypto", "ds_1m", key, hex.EncodeToString(sha[:]),
		staging.Remote{Key: remoteKey, ETag: hex.EncodeToString(md[:]), UploadedAt: h.now.Add(-48 * time.Hour), VerifiedAt: h.now.Add(-48 * time.Hour)}); err != nil || !ok {
		t.Fatalf("MarkUploaded: %v %v", ok, err)
	}
	if body != nil {
		srv.Put(objectstore.DefaultPrefix+remoteKey, body)
	}
	_ = h.store.MarkReported(ctx, "crypto", "ds_1m", []string{key})
}

func partition(t *testing.T, h *harness, key string) staging.PartitionState {
	t.Helper()
	p, ok, err := h.store.Partition(context.Background(), "crypto", "ds_1m", key)
	if err != nil || !ok {
		t.Fatalf("分区 %s: %v", key, err)
	}
	return p
}

func TestMaintenanceSpotCheckResetsMissingOrChangedRemoteCopies(t *testing.T) {
	h := newHarness(t)
	srv := withRemote(t, h)
	good := []byte("good partition")
	seedReplicated(t, h, srv, "2026-10-10", good, good)
	seedReplicated(t, h, srv, "2026-10-11", []byte("missing"), nil)
	seedReplicated(t, h, srv, "2026-10-12", []byte("expected"), []byte("something else"))
	h.app.checkRemote(context.Background())
	if p := partition(t, h, "2026-10-10"); !p.Replicated() || !p.Remote.VerifiedAt.Equal(h.now) || !p.ReportPending {
		t.Fatalf("通过抽查的应更新校验时间并待上报: %+v", p.Remote)
	}
	if p := partition(t, h, "2026-10-11"); p.Replicated() || p.Remote.Key != "" || p.Remote.Error != "远端副本丢失" || !p.ReportPending {
		t.Fatalf("远端丢失的应重置为待上传: %+v", p.Remote)
	}
	if p := partition(t, h, "2026-10-12"); p.Replicated() || p.Remote.Error != "远端副本内容不符" {
		t.Fatalf("ETag 不符的应重置: %+v", p.Remote)
	}
}

func TestMaintenanceSpotCheckDownloadDetectsCorruptContent(t *testing.T) {
	h := newHarness(t)
	srv := withRemote(t, h)
	content := []byte("partition bytes")
	seedReplicated(t, h, srv, "2026-10-10", content, content)
	// HEAD 照常（ETag 按存的内容算），GET 回来的内容被改：只有下载重算 sha256 才能发现。
	srv.MutateGet = func(_ string, body []byte) []byte { body[0] ^= 0xff; return body }
	h.app.checkRemote(context.Background())
	if p := partition(t, h, "2026-10-10"); p.Replicated() || p.Remote.Error != "远端副本内容不符" {
		t.Fatalf("下载内容不符应重置: %+v", p.Remote)
	}
}

func TestMaintenanceSpotCheckLeavesStateAloneOnNetworkErrors(t *testing.T) {
	h := newHarness(t)
	srv := withRemote(t, h)
	content := []byte("partition bytes")
	seedReplicated(t, h, srv, "2026-10-10", content, content)
	before := partition(t, h, "2026-10-10")
	srv.Hook = func(*http.Request, string) (int, string, bool) {
		return http.StatusServiceUnavailable, "SlowDown", true
	}
	h.app.checkRemote(context.Background())
	after := partition(t, h, "2026-10-10")
	if !after.Replicated() || !after.Remote.VerifiedAt.Equal(before.Remote.VerifiedAt) || after.ReportPending {
		t.Fatalf("网络错误不应改状态: %+v", after.Remote)
	}
	// 没配对象存储：什么都不做。
	h.app.opts.Remote = nil
	h.app.checkRemote(context.Background())
}

// 桶名配错（桶不存在）时 HEAD 也是 404：GET 确认是 NoSuchBucket，不能把远端副本记成丢失。
func TestMaintenanceSpotCheckDoesNotResetWhenTheBucketIsMissing(t *testing.T) {
	h := newHarness(t)
	srv := withRemote(t, h)
	content := []byte("partition bytes")
	seedReplicated(t, h, srv, "2026-10-10", content, content)
	plain := false
	cfg := objectstore.Config{Endpoint: "cos.ap-nanjing.myqcloud.com", Region: "ap-nanjing", Bucket: "wrong-bucket-1250000000", URLStyle: objectstore.URLStylePath,
		UseTLS: &plain, CredentialsFile: "x"}
	_ = cfg.Validate()
	wrong, err := objectstore.New(cfg, objectstore.Credentials{AccessKeyID: "AKIDtest", SecretAccessKey: "secret"},
		objectstore.WithRoundTripper(func(http.RoundTripper) http.RoundTripper { return srv.Transport() }))
	if err != nil {
		t.Fatal(err)
	}
	h.app.opts.Remote = wrong
	h.app.checkRemote(context.Background())
	if p := partition(t, h, "2026-10-10"); !p.Replicated() || p.Remote.Error != "" {
		t.Fatalf("桶不存在时不应重置: %+v", p.Remote)
	}
}
