package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

func TestValidateRepairViewRequiresConfirmation(t *testing.T) {
	opts := repairViewOptions{spaceID: "space", datasetID: "dataset_prices", timeout: defaultRepairTimeout}
	if err := validateRepairViewOptions(opts); err == nil {
		t.Fatal("没有 --yes 或 --dry-run 时应拒绝执行")
	}
	opts.dryRun = true
	if err := validateRepairViewOptions(opts); err != nil {
		t.Fatal(err)
	}
	opts = repairViewOptions{spaceID: "space", timeout: defaultRepairTimeout, yes: true}
	if err := validateRepairViewOptions(opts); err == nil {
		t.Fatal("缺少 --dataset-id 时应拒绝执行")
	}
}

func TestPurgeRepairViewIndexesRemovesIndexWALAndTempDir(t *testing.T) {
	root := t.TempDir()
	duckRoot := filepath.Join(root, "duckdb")
	if err := os.MkdirAll(duckRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	id := viewindex.IndexID("space", "dataset_prices")
	for _, name := range []string{id + ".duckdb", id + ".duckdb.wal"} {
		if err := os.WriteFile(filepath.Join(duckRoot, name), []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tmpDir := filepath.Join(duckRoot, id+".duckdb.tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "duckdb_temp_storage_S32K-0.tmp"), []byte("spill"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := purgeRepairViewIndexes(root, "duckdb", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed paths = %v, want index, WAL and .duckdb.tmp", removed)
	}
	for _, name := range []string{id + ".duckdb", id + ".duckdb.wal", id + ".duckdb.tmp"} {
		if _, err := os.Stat(filepath.Join(duckRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, stat err=%v", name, err)
		}
	}
}

// repairViewFixture 造一个带一份 DuckDB 索引文件的时序数据集，返回 repair-view 的参数、索引文件路径与索引标识。
func repairViewFixture(t *testing.T) (args []string, indexFile, id string) {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "metadata.db")
	indexRoot := filepath.Join(root, "view-indexes")
	confPath := filepath.Join(root, "config", "storage.yaml")
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "storage:\n  metadata:\n    path: " + dbPath + "\n  devices:\n    view_index_root: " + filepath.Join(root, "primary-has-no-indexes") + "\n"
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	viewConfPath := filepath.Join(root, "config", "storage-view.yaml")
	if err := os.WriteFile(viewConfPath, []byte("storage:\n  devices:\n    view_index_root: "+indexRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id = viewindex.IndexID("space", "dataset_prices")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t_datasets (c_space_id TEXT, c_dataset_id TEXT, c_data_kind TEXT, PRIMARY KEY(c_space_id,c_dataset_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t_datasets VALUES ('space','dataset_prices','time_series')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(indexRoot, "duckdb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexRoot, "duckdb", id+".duckdb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"--space-id", "space", "--dataset-id", "dataset_prices", "--storage-conf", confPath, "--view-conf", viewConfPath, "--yes"}, filepath.Join(indexRoot, "duckdb", id+".duckdb"), id
}

// fakeSystemctl 记下 systemctl --user 的调用；fail 返回非空时那一次调用失败。
func fakeSystemctl(t *testing.T, fail func(args []string) error) *[]string {
	t.Helper()
	calls := &[]string{}
	previous := systemctlUser
	systemctlUser = func(_ context.Context, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if fail != nil {
			if err := fail(args); err != nil {
				return []byte("job timed out"), err
			}
		}
		return nil, nil
	}
	t.Cleanup(func() { systemctlUser = previous })
	return calls
}

// repair-view 按顺序停掉 storage-view、删索引文件、再恢复；元数据不动。索引目录取自 storage-view 自己的配置。
func TestRepairViewRebuildsTheNamedDatasetIndex(t *testing.T) {
	args, indexFile, id := repairViewFixture(t)
	calls := fakeSystemctl(t, nil)

	var out bytes.Buffer
	if err := runRepairView(args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "disable --now moox-storage-view.service,enable --now moox-storage-view.service" {
		t.Fatalf("systemctl calls = %v", *calls)
	}
	if _, err := os.Stat(indexFile); !os.IsNotExist(err) {
		t.Fatalf("索引文件应已删除，stat err=%v", err)
	}
	if !strings.Contains(out.String(), id) {
		t.Fatalf("输出应带索引标识：%s", out.String())
	}
}

// 停 storage-view 失败（停得慢、被超时打断）时单元可能已经被停用：命令仍然要恢复它，否则之后每次部署都当成用户停用、不再拉起。
// 报错里带上手工恢复的命令；索引没有动。
func TestRepairViewRestoresStorageViewWhenStoppingFails(t *testing.T) {
	args, indexFile, _ := repairViewFixture(t)
	calls := fakeSystemctl(t, func(args []string) error {
		if args[0] == "disable" {
			return context.DeadlineExceeded
		}
		return nil
	})
	var stderr bytes.Buffer
	err := runRepairView(args, io.Discard, &stderr)
	if err == nil || !strings.Contains(err.Error(), "停掉 storage-view") || !strings.Contains(err.Error(), restoreStorageViewCommand) {
		t.Fatalf("报错应说明停止失败并带恢复命令：%v", err)
	}
	if strings.Join(*calls, ",") != "disable --now moox-storage-view.service,enable --now moox-storage-view.service" {
		t.Fatalf("停止失败后仍要恢复：systemctl calls = %v", *calls)
	}
	if _, err := os.Stat(indexFile); err != nil {
		t.Fatalf("停止失败时不应删索引：%v", err)
	}
	// 恢复也失败：把恢复命令打出来。
	calls = fakeSystemctl(t, func([]string) error { return context.DeadlineExceeded })
	stderr.Reset()
	if err := runRepairView(args, io.Discard, &stderr); err == nil {
		t.Fatal("停止失败应报错")
	}
	if len(*calls) != 2 || !strings.Contains(stderr.String(), restoreStorageViewCommand) {
		t.Fatalf("恢复失败时要给出恢复命令：calls=%v stderr=%s", *calls, stderr.String())
	}
}

// --restart=false：不自动恢复，但告诉用户怎么恢复（停止失败时也一样）。
func TestRepairViewWithoutRestartPrintsRestoreCommand(t *testing.T) {
	args, indexFile, _ := repairViewFixture(t)
	args = append(args, "--restart=false")
	calls := fakeSystemctl(t, nil)
	var stderr bytes.Buffer
	if err := runRepairView(args, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "disable --now moox-storage-view.service" {
		t.Fatalf("--restart=false 不恢复：systemctl calls = %v", *calls)
	}
	if !strings.Contains(stderr.String(), restoreStorageViewCommand) {
		t.Fatalf("要打印恢复命令：%s", stderr.String())
	}
	if _, err := os.Stat(indexFile); !os.IsNotExist(err) {
		t.Fatalf("索引文件应已删除，stat err=%v", err)
	}
	calls = fakeSystemctl(t, func([]string) error { return context.DeadlineExceeded })
	stderr.Reset()
	if err := runRepairView(args, io.Discard, &stderr); err == nil {
		t.Fatal("停止失败应报错")
	}
	if len(*calls) != 1 || !strings.Contains(stderr.String(), restoreStorageViewCommand) {
		t.Fatalf("--restart=false 时停止失败也不自动恢复，但要给出恢复命令：calls=%v stderr=%s", *calls, stderr.String())
	}
}

// 默认超时要明显大于 storage-view 单元的停止时限（TimeoutStopSec=120），否则停得慢时命令自己的超时先到。
func TestRepairViewDefaultTimeoutExceedsUnitStopTimeout(t *testing.T) {
	if defaultRepairTimeout < 4*120*time.Second {
		t.Fatalf("defaultRepairTimeout = %s，要明显大于 120 秒的停止时限", defaultRepairTimeout)
	}
}
