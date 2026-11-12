package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

func logRow(key, host, component, level, message string, at time.Time) domain.LogRecord {
	return domain.LogRecord{RecordKey: key, HostID: host, Component: component, InstanceID: component + "@" + host, Level: level, Message: message, Caller: "pkg/file.go:1", Fields: "{}", LoggedAt: at}
}

func TestLogRecordsInsertIsIdempotentAndQueryFilters(t *testing.T) {
	repo := openNotificationTestDB(t).Repositories().Logs
	ctx := t.Context()
	base := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	batch := []domain.LogRecord{
		logRow("a|b1|1", "control", "monitor", "error", "写库失败 100%_done", base),
		logRow("a|b1|2", "control", "monitor", "info", "进程启动", base.Add(time.Minute)),
		logRow("s|b1|1", "storage", "storage-node", "error", "磁盘写满", base.Add(2*time.Minute)),
		logRow("s|b1|2", "storage", "storage-node", "fatal", "退出", base.Add(3*time.Minute)),
	}
	batch[1].Event = "process_started"
	stored, err := repo.InsertBatch(ctx, batch)
	if err != nil || stored != 4 {
		t.Fatalf("InsertBatch = %d, %v", stored, err)
	}
	// 重复投递：同一个记录号只保留一条；进程重启后（启动号不同）同样的序号是新记录。
	again := []domain.LogRecord{batch[0], logRow("a|b2|1", "control", "monitor", "error", "重启后的第一条", base.Add(4*time.Minute))}
	again[0].ID = 0
	if stored, err = repo.InsertBatch(ctx, again); err != nil || stored != 1 {
		t.Fatalf("重复投递后新写入 %d 条，应为 1：%v", stored, err)
	}

	query := func(filter LogRecordFilter) []string {
		t.Helper()
		rows, err := repo.Query(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		var messages []string
		for _, row := range rows {
			messages = append(messages, row.Message)
		}
		return messages
	}
	equal := func(name string, got []string, want ...string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s：%v，应为 %v", name, got, want)
		}
	}
	equal("全部（新的在前）", query(LogRecordFilter{}), "重启后的第一条", "退出", "磁盘写满", "进程启动", "写库失败 100%_done")
	equal("按组件", query(LogRecordFilter{Component: "storage-node"}), "退出", "磁盘写满")
	equal("按主机", query(LogRecordFilter{HostID: "control"}), "重启后的第一条", "进程启动", "写库失败 100%_done")
	equal("最低级别 error 不含关键事件", query(LogRecordFilter{Level: "error", HostID: "control"}), "重启后的第一条", "写库失败 100%_done")
	equal("最低级别 fatal", query(LogRecordFilter{Level: "fatal"}), "退出")
	equal("最低级别 error 包含 fatal", query(LogRecordFilter{Level: "error", HostID: "storage"}), "退出", "磁盘写满")
	equal("关键字里的 % 和 _ 按原样匹配", query(LogRecordFilter{Keyword: "100%_"}), "写库失败 100%_done")
	equal("关键字不把 _ 当通配符", query(LogRecordFilter{Keyword: "写库失败_100"}))
	equal("关键字匹配事件名", query(LogRecordFilter{Keyword: "process_started"}), "进程启动")
	equal("时间范围（含起点、不含终点）", query(LogRecordFilter{Start: base.Add(time.Minute), End: base.Add(3 * time.Minute)}), "磁盘写满", "进程启动")
	page, err := repo.Query(ctx, LogRecordFilter{Limit: 2})
	if err != nil || len(page) != 2 {
		t.Fatalf("第一页 = %d 条，%v", len(page), err)
	}
	equal("向更早翻页", query(LogRecordFilter{Limit: 2, BeforeID: page[1].ID}), "磁盘写满", "进程启动")
}

func TestLogRecordsPruneByAgeAndRowLimit(t *testing.T) {
	repo := openNotificationTestDB(t).Repositories().Logs
	ctx := t.Context()
	now := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	var batch []domain.LogRecord
	for i := 0; i < 10; i++ {
		batch = append(batch, logRow(fmt.Sprintf("k|b|%d", i+1), "control", "monitor", "error", fmt.Sprintf("第 %d 条", i+1), now.Add(-time.Duration(10-i)*24*time.Hour)))
	}
	if _, err := repo.InsertBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	// 先按时间：7.5 天前的删掉（第 1～3 条）；再按行数：只留最新的 4 条。
	deleted, err := repo.Prune(ctx, now.Add(-180*time.Hour), 4)
	if err != nil || deleted != 6 {
		t.Fatalf("Prune 删除 %d 行，应为 6：%v", deleted, err)
	}
	rows, err := repo.Query(ctx, LogRecordFilter{})
	if err != nil || len(rows) != 4 || rows[0].Message != "第 10 条" || rows[3].Message != "第 7 条" {
		t.Fatalf("剩余 %d 行：%v", len(rows), err)
	}
	if deleted, err = repo.Prune(ctx, now.Add(-180*time.Hour), 4); err != nil || deleted != 0 {
		t.Fatalf("没有可清理的行时应删除 0 行：%d, %v", deleted, err)
	}
}
