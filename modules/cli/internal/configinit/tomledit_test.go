package configinit

import (
	"strings"
	"testing"
)

const editSample = `[admin]
username = "admin"   # 不该保留在字符串值后

[storage_retention]
view_rows_per_series = 5000   # 每个序列 View 保留最近 N 根

[storage_retention.defaults]
"1m" = "7d"
# 注释行
"5m" = "30d"

[hosts.storage]
address = "198.51.100.11"
ssh = { port = 22, username = "ubuntu", password = "pw" }
`

func TestPutKeyEditsOnlyTheTargetLine(t *testing.T) {
	lines := strings.Split(editSample, "\n")
	lines = putKey(lines, "storage_retention", "view_rows_per_series", "6000")
	lines = putKey(lines, "storage_retention.defaults", "1m", `"14d"`)
	lines = putKey(lines, "admin", "username", `"owner"`)
	lines = putKey(lines, "hosts.storage", "ssh", `{ port = 2222, username = "root" }`)
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"view_rows_per_series = 6000   # 每个序列 View 保留最近 N 根", `"1m" = "14d"`, `username = "owner"`, "# 注释行", `"5m" = "30d"`,
		`ssh = { port = 2222, username = "root" }`, `address = "198.51.100.11"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "不该保留") || strings.Contains(got, `password = "pw"`) {
		t.Fatalf("字符串与内联表整行替换:\n%s", got)
	}
}

func TestPutKeyInsertsMissingKeysAndSections(t *testing.T) {
	lines := strings.Split(editSample, "\n")
	lines = putKey(lines, "storage_retention.defaults", "1h", `"365d"`)
	lines = putKey(lines, "local_logs", "max_size_mb", "50")
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "\"5m\" = \"30d\"\n\"1h\" = \"365d\"") {
		t.Fatalf("缺的键应插在段内最后一个键之后:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "[local_logs]\nmax_size_mb = 50") {
		t.Fatalf("缺的段应追加到文件末尾:\n%s", got)
	}
	lines = dropKey(lines, "storage_retention.defaults", "1m")
	if strings.Contains(strings.Join(lines, "\n"), `"1m"`) {
		t.Fatal("dropKey 应删掉这一行")
	}
	if before := strings.Join(lines, "\n"); strings.Join(dropKey(lines, "admin", "missing"), "\n") != before {
		t.Fatal("删除不存在的键不应改动")
	}
}

func TestDropSectionRemovesTheWholeSectionWithItsComments(t *testing.T) {
	sample := `[admin]
username = "admin"

# 归档对象存储（可选）
[archive_object_store]
endpoint = "cos.ap-nanjing.myqcloud.com"
# 段内注释
secret_id = "s3-1"

# 下一段的说明
[storage_retention]
view_rows_per_series = 5000
`
	got := strings.Join(dropSection(strings.Split(sample, "\n"), "archive_object_store"), "\n")
	want := `[admin]
username = "admin"

# 下一段的说明
[storage_retention]
view_rows_per_series = 5000
`
	if got != want {
		t.Fatalf("删段结果:\n%s\nwant:\n%s", got, want)
	}
	// 段在文件末尾。
	tail := "[admin]\nusername = \"admin\"\n\n[archive_object_store]\nendpoint = \"x\"\n"
	if got := strings.Join(dropSection(strings.Split(tail, "\n"), "archive_object_store"), "\n"); got != "[admin]\nusername = \"admin\"\n" {
		t.Fatalf("删末尾的段: %q", got)
	}
	// 没有该段：不动；不碰名字相近的子表。
	if got := strings.Join(dropSection(strings.Split(sample, "\n"), "missing"), "\n"); got != sample {
		t.Fatal("没有该段时不应改动")
	}
	nested := "[hosts]\n[hosts.storage]\naddress = \"x\"\n"
	if got := strings.Join(dropSection(strings.Split(nested, "\n"), "hosts.storage"), "\n"); got != "[hosts]\n" {
		t.Fatalf("只删指定的子表: %q", got)
	}
}
