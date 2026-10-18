package domain

import (
	"testing"
	"time"
)

func TestScopedResultIDDiffersAcrossSpacesForSameCheckAndInstant(t *testing.T) {
	at := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	a := ScopedResultID("space-a", "dataset:storage:bars:1m", at)
	b := ScopedResultID("space-b", "dataset:storage:bars:1m", at)
	if a == b {
		t.Fatalf("两个空间的同名检查在同一时刻必须得到不同的结果 ID: %s", a)
	}
	// 空间名里的分隔符不能让不同的 (空间, 检查) 组合撞出同一个 ID
	if ScopedResultID("a/b", "c", at) == ScopedResultID("a", "b/c", at) {
		t.Fatal("空间与检查 ID 的拼接存在歧义")
	}
	if a != ScopedResultID("space-a", "dataset:storage:bars:1m", at) {
		t.Fatal("同一输入必须得到稳定的 ID")
	}
}
