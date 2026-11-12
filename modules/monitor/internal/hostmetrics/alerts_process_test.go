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
		{Pid: 11, Name: "storage-node", CpuPercent: 340, CpuAvailable: true, RssBytes: 5 << 30},
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
	if !strings.Contains(body, "占用最高：storage-node CPU 340%、内存 5.0 GB；duckdb CPU 45%、内存 7.0 GB") || strings.Contains(body, "broken") {
		t.Fatalf("CPU 告警正文：%s", body)
	}
	memory := cpu
	memory.metric = HostMetricMemory
	_, body = hostAlertText(memory, domain.AlertEventReminder, now)
	if !strings.Contains(body, "占用最高：duckdb CPU 45%、内存 7.0 GB；storage-node CPU 340%、内存 5.0 GB；fresh 内存 1.0 GB") {
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
	// NaN 的 CPU 值不能进 JSON（会让整条事件内容序列化失败），按“没有 CPU 值”记录。
	if payload.TopProcesses[0].Name != "storage-node" || *payload.TopProcesses[0].CPUPercent != 340 || payload.TopProcesses[3].Name != "broken" || payload.TopProcesses[3].CPUPercent != nil {
		t.Fatalf("top_processes = %+v", payload.TopProcesses)
	}
	if strings.Contains(string(alertPayload(sample, domain.AlertEventResolved)), "top_processes") {
		t.Fatal("恢复事件不应带进程排行")
	}
}
