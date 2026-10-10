package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDefinitionLifecycleAndVersions(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	if err := repo.CreateDefinition(ctx, Definition{StrategyID: "s1", Name: "momentum", DSLYaml: "name: momentum", DSLHash: "h1", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetDefinition(ctx, "s1")
	if err != nil || got.Name != "momentum" || got.DSLHash != "h1" || got.DeletedAt != nil || !got.CreatedAt.Equal(testNow) {
		t.Fatalf("定义不符：%+v err=%v", got, err)
	}
	if version, err := repo.GetDefinitionVersion(ctx, "h1"); err != nil || version.DSLYaml != "name: momentum" {
		t.Fatalf("版本应已登记：%+v err=%v", version, err)
	}
	if err := repo.UpdateDefinition(ctx, Definition{StrategyID: "s1", Name: "momentum2", DSLYaml: "name: momentum2", DSLHash: "h2", UpdatedAt: testNow.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if version, err := repo.GetDefinitionVersion(ctx, "h2"); err != nil || version.DSLYaml != "name: momentum2" {
		t.Fatalf("新版本应已登记：%+v err=%v", version, err)
	}
	if version, err := repo.GetDefinitionVersion(ctx, "h1"); err != nil || version.DSLYaml != "name: momentum" {
		t.Fatalf("旧版本应保留：%+v err=%v", version, err)
	}
	defs, err := repo.ListDefinitions(ctx)
	if err != nil || len(defs) != 1 || defs[0].Name != "momentum2" {
		t.Fatalf("列表不符：%+v err=%v", defs, err)
	}
	if _, err := repo.GetDefinition(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的定义应返回 ErrNotFound：%v", err)
	}
}

func TestUpdateDefinitionRequiresDisabledInstances(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	err := repo.UpdateDefinition(ctx, Definition{StrategyID: "s1", Name: "new", DSLYaml: "name: new", DSLHash: "h-new", UpdatedAt: testNow})
	if err == nil || !strings.Contains(err.Error(), "启用") {
		t.Fatalf("启用实例应阻止修改定义：%v", err)
	}
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateDefinition(ctx, Definition{StrategyID: "s1", Name: "new", DSLYaml: "name: new", DSLHash: "h-new", UpdatedAt: testNow}); err != nil {
		t.Fatalf("停用后应可修改：%v", err)
	}
}

// S25（定义部分）：被未删除实例引用的定义不能删除；实例软删除后可以；删除后 Get 仍可读、List 不再出现。
func TestSoftDeleteDefinitionRequiresNoLiveInstances(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	if err := repo.DisableInstance(ctx, "i1", ptr("session-1"), nil, "", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDeleteDefinition(ctx, "s1", testNow); err == nil || !strings.Contains(err.Error(), "引用") {
		t.Fatalf("被引用的定义应拒绝删除：%v", err)
	}
	if err := repo.SoftDeleteInstance(ctx, "i1", testNow); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDeleteDefinition(ctx, "s1", testNow); err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.GetDefinition(ctx, "s1")
	if err != nil || deleted.DeletedAt == nil {
		t.Fatalf("已删除定义仍应可读且带删除时间：%+v err=%v", deleted, err)
	}
	if defs, err := repo.ListDefinitions(ctx); err != nil || len(defs) != 0 {
		t.Fatalf("列表不应出现已删除定义：%+v err=%v", defs, err)
	}
	if err := repo.SoftDeleteDefinition(ctx, "s1", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound：%v", err)
	}
	if err := repo.UpdateDefinition(ctx, Definition{StrategyID: "s1", Name: "x", DSLYaml: "name: x", DSLHash: "hx", UpdatedAt: testNow}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已删除定义不应可修改：%v", err)
	}
}
