package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStatusScript(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, lifecycleDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status.sh"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureStorageViewStoppedReadsRuntimeStatus(t *testing.T) {
	// 没有运行的组件，status.sh 输出「未运行」并以退出码 1 结束，这是正常的已停止状态。
	// 只停止没有暂停时，健康检查会在整理元数据的过程中把它重新拉起来，必须被拒绝。
	stopped := t.TempDir()
	writeStatusScript(t, stopped, `echo "主机 storage，发布 r1（abc）"; echo "storage-view: 未运行"; exit 1`)
	if err := ensureStorageViewStopped(stopped); err == nil || !strings.Contains(err.Error(), "先暂停") {
		t.Fatalf("没有暂停的 storage-view 必须被拒绝，got %v", err)
	}
	paused := t.TempDir()
	writeStatusScript(t, paused, `echo "storage-view: 未运行（已暂停）"; exit 0`)
	if err := ensureStorageViewStopped(paused); err != nil {
		t.Fatalf("已暂停且未运行的 storage-view 不应报错：%v", err)
	}
	running := t.TempDir()
	writeStatusScript(t, running, `echo "storage-view: 运行中 pid=100 就绪"; exit 0`)
	if err := ensureStorageViewStopped(running); err == nil || !strings.Contains(err.Error(), "先停止") {
		t.Fatalf("运行中的 storage-view 必须被拒绝，got %v", err)
	}
	notReady := t.TempDir()
	writeStatusScript(t, notReady, `echo "storage-view: 运行中 pid=100 未就绪"; exit 1`)
	if err := ensureStorageViewStopped(notReady); err == nil || !strings.Contains(err.Error(), "先停止") {
		t.Fatalf("运行中但未就绪的 storage-view 也必须被拒绝，got %v", err)
	}
	unknown := t.TempDir()
	writeStatusScript(t, unknown, `echo "没有这个组件"; exit 2`)
	if err := ensureStorageViewStopped(unknown); err == nil || !strings.Contains(err.Error(), "无法确定") {
		t.Fatalf("无法识别的输出必须被拒绝，got %v", err)
	}
	if err := ensureStorageViewStopped(t.TempDir()); err == nil || !strings.Contains(err.Error(), "status.sh") {
		t.Fatalf("没有当前发布时应报错，got %v", err)
	}
}

func TestValidateRetainViewsRequiresExactConfirmedInventory(t *testing.T) {
	keep := []string{
		"crypto/view_binance_kline_1m",
		"crypto/view_crypto_swap_kline_1h",
		"crypto/view_crypto_spot_kline_1h",
		"mooxsys/view_mooxsys_host_resource",
		"mooxsys/view_mooxsys_host_fs",
		"mooxsys/view_mooxsys_host_disk",
		"mooxsys/view_mooxsys_host_net",
		"mooxsys/view_mooxsys_service_metrics",
	}
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: keep, yes: true}); err != nil {
		t.Fatal(err)
	}
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: keep}); err == nil {
		t.Fatal("missing --yes was accepted")
	}
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: keep[:7], yes: true}); err == nil {
		t.Fatal("incomplete keep inventory was accepted")
	}
	duplicate := append([]string(nil), keep...)
	duplicate[7] = duplicate[0]
	if err := validateRetainViewsOptions(retainViewsOptions{metadataDB: "metadata.db", packageRoot: "/tmp/storage", keepViews: duplicate, yes: true}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate keep inventory error = %v", err)
	}
}
