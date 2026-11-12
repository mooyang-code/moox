package collector

import (
	"testing"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

func TestParseProcStatHandlesSpacesAndParenthesesInComm(t *testing.T) {
	// 字段：pid (comm) state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime cutime cstime
	// priority nice threads itrealvalue starttime vsize rss ...
	line := []byte("4242 (my (odd) proc) S 1 4242 4242 0 -1 4194560 100 0 0 0 700 300 0 0 20 0 5 0 123456 104857600 2560 18446744073709551615 1 1 0 0\n")
	stat, err := ParseProcStat(line)
	if err != nil {
		t.Fatalf("ParseProcStat() error = %v", err)
	}
	want := ProcStat{PID: 4242, Comm: "my (odd) proc", StartTime: 123456, CPUTicks: 1000, RSSPages: 2560}
	if stat != want {
		t.Fatalf("stat = %+v, want %+v", stat, want)
	}
	for _, broken := range []string{"", "12 no-comm S 1", "x (a) S 1 2", "7 (short) S 1 2 3"} {
		if _, err := ParseProcStat([]byte(broken)); err == nil {
			t.Fatalf("%q 应当解析失败", broken)
		}
	}
}

func TestProcessNameUsesFirstArgumentOnly(t *testing.T) {
	for _, tt := range []struct {
		cmdline, comm, want string
	}{
		{"/data/moox/storage/current/bin/moox-storage-node\x00--token=secret\x00", "moox-storage-no", "moox-storage-node"},
		{"python3\x00/opt/engine/main.py\x00", "python3", "python3"},
		{"", "kswapd0", "kswapd0"},
		{"nginx: worker process\x00", "nginx", "nginx: worker process"},
	} {
		if got := ProcessName([]byte(tt.cmdline), tt.comm); got != tt.want {
			t.Fatalf("ProcessName(%q) = %q, want %q", tt.cmdline, got, tt.want)
		}
	}
}

func TestProcessTrackerRanksByCPUAndMemory(t *testing.T) {
	const page = 4096
	name := func(stat ProcStat) string { return stat.Comm }
	tracker := &processTracker{}
	first := []ProcStat{
		{PID: 1, Comm: "storage", StartTime: 10, CPUTicks: 1000, RSSPages: 1000},
		{PID: 2, Comm: "duckdb", StartTime: 20, CPUTicks: 500, RSSPages: 3000},
		{PID: 3, Comm: "idle", StartTime: 30, CPUTicks: 50, RSSPages: 0},
		{PID: 4, Comm: "reused", StartTime: 40, CPUTicks: 9000, RSSPages: 10},
	}
	// 第一次采样没有可比的上一次：只按内存排，CPU 一律不可用。
	got := tracker.top(first, 0, false, 4, page, name)
	if len(got) != 3 || got[0].Name != "duckdb" || got[0].CpuAvailable || got[0].RssBytes != 3000*page {
		t.Fatalf("first sample = %+v", got)
	}

	// 整机 4 核、两次采样之间共 6000 个滴答（每核 1500）。storage 用了 5100 → 340%；duckdb 用了 600 → 40%；
	// idle 没有变化；PID 4 的启动时刻变了（PID 被新进程复用），不能相减；PID 5 是新进程。
	second := []ProcStat{
		{PID: 1, Comm: "storage", StartTime: 10, CPUTicks: 6100, RSSPages: 1000},
		{PID: 2, Comm: "duckdb", StartTime: 20, CPUTicks: 1100, RSSPages: 3000},
		{PID: 3, Comm: "idle", StartTime: 30, CPUTicks: 50, RSSPages: 0},
		{PID: 4, Comm: "reused", StartTime: 99, CPUTicks: 20, RSSPages: 10},
		{PID: 5, Comm: "fresh", StartTime: 100, CPUTicks: 400, RSSPages: 20},
	}
	got = tracker.top(second, 6000, true, 4, page, name)
	byName := map[string]*hostmetricpb.ProcessMetric{}
	for _, item := range got {
		byName[item.Name] = item
	}
	if got[0].Name != "storage" || got[0].CpuPercent != 340 || !got[0].CpuAvailable {
		t.Fatalf("top = %+v", got[0])
	}
	if byName["duckdb"].CpuPercent != 40 || byName["duckdb"].RssBytes != 3000*page {
		t.Fatalf("duckdb = %+v", byName["duckdb"])
	}
	if byName["reused"].CpuAvailable || byName["fresh"].CpuAvailable {
		t.Fatalf("PID 复用或新进程不应有 CPU 值：%+v %+v", byName["reused"], byName["fresh"])
	}
	if _, listed := byName["idle"]; listed {
		t.Fatal("没有 CPU 占用也没有常驻内存的进程不应上榜")
	}
}

func TestProcessTrackerCapsEachRankingAtFive(t *testing.T) {
	tracker := &processTracker{}
	var stats []ProcStat
	for pid := 1; pid <= 30; pid++ {
		stats = append(stats, ProcStat{PID: pid, Comm: "p", StartTime: uint64(pid), CPUTicks: 0, RSSPages: uint64(pid)})
	}
	tracker.top(stats, 0, false, 2, 4096, func(ProcStat) string { return "p" })
	// 第二次：CPU 占用与内存排名相反，两个排行各取 5 个，没有重叠 → 10 个。
	for i := range stats {
		stats[i].CPUTicks = uint64(100 * (31 - stats[i].PID))
	}
	named := 0
	got := tracker.top(stats, 100000, true, 2, 4096, func(ProcStat) string { named++; return "p" })
	if len(got) != 10 || named != 10 {
		t.Fatalf("len = %d, named = %d, want 10 each", len(got), named)
	}
	// 按 CPU 从高到低：PID 1～5 是 CPU 前五，PID 26～30 是内存前五，其中 PID 30 的 CPU 最低。
	if got[0].Pid != 1 || got[4].Pid != 5 || got[5].Pid != 26 || got[len(got)-1].Pid != 30 {
		t.Fatalf("order = first pid %d, last pid %d", got[0].Pid, got[len(got)-1].Pid)
	}
}
