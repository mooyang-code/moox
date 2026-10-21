package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestVersionAndUsage(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out); err != nil || out.String() != "moox-replay dev\n" {
		t.Fatalf("version = %q err=%v", out.String(), err)
	}
	if err := run(context.Background(), nil, &out); err == nil {
		t.Fatal("没有子命令应报用法")
	}
	if err := run(context.Background(), []string{"nope"}, &out); err == nil {
		t.Fatal("未知子命令应报错")
	}
}

// 控制命令只操作回放库：没有这个回放时报错；status 在空库里不报错。
func TestStatusAndControlOnEmptyDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "replay.db")
	var out bytes.Buffer
	if err := run(context.Background(), []string{"status", "--db", db}, &out); err != nil {
		t.Fatalf("空库 status：%v", err)
	}
	if err := run(context.Background(), []string{"pause", "--db", db, "--replay", "nope"}, &out); err == nil {
		t.Fatal("不存在的回放不能暂停")
	}
	if err := run(context.Background(), []string{"pause", "--db", db}, &out); err == nil {
		t.Fatal("缺少 --replay 应报错")
	}
}
