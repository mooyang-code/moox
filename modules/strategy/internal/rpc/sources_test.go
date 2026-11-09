package rpc

import (
	"strings"
	"testing"
)

// factor_tasks 的 来源名:市场 键：附加来源的任务与主帧一起登记；来源名写法、个数、重复任务都在保存时拒绝。
func TestNormalizeFactorTasksWithSources(t *testing.T) {
	tasks, err := normalizeFactorTasks(map[string]string{"spot": "ftask_main", " daily:spot ": " ftask_daily ", "weekly:spot": "ftask_weekly"})
	if err != nil || tasks["daily:spot"] != "ftask_daily" || tasks["weekly:spot"] != "ftask_weekly" || len(tasks) != 3 {
		t.Fatalf("来源任务应通过：%v err=%v", tasks, err)
	}
	if tasks, err := normalizeFactorTasks(map[string]string{"multi": "ftask_u", "daily:multi": "ftask_ud"}); err != nil || len(tasks) != 2 {
		t.Fatalf("统一数据集的来源任务应通过：%v err=%v", tasks, err)
	}
	if tasks, err := normalizeFactorTasks(map[string]string{"swap": "ftask_swap", "spot": "ftask_ref"}); err != nil || len(tasks) != 2 {
		t.Fatalf("主帧加参考市场（两个市场键）应通过：%v err=%v", tasks, err)
	}
	for name, tc := range map[string]struct {
		raw  map[string]string
		want string
	}{
		"来源名大写":    {map[string]string{"spot": "ftask_a", "Daily:spot": "ftask_b"}, "来源名必须是小写字母开头的 snake_case"},
		"来源名为空":    {map[string]string{"spot": "ftask_a", ":spot": "ftask_b"}, "来源名必须是小写字母开头的 snake_case"},
		"来源名是保留字":  {map[string]string{"spot": "ftask_a", "bars:spot": "ftask_b"}, "来源名必须是小写字母开头的 snake_case"},
		"来源市场未知":   {map[string]string{"spot": "ftask_a", "daily:future": "ftask_b"}, "不受支持"},
		"来源任务重复":   {map[string]string{"spot": "ftask_a", "daily:spot": "ftask_a"}, "同时登记"},
		"只有来源没有主帧": {map[string]string{"daily:spot": "ftask_a"}, "主帧"},
		"来源超过两个":   {map[string]string{"spot": "ftask_a", "a:spot": "ftask_b", "b:spot": "ftask_c", "c:spot": "ftask_d"}, "至多绑定 2 个附加来源"},
		"来源市场超过两个": {map[string]string{"spot": "ftask_a", "a:spot": "ftask_b", "a:swap": "ftask_c", "a:multi": "ftask_d"}, "至多绑定 2 个市场"},
		"主帧超过两个":   {map[string]string{"spot": "ftask_a", "swap": "ftask_b", "equity": "ftask_c"}, "不能与现货、永续混用"},
	} {
		if _, err := normalizeFactorTasks(tc.raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：期望错误含 %q，实际 %v", name, tc.want, err)
		}
	}
}
