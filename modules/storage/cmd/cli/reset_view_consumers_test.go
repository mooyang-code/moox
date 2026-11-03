package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/packages/events"
	_ "modernc.org/sqlite"
)

func TestValidateResetViewConsumersRequiresExplicitConfirmation(t *testing.T) {
	opts := resetViewConsumersOptions{
		stream:  events.StorageViewConsumerStream,
		timeout: time.Minute,
	}
	if err := validateResetViewConsumersOptions(opts); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected explicit confirmation error, got %v", err)
	}
	opts.dryRun = true
	if err := validateResetViewConsumersOptions(opts); err != nil {
		t.Fatalf("dry-run should not require confirmation: %v", err)
	}
}

func TestStageResetViewIndexesRestoresIndex(t *testing.T) {
	root := t.TempDir()
	view := resetViewRecord{SpaceID: "crypto", ViewID: "kline", Engine: "duckdb"}
	id := viewindex.ViewIndexID(view.SpaceID, view.ViewID)
	path := filepath.Join(root, "duckdb", id+".duckdb")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		t.Fatal(err)
	}
	staging, moves, err := stageResetViewIndexes(root, []resetViewRecord{view})
	if err != nil {
		t.Fatal(err)
	}
	for _, move := range moves {
		if _, err := os.Stat(move.original); !os.IsNotExist(err) {
			t.Fatalf("original index still exists after staging: %s", move.original)
		}
	}
	if err := restoreResetViewIndexes(moves); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(staging)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("restored index missing: %v", err)
	}
}

func TestResetViewsActiveRequiresAnActiveIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE t_views (c_status TEXT, c_engine TEXT, c_active_index_id TEXT, c_indexed_from TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES ('active','duckdb','view_a',?)`, time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ready, err := resetViewsActive(context.Background(), dbPath)
	if err != nil || !ready {
		t.Fatalf("active View ready=%v err=%v", ready, err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE t_views SET c_active_index_id = ''`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	ready, err = resetViewsActive(context.Background(), dbPath)
	if err != nil || ready {
		t.Fatalf("missing active View ready=%v err=%v", ready, err)
	}
}

func TestValidateResetViewConsumersRejectsWrongStreamAndTimeout(t *testing.T) {
	opts := resetViewConsumersOptions{
		stream:  "OTHER",
		timeout: time.Minute,
		yes:     true,
	}
	if err := validateResetViewConsumersOptions(opts); err == nil || !strings.Contains(err.Error(), events.StorageViewConsumerStream) {
		t.Fatalf("expected stream validation error, got %v", err)
	}
	opts.stream = events.StorageViewConsumerStream
	opts.timeout = 0
	if err := validateResetViewConsumersOptions(opts); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout validation error, got %v", err)
	}
}

func TestValidateResetPrimaryPathRequiresPackageDataChild(t *testing.T) {
	root := t.TempDir()
	if _, err := validateResetPrimaryPath(filepath.Join(root, "data", "storage-node", "pebble"), root); err != nil {
		t.Fatalf("expected package data path to be allowed: %v", err)
	}
	// 原生部署：Pebble 在 <包根>/storage-node/var/storage/pebble（线上第一次执行时就因为只认 data/ 而在清事件流之后才被拒绝）。
	if _, err := validateResetPrimaryPath(filepath.Join(root, "storage-node", "var", "storage", "pebble"), root); err != nil {
		t.Fatalf("expected native component var path to be allowed: %v", err)
	}
	for _, path := range []string{
		root, filepath.Dir(root), filepath.Join(root, "config"), filepath.Join(root, "data"),
		filepath.Join(root, "storage-node"), filepath.Join(root, "storage-node", "var"), filepath.Join(root, "storage-node", "config"),
		filepath.Join(root, "other", "var", "storage"), filepath.Join(root, "data", "..", "..", "etc"),
	} {
		if _, err := validateResetPrimaryPath(path, root); err == nil {
			t.Fatalf("expected unsafe primary path %q to be rejected", path)
		}
	}
}

func TestResetViewsActiveIgnoresDisabledViews(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t_views (c_status TEXT, c_engine TEXT, c_active_index_id TEXT, c_indexed_from TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_views VALUES ('disabled','duckdb','','')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ready, err := resetViewsActive(context.Background(), dbPath)
	if err != nil || !ready {
		t.Fatalf("disabled-only View set should not block reset ready=%v err=%v", ready, err)
	}
}

func TestResetReadyWaitErrorSaysTheResetIsAlreadyCommitted(t *testing.T) {
	// 等 View 重建超时：说明重置已完成、View 在后台重建、不要重复执行，并给出建议超时。
	err := resetReadyWaitError(context.DeadlineExceeded, true, 20*time.Minute)
	for _, want := range []string{"重置已完成", "20m0s", "后台", "不需要也不应该重新执行", "t_views", "设为 60m"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("报错缺少 %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatal("报错不应只剩英文的 context deadline exceeded")
	}

	// 清空 Primary 后只等 storage-view 健康，不能说成在重建 View。
	err = resetReadyWaitError(context.DeadlineExceeded, false, time.Minute)
	if !strings.Contains(err.Error(), "重置已完成") || !strings.Contains(err.Error(), "storage-view 就绪") || strings.Contains(err.Error(), "回溯重建") {
		t.Fatalf("清空全部数据后的超时报错不对: %v", err)
	}

	// 被中断与超时要区分开。
	err = resetReadyWaitError(context.Canceled, true, time.Minute)
	if !strings.Contains(err.Error(), "等待被中断") || strings.Contains(err.Error(), "--timeout（") {
		t.Fatalf("中断时的报错不对: %v", err)
	}

	// 其他错误（例如读元数据失败）保持原样，不能被说成“重置已完成”。
	other := errors.New("boom")
	err = resetReadyWaitError(other, true, time.Minute)
	if !errors.Is(err, other) || strings.Contains(err.Error(), "重置已完成") {
		t.Fatalf("非超时错误不应改写: %v", err)
	}
}

// resetE2EFixture 准备假的启停与健康检查脚本、带一个已有活动索引的 View 的元数据库和最小配置，
// 返回 resetViewConsumers 的选项与记录启停顺序的文件、元数据库路径。健康检查脚本每次运行都会写 health.log，
// runResetE2E 据此判断命令已进入等待就绪阶段。
func resetE2EFixture(t *testing.T, storageViewReady bool) (opts resetViewConsumersOptions, calls, dbPath string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "pkg")
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls = filepath.Join(parent, "calls.log")
	ready := "false"
	if storageViewReady {
		ready = "true"
	}
	for name, body := range map[string]string{
		"start.sh":       "#!/bin/sh\necho start >> " + calls + "\n",
		"stop.sh":        "#!/bin/sh\necho stop >> " + calls + "\n",
		"healthcheck.sh": "#!/bin/sh\necho health >> " + filepath.Join(parent, "health.log") + "\necho '{\"components\":[{\"id\":\"storage-view\",\"ready\":" + ready + "}]}'\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dbPath = filepath.Join(parent, "meta.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE t_views (
  c_space_id TEXT, c_view_id TEXT, c_engine TEXT, c_status TEXT, c_dataset_id TEXT, c_active_index_id TEXT,
  c_view_revision INTEGER, c_active_columns_json TEXT, c_active_view_schema_hash TEXT,
  c_indexed_from TEXT, c_indexed_to TEXT, c_attrs_json TEXT, c_mtime TEXT,
  PRIMARY KEY(c_space_id,c_view_id));
CREATE TABLE t_view_period_dataset_states (c_space_id TEXT, c_view_id TEXT);
CREATE TABLE t_view_sync_points (c_space_id TEXT, c_view_id TEXT);
INSERT INTO t_views (c_space_id,c_view_id,c_engine,c_status,c_dataset_id,c_active_index_id,c_attrs_json)
VALUES ('crypto','view','duckdb','active','ds_a','view_a','{"space_id":"crypto","view_id":"view"}');`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	conf := filepath.Join(root, "config", "storage.yaml")
	yaml := "storage:\n  root: " + parent + "\n  metadata:\n    path: " + dbPath + "\n  devices:\n    pebble_path: " + filepath.Join(root, "data", "pebble") + "\n    view_index_root: " + filepath.Join(parent, "view-indexes") + "\n" +
		"  view:\n    fetch_batch: 1\n    max_workers: 1\n    max_ack_pending: 1\n"
	if err := os.WriteFile(conf, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	opts = resetViewConsumersOptions{storageConf: conf, packageRoot: root, stream: events.StorageViewConsumerStream, timeout: resetE2ETimeout, yes: true, restart: true}
	return opts, calls, dbPath
}

// resetE2ETimeout 是传给命令的 --timeout，只用于核对报错文案；何时到点由 runResetE2E 决定。
const resetE2ETimeout = 8 * time.Second

// runResetE2E 模拟 --timeout 在等待 storage-view 就绪时到点：等健康检查脚本第一次运行后才让截止时间到达。
// 不能用真实计时：本机 CI 运行器负载高时，清理和拉起子进程就可能用完整个超时，截止时间落在拉起过程中，
// 报 start storage-view: signal: killed 而不是要验证的“重置已完成”。
func runResetE2E(opts resetViewConsumersOptions) error {
	ctx := &expiringContext{Context: context.Background(), done: make(chan struct{})}
	// 兜底：命令始终没进入等待阶段时也要到点，避免测试挂住。
	fallback := time.AfterFunc(time.Minute, ctx.expire)
	defer fallback.Stop()
	marker := filepath.Join(filepath.Dir(opts.packageRoot), "health.log")
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(marker); err == nil {
				ctx.expire()
				return
			}
			select {
			case <-ctx.done:
				return
			case <-ticker.C:
			}
		}
	}()
	defer ctx.expire()
	return resetViewConsumers(ctx, opts, io.Discard, io.Discard)
}

// expiringContext 由测试决定何时到点；到点后 Err 返回 context.DeadlineExceeded，与 --timeout 到期时一致。
type expiringContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func (c *expiringContext) Done() <-chan struct{} { return c.done }

func (c *expiringContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *expiringContext) expire() { c.once.Do(func() { close(c.done) }) }

// View 一直没有重建出活动索引：命令在 --timeout 到点时要报“重置已完成、仍在后台重建”，而不是笼统的 context deadline exceeded。
func TestResetViewConsumersReportsCommittedResetWhenViewsAreStillRebuilding(t *testing.T) {
	opts, calls, dbPath := resetE2EFixture(t, true)
	admin := &fakeEventBusAdmin{}
	useFakeEventBusAdmin(t, admin)

	err := runResetE2E(opts)
	if err == nil {
		t.Fatal("View 没有重建完成，命令应因超时返回错误")
	}
	for _, want := range []string{"重置已完成", "后台", "--timeout（" + resetE2ETimeout.String() + "）", "设为 60m"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("报错缺少 %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("报错不应只是 context deadline exceeded: %v", err)
	}
	log, _ := os.ReadFile(calls)
	if string(log) != "stop\nstart\n" {
		t.Fatalf("启停顺序 = %q，要先停后启且超时后不能再停", log)
	}
	if len(admin.deleted) == 0 || admin.purgeCalls == 0 {
		t.Fatalf("EventBus 的删除与清除都应已执行: deleted=%v purgeCalls=%d", admin.deleted, admin.purgeCalls)
	}
	// 重置确实已提交：View 的活动索引被清掉了。
	check, _ := sql.Open("sqlite", dbPath)
	defer check.Close()
	var active string
	if err := check.QueryRow(`SELECT c_active_index_id FROM t_views WHERE c_view_id='view'`).Scan(&active); err != nil || active != "" {
		t.Fatalf("active index = %q err=%v，重置应已清掉活动索引", active, err)
	}
}

// 清空全部数据后 View 本来就是空的，只等 storage-view 健康：超时报错不能说成 View 在回溯重建。
func TestResetViewConsumersWithFullWipeReportsStorageViewNotReady(t *testing.T) {
	opts, _, _ := resetE2EFixture(t, false)
	opts.resetAllStorageData = true
	useFakeEventBusAdmin(t, &fakeEventBusAdmin{})

	err := runResetE2E(opts)
	if err == nil {
		t.Fatal("storage-view 一直未就绪，命令应因超时返回错误")
	}
	if !strings.Contains(err.Error(), "重置已完成") || !strings.Contains(err.Error(), "storage-view 就绪") {
		t.Fatalf("报错不对: %v", err)
	}
	if strings.Contains(err.Error(), "回溯重建") || strings.Contains(err.Error(), "60m") {
		t.Fatalf("清空全部数据后不该提示 View 在重建或建议加长超时: %v", err)
	}
}
