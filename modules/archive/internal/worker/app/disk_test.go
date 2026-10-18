package app

import "testing"

// 磁盘余量在各平台（含 Windows 的 GetDiskFreeSpaceEx）都能读到：总量为正、可用量不超过总量。
func TestStatfsDiskUsageReadsTheVolume(t *testing.T) {
	total, free, err := StatfsDiskUsage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || free > total {
		t.Fatalf("total=%d free=%d", total, free)
	}
}
