package configinit

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"

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
	report := evaluate(parseFacts("os=Linux\narch=x86_64\nosname=Ubuntu 26.04 LTS\ncpu=88\nmem_kb=263854080\ndisk_kb=1800000000\n"), true)
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
	report := evaluate(parseFacts("os=Darwin\narch=arm64\nosname=macOS 15.1\ncpu=4\nmem_bytes=8589934592\ndisk_kb=10485760\n"), true)
	if !report.OK {
		t.Fatalf("只是低于建议配置时应通过: %+v", report)
	}
	for name, want := range map[string]string{"CPU": CheckWarn, "内存": CheckWarn, "磁盘": CheckWarn} {
		if status, detail := statusOf(report, name); status != want {
			t.Fatalf("%s 应为 %s，实际 %s %s", name, want, status, detail)
		}
	}

	minimal := evaluate(parseFacts("os=Linux\ncpu=16\nmem_kb=33554432\ndisk_kb=104857600\n"), true)
	if !minimal.OK {
		t.Fatal("满足最低配置时应通过")
	}
	tinyDisk := evaluate(parseFacts("os=Linux\ncpu=16\nmem_kb=33554432\ndisk_kb=1048576\n"), true)
	if tinyDisk.OK {
		t.Fatal("可用磁盘不足 5 GB 时应失败")
	}
}

func TestEvaluateWindowsAndUnknownSystems(t *testing.T) {
	windows := evaluate(parseFacts("os=Windows\nosname=Microsoft Windows 11 专业版\ncpu=16\nmem_bytes=34359738368\ndisk_bytes=214748364800\n"), true)
	if !windows.OK || windows.OS != "windows" {
		t.Fatalf("Windows 可以登记: %+v", windows)
	}
	if status, detail := statusOf(windows, "系统"); status != CheckWarn || !strings.Contains(detail, "手动安装") {
		t.Fatalf("Windows 应提醒一键安装暂不支持，实际 %s %s", status, detail)
	}
	if unknown := evaluate(parseFacts("garbage"), true); unknown.OK {
		t.Fatal("认不出系统时应失败")
	}
}

func TestPowershellCommandCarriesTheScriptAsUTF16(t *testing.T) {
	command := powershellCommand()
	encoded := command[strings.LastIndex(command, " ")+1:]
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("不是合法的 UTF-16LE base64: %v", err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if string(utf16.Decode(units)) != windowsProbe {
		t.Fatal("解码后应与检测脚本一致")
	}
}

func TestFactorServerNormalize(t *testing.T) {
	local, err := FactorServer{Mode: " local ", Address: "x", SSHPassword: "secret"}.normalize()
	if err != nil || local != (FactorServer{Mode: FactorLocal}) {
		t.Fatalf("本机模式应丢掉其余字段: %+v %v", local, err)
	}
	remote, err := FactorServer{Mode: FactorRemote, Address: " 192.168.0.9 ", SSHUser: " me ", SSHPassword: " pw "}.normalize()
	if err != nil || remote.Address != "192.168.0.9" || remote.SSHUser != "me" || remote.SSHPassword != " pw " || remote.SSHPort != 22 {
		t.Fatalf("remote = %+v %v", remote, err)
	}
	for _, bad := range []FactorServer{{Mode: ""}, {Mode: FactorRemote, SSHUser: "u", SSHPassword: "p"}, {Mode: FactorRemote, Address: "a", SSHUser: "u", SSHPassword: "p", SSHPort: 70000}} {
		if _, err := bad.normalize(); err == nil {
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
