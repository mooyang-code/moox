package alerttext

import "testing"

func TestTopProcessesRanksByTheAlertingMetric(t *testing.T) {
	items := []Process{
		{Name: "duckdb", CPUPercent: 45, CPUAvailable: true, RSSBytes: 2469606195},
		{Name: "storage-primary", CPUPercent: 340.4, CPUAvailable: true, RSSBytes: 5476083302},
		{Name: "fresh", RSSBytes: 6 << 30},
		{Name: "idle", CPUAvailable: true, RSSBytes: 300 << 20},
		{Name: "kworker", CPUPercent: 3, CPUAvailable: true},
	}
	if got, want := TopProcesses(items, false, 3), "storage-primary CPU 340%、内存 5.1 GB；duckdb CPU 45%、内存 2.3 GB；kworker CPU 3%"; got != want {
		t.Fatalf("按 CPU：%q，want %q", got, want)
	}
	// 没有 CPU 值的新进程只写内存。
	if got, want := TopProcesses(items, true, 3), "fresh 内存 6.0 GB；storage-primary CPU 340%、内存 5.1 GB；duckdb CPU 45%、内存 2.3 GB"; got != want {
		t.Fatalf("按内存：%q，want %q", got, want)
	}
	if got := TopProcesses([]Process{{Name: "idle", CPUAvailable: true}}, false, 3); got != "" {
		t.Fatalf("没有占用 CPU 的进程时应为空：%q", got)
	}
	if got := Bytes(300 << 20); got != "300 MB" {
		t.Fatalf("Bytes = %q", got)
	}
}
