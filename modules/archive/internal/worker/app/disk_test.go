//go:build linux || darwin

package app

import "testing"

// 磁盘用量能读到：总量为正，已用与可用都不超过总量，两者之和也不超过总量（差额是 root 保留空间）。
func TestStatfsDiskUsageReadsTheVolume(t *testing.T) {
	total, used, free, err := StatfsDiskUsage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || used == 0 || free > total || used > total || used+free > total {
		t.Fatalf("total=%d used=%d free=%d", total, used, free)
	}
}
