package hostpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 概要不含推送地址；文件不存在时 Present 为 false。
func TestSummarizeOmitsWebhooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if summary := Summarize(path); summary.Present || summary.Error != "" {
		t.Fatalf("missing file: %+v", summary)
	}
	if err := Write(path, samplePolicy()); err != nil {
		t.Fatal(err)
	}
	summary := Summarize(path)
	if !summary.Present || summary.Version != 3 || summary.Channels != 2 || summary.Source != SourceMonitor || summary.ContentHash == "" {
		t.Fatalf("summary = %+v", summary)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "qyapi") || strings.Contains(string(raw), "feishu.cn") {
		t.Fatalf("summary leaks webhook: %s", raw)
	}
}

func samplePolicy() Policy {
	return Policy{
		Version: 3, Source: SourceMonitor, FetchedAt: time.Date(2026, 10, 18, 6, 0, 5, 0, time.UTC),
		NotificationChannels: []Channel{
			{Type: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=a"},
			{Type: "feishu", WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/b"},
		},
		Watchdog: Watchdog{MergeWindow: "10m", CrashLoopFailures: 3},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Write(path, samplePolicy()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentHash != samplePolicy().Hash() || len(got.NotificationChannels) != 2 || got.Watchdog.MergeWindow != "10m" {
		t.Fatalf("got %+v", got)
	}
}

func TestHashIgnoresChannelOrderAndMetadata(t *testing.T) {
	a := samplePolicy()
	b := samplePolicy()
	b.NotificationChannels[0], b.NotificationChannels[1] = b.NotificationChannels[1], b.NotificationChannels[0]
	b.Version, b.FetchedAt, b.Source = 9, time.Now(), SourceSeed
	if a.Hash() != b.Hash() {
		t.Fatal("渠道顺序与元数据不应影响摘要")
	}
	b.NotificationChannels = b.NotificationChannels[:1]
	if a.Hash() == b.Hash() {
		t.Fatal("渠道变化应改变摘要")
	}
}

func TestReadRejectsUnsafeOrInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	loose := filepath.Join(dir, "loose.json")
	if err := Write(loose, samplePolicy()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(loose); err == nil {
		t.Fatal("组或其他人可读的策略文件应被拒绝")
	}
	bad := samplePolicy()
	bad.Watchdog.CrashLoopRetry = "soon"
	if err := Write(filepath.Join(dir, "bad.json"), bad); err == nil {
		t.Fatal("非法时长应被拒绝")
	}
	unknown := samplePolicy()
	unknown.NotificationChannels = []Channel{{Type: "slack", WebhookURL: "https://example.com"}}
	if err := unknown.Validate(); err == nil {
		t.Fatal("未知渠道类型应被拒绝")
	}
}
