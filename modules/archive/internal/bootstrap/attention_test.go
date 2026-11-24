package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/packages/report"
)

// 只有异常的任务进关注项；对象存储不可用超过 5 分钟、worker 在线时才报。
func TestArchiveAttention(t *testing.T) {
	got := taskAttention([]report.ArchiveTaskLevel{
		{Space: "crypto", Dataset: "b", Level: report.ArchiveTaskHealthError},
		{Space: "crypto", Dataset: "a", Level: report.ArchiveTaskHealthError},
		{Space: "crypto", Dataset: "c", Level: report.ArchiveTaskHealthWarning},
	})
	if len(got) != 1 || got[0] != "crypto/a；crypto/b。用 moox-cli archive tasks list 查看原因" {
		t.Fatalf("任务关注项不对: %v", got)
	}
	if taskAttention([]report.ArchiveTaskLevel{{Space: "crypto", Dataset: "a", Level: report.ArchiveTaskHealthPaused}}) != nil {
		t.Fatal("停用的任务不应报")
	}
	if objectStoreAttention(true, "w", report.ObjectStoreUnavailable, "x") != nil || objectStoreAttention(false, "w", report.ObjectStoreDown, "x") != nil {
		t.Fatal("未满 5 分钟或 worker 不在线时不应报")
	}
	if got := objectStoreAttention(true, "w", report.ObjectStoreDown, "连接被拒绝"); len(got) != 1 {
		t.Fatalf("对象存储不可用应报: %v", got)
	}
}
