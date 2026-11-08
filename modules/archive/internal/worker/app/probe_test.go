package app

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

// probed 探测一次后发一次心跳，返回心跳里的对象存储摘要。
func probed(t *testing.T, h *harness) *archivepb.ObjectStoreInfo {
	t.Helper()
	h.app.ProbeOnce(context.Background())
	h.app.HeartbeatOnce(context.Background())
	return h.mgr.heartbeats[len(h.mgr.heartbeats)-1].GetObjectStore()
}

// 404 算可用；403 算不可用，连续失败时 unavailable_since 保持首次失败时刻；恢复后两个字段清空。探测的是 <prefix>_probe/absent。
func TestProbeReportsObjectStoreAvailability(t *testing.T) {
	h := newHarness(t)
	srv := withRemote(t, h)
	if got := probed(t, h); got.GetProbeError() != "" || got.GetUnavailableSince() != "" {
		t.Fatalf("404 应算可用: %v", got)
	}
	if reqs := srv.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodHead || !strings.HasSuffix(reqs[0].Path, "/moox-archive/_probe/absent") {
		t.Fatalf("探测请求 = %+v", reqs)
	}
	srv.Hook = func(r *http.Request, key string) (int, string, bool) {
		return http.StatusForbidden, "AccessDenied", true
	}
	first := h.now
	if got := probed(t, h); !strings.Contains(got.GetProbeError(), "访问被拒绝") || got.GetUnavailableSince() != first.Format(time.RFC3339) {
		t.Fatalf("403 应算不可用: %v", got)
	}
	h.now = h.now.Add(3 * time.Minute)
	if got := probed(t, h); got.GetProbeError() == "" || got.GetUnavailableSince() != first.Format(time.RFC3339) {
		t.Fatalf("连续不可用的起点应保持首次失败时刻: %v", got)
	}
	srv.Hook = nil
	if got := probed(t, h); got.GetProbeError() != "" || got.GetUnavailableSince() != "" {
		t.Fatalf("恢复后应清空: %v", got)
	}
}

// 连接被拒绝算不可用。
func TestProbeConnectionRefusedIsUnavailable(t *testing.T) {
	h := newHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	plain := false
	cfg := objectstore.Config{Endpoint: addr, Region: "ap-nanjing", Bucket: checkBucket, URLStyle: objectstore.URLStylePath, UseTLS: &plain, CredentialsFile: "x"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	client, err := objectstore.New(cfg, objectstore.Credentials{AccessKeyID: "AKIDtest", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	h.app.opts.Remote = client
	if got := probed(t, h); !strings.Contains(got.GetProbeError(), "refused") || got.GetUnavailableSince() != h.now.Format(time.RFC3339) {
		t.Fatalf("拒绝连接应算不可用: %v", got)
	}
}
