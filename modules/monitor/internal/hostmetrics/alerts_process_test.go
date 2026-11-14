package hostmetrics

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

func processSnapshot() *hostmetricpb.HostSnapshot {
	return &hostmetricpb.HostSnapshot{TopProcesses: []*hostmetricpb.ProcessMetric{
		{Pid: 11, Name: "storage-primary", CpuPercent: 340, CpuAvailable: true, RssBytes: 5 << 30},
		{Pid: 12, Name: "duckdb", CpuPercent: 45, CpuAvailable: true, RssBytes: 7 << 30},
		{Pid: 13, Name: "fresh", RssBytes: 1 << 30},
		{Pid: 14, Name: " ", CpuPercent: 900, CpuAvailable: true},
		{Pid: 15, Name: "broken", CpuPercent: math.NaN(), CpuAvailable: true, RssBytes: 1 << 20},
	}}
}

// CPU 告警按 CPU 排、内存告警按内存排；恢复通知不带排行；磁盘空间告警不带。
func TestHostAlertTextListsTopProcesses(t *testing.T) {
	now := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	processes := topProcesses(processSnapshot())
	if len(processes) != 4 {
		t.Fatalf("没有名字的条目应被忽略：%+v", processes)
	}
	cpu := hostSample{agentID: "a", hostname: "storage", metric: HostMetricCPU, value: 91, threshold: 80, processes: processes}
	_, body := hostAlertText(cpu, domain.AlertEventTriggered, now)
	if !strings.Contains(body, "占用最高（进程 CPU 按单核计）：storage-primary CPU 340%、内存 5.0 GB；duckdb CPU 45%、内存 7.0 GB\n") || strings.Contains(body, "broken") {
		t.Fatalf("CPU 告警正文：%s", body)
	}
	memory := cpu
	memory.metric = HostMetricMemory
	_, body = hostAlertText(memory, domain.AlertEventReminder, now)
	// 内存排行有 4 个进程，正文只列前 3 个（第 4 个是 broken）。
	if !strings.Contains(body, "占用最高（进程 CPU 按单核计）：duckdb CPU 45%、内存 7.0 GB；storage-primary CPU 340%、内存 5.0 GB；fresh 内存 1.0 GB\n") || strings.Contains(body, "broken") {
		t.Fatalf("内存告警正文：%s", body)
	}
	if _, body = hostAlertText(cpu, domain.AlertEventResolved, now); strings.Contains(body, "占用最高") {
		t.Fatalf("恢复通知不应带进程排行：%s", body)
	}
	if _, body = hostAlertText(hostSample{hostname: "storage", metric: HostMetricCPU, value: 91, threshold: 80}, domain.AlertEventTriggered, now); strings.Contains(body, "占用最高") {
		t.Fatalf("没有排行时不应出现这一行：%s", body)
	}
}

func TestAlertPayloadKeepsTheFullRanking(t *testing.T) {
	sample := hostSample{agentID: "a", hostname: "storage", metric: HostMetricCPU, value: 91, threshold: 80, processes: topProcesses(processSnapshot())}
	var payload struct {
		Metric       string  `json:"metric"`
		Value        float64 `json:"value"`
		TopProcesses []struct {
			PID        uint32   `json:"pid"`
			Name       string   `json:"name"`
			CPUPercent *float64 `json:"cpu_percent"`
			RSSBytes   uint64   `json:"rss_bytes"`
		} `json:"top_processes"`
	}
	if err := json.Unmarshal(alertPayload(sample, domain.AlertEventTriggered), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Metric != HostMetricCPU || payload.Value != 91 || len(payload.TopProcesses) != 4 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.TopProcesses[0].PID != 11 || payload.TopProcesses[3].PID != 15 {
		t.Fatalf("事件里要带 PID，用来区分同名进程：%+v", payload.TopProcesses)
	}
	// NaN 的 CPU 值不能进 JSON（会让整条事件内容序列化失败），按“没有 CPU 值”记录。
	if payload.TopProcesses[0].Name != "storage-primary" || *payload.TopProcesses[0].CPUPercent != 340 || payload.TopProcesses[3].Name != "broken" || payload.TopProcesses[3].CPUPercent != nil {
		t.Fatalf("top_processes = %+v", payload.TopProcesses)
	}
	if strings.Contains(string(alertPayload(sample, domain.AlertEventResolved)), "top_processes") {
		t.Fatal("恢复事件不应带进程排行")
	}
}

// 只有 CPU、内存告警附带进程排行；磁盘空间告警不带。
func TestAlertProcessesOnlyForCPUAndMemory(t *testing.T) {
	snapshot := processSnapshot()
	for metric, want := range map[string]int{HostMetricCPU: 4, HostMetricMemory: 4, HostMetricFilesystemUsage: 0} {
		if got := len(alertProcesses(metric, snapshot)); got != want {
			t.Fatalf("%s 告警附带 %d 个进程，应为 %d", metric, got, want)
		}
	}
}
