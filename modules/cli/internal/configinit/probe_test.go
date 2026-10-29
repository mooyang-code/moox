package configinit

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

func statusOf(report ProbeReport, name string) (string, string) {
	for _, item := range report.Items {
		if item.Name == name {
			return item.Status, item.Detail
		}
	}
	return "", ""
}

func TestEvaluateAcceptsAWellEquippedLinuxMachine(t *testing.T) {
	report := evaluate(parseFacts("os=Linux\narch=x86_64\nosname=Ubuntu 26.04 LTS\ncpu=88\nmem_kb=263854080\ndisk_kb=1800000000\n"))
	if !report.OK || report.OS != "linux" {
		t.Fatalf("report = %+v", report)
	}
	for _, name := range []string{"系统", "CPU", "内存", "磁盘"} {
		if status, detail := statusOf(report, name); status != CheckOK {
			t.Fatalf("%s 应通过，实际 %s %s", name, status, detail)
		}
	}
	if _, detail := statusOf(report, "内存"); detail != "252 GB" {
		t.Fatalf("内存应按 KiB 换算成 GB，实际 %q", detail)
	}
}

func TestEvaluateWarnsBelowTheRecommendationButOnlyFailsWhenItCannotInstall(t *testing.T) {
	report := evaluate(parseFacts("os=Linux\narch=arm64\nosname=Ubuntu 26.04\ncpu=4\nmem_kb=8388608\ndisk_kb=10485760\n"))
	if !report.OK {
		t.Fatalf("只是低于建议配置时应通过: %+v", report)
	}
	for name, want := range map[string]string{"CPU": CheckWarn, "内存": CheckWarn, "磁盘": CheckWarn} {
		if status, detail := statusOf(report, name); status != want {
			t.Fatalf("%s 应为 %s，实际 %s %s", name, want, status, detail)
		}
	}

	minimal := evaluate(parseFacts("os=Linux\ncpu=16\nmem_kb=33554432\ndisk_kb=104857600\n"))
	if !minimal.OK {
		t.Fatal("满足最低配置时应通过")
	}
	tinyDisk := evaluate(parseFacts("os=Linux\ncpu=16\nmem_kb=33554432\ndisk_kb=1048576\n"))
	if tinyDisk.OK {
		t.Fatal("可用磁盘不足 5 GB 时应失败")
	}
}

func TestEvaluateRejectsNonLinuxSystems(t *testing.T) {
	for name, facts := range map[string]string{
		"macOS":   "os=Darwin\narch=arm64\nosname=macOS 15.1\ncpu=16\nmem_kb=33554432\ndisk_kb=104857600\n",
		"Windows": "os=Windows\nosname=Microsoft Windows 11\ncpu=16\n",
		"未知":      "garbage",
	} {
		report := evaluate(parseFacts(facts))
		if report.OK {
			t.Fatalf("%s：因子引擎只支持 Linux，应拒绝: %+v", name, report)
		}
		if status, detail := statusOf(report, "系统"); status != CheckFail || !strings.Contains(detail, "只支持 Linux") && name != "未知" {
			t.Fatalf("%s：系统检测应失败并说明只支持 Linux，实际 %s %s", name, status, detail)
		}
	}
}

func TestSSHTargetNormalize(t *testing.T) {
	remote, err := SSHTarget{Address: " 192.168.0.9 ", SSHUser: " me "}.normalize("因子机")
	if err != nil || remote.Address != "192.168.0.9" || remote.SSHUser != "me" || remote.SSHPort != 22 {
		t.Fatalf("remote = %+v %v", remote, err)
	}
	for _, bad := range []SSHTarget{{}, {SSHUser: "u"}, {Address: "a", SSHUser: "u", SSHPort: 70000}} {
		if _, err := bad.normalize("因子机"); err == nil {
			t.Fatalf("%+v 应被拒绝", bad)
		}
	}
}

func TestCloudLookupsAreCachedPerKeys(t *testing.T) {
	cloud := newFakeCloud()
	cache := newCloudCache(cloud)
	keys := Keys{SecretID: "AKID", SecretKey: "k1"}
	ctx := context.Background()
	if _, err := cache.lookup(ctx, keys, "198.51.100.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.lookup(ctx, keys, " 198.51.100.10 "); err != nil || cloud.count() != 1 {
		t.Fatalf("同一密钥同一地址应命中缓存: lookups=%d err=%v", cloud.count(), err)
	}
	if _, err := cache.lookup(ctx, Keys{SecretID: "AKID", SecretKey: "k2"}, "198.51.100.10"); err != nil || cloud.count() != 2 {
		t.Fatalf("换了密钥应重新识别: lookups=%d", cloud.count())
	}
	for _, address := range []string{"10.0.0.8", "127.0.0.1", "example.com", "2001:db8::1"} {
		if _, err := cache.lookup(ctx, keys, address); err == nil {
			t.Fatalf("%s 不是公网 IPv4，应直接拒绝", address)
		}
	}
	if cloud.count() != 2 {
		t.Fatal("非法地址不应去调腾讯云")
	}
	if _, err := cache.lookup(ctx, keys, "203.0.113.50"); err == nil || !strings.Contains(err.Error(), "不在当前腾讯云账号下") {
		t.Fatalf("不在账号下应说明，实际 %v", err)
	}
	if _, err := cache.verify(ctx, Keys{SecretID: "AKID", SecretKey: "wrong"}); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("验证失败应报错且不回显密钥，实际 %v", err)
	}
	if account, err := cache.verify(ctx, keys); err != nil || account.AccountID == "" {
		t.Fatalf("验证通过应返回账号: %+v %v", account, err)
	}
}

func TestDescribeInstanceUsesChineseNames(t *testing.T) {
	got := describeInstance(tencent.CloudInstance{Kind: tencent.KindLighthouse, Region: "ap-nanjing", Zone: "ap-nanjing-1", InstanceName: "moox-storage", CPU: 4, MemoryGB: 16, DiskGB: 100})
	if got.KindName != "轻量应用服务器" || got.RegionName != "南京" || got.ZoneName != "南京一区" || got.CPU != 4 {
		t.Fatalf("instance = %+v", got)
	}
	if zoneName("ap-hongkong", "ap-hongkong-2") != "香港二区" {
		t.Fatalf("香港可用区 = %q", zoneName("ap-hongkong", "ap-hongkong-2"))
	}
	if zoneName("ap-guangzhou", "") != "广州" || zoneName("xx-unknown", "xx-unknown-1") != "xx-unknown一区" {
		t.Fatalf("回退显示不对: %q %q", zoneName("ap-guangzhou", ""), zoneName("xx-unknown", "xx-unknown-1"))
	}
	if describeInstance(tencent.CloudInstance{Kind: tencent.KindCVM, Region: "ap-guangzhou"}).KindName != "云服务器" {
		t.Fatal("CVM 应显示为云服务器")
	}
}
