package hostpolicy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func samplePolicy() Policy {
	return Policy{
		Version: 3, FetchedAt: time.Date(2026, 10, 18, 6, 0, 5, 0, time.UTC),
		NotificationChannels: []Channel{
			{Type: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=a"},
			{Type: "feishu", WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/b"},
		},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-policy.json")
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
	if got.ContentHash != samplePolicy().Hash() || len(got.NotificationChannels) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestHashIgnoresChannelOrderAndMetadata(t *testing.T) {
	a := samplePolicy()
	b := samplePolicy()
	b.NotificationChannels[0], b.NotificationChannels[1] = b.NotificationChannels[1], b.NotificationChannels[0]
	b.Version, b.FetchedAt = 9, time.Now()
	if a.Hash() != b.Hash() {
		t.Fatal("渠道顺序与元数据不应影响摘要")
	}
	b.NotificationChannels = b.NotificationChannels[:1]
	if a.Hash() == b.Hash() {
		t.Fatal("渠道变化应改变摘要")
	} // 只改密钥也要改变摘要，否则主机上的策略缓存不会重写；四种渠道类型都能通过校验。
	c := samplePolicy()
	c.NotificationChannels = []Channel{{Type: "dingtalk", WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=x", Secret: "SEC1"}, {Type: "webhook", WebhookURL: "https://alerts.example.com/moox"}}
	d := c
	d.NotificationChannels = []Channel{{Type: "dingtalk", WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=x", Secret: "SEC2"}, c.NotificationChannels[1]}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if c.Hash() == d.Hash() {
		t.Fatal("密钥变化应改变摘要")
	}
	c.NotificationChannels[0].Type = "sms"
	if err := c.Validate(); err == nil {
		t.Fatal("未知渠道类型应被拒绝")
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
	unknown := samplePolicy()
	unknown.NotificationChannels = []Channel{{Type: "slack", WebhookURL: "https://example.com"}}
	if err := unknown.Validate(); err == nil {
		t.Fatal("未知渠道类型应被拒绝")
	}
}
