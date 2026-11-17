package healthz

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Readiness 把组件 /readyz 用的快照函数包装成上报（packages/report）用的就绪判断：同一个函数，返回是否就绪与未就绪原因。
// Monitor 只按上报判断组件健康（设计 3.8），所以上报里的就绪必须和 /readyz 的结论一致。
func Readiness(snapshot SnapshotFunc) func(context.Context) (bool, string) {
	return func(ctx context.Context) (bool, string) {
		rsp := safeSnapshot(ctx, snapshot)
		if rsp.Ready {
			return true, ""
		}
		return false, NotReadyReason(rsp)
	}
}

// NotReadyReason 返回未就绪的原因：组件填了 Reason 就用它；否则列出 Details 里没通过的项
// （名字以 _ready、_connected、_ok 结尾而取值为 false 的，以及名字以 _error 结尾的非空文字）。都没有时只说未就绪。
func NotReadyReason(rsp Response) string {
	if reason := strings.TrimSpace(rsp.Reason); reason != "" {
		return reason
	}
	var failed []string
	for key, value := range rsp.Details {
		switch typed := value.(type) {
		case bool:
			if !typed && (strings.HasSuffix(key, "_ready") || strings.HasSuffix(key, "_connected") || strings.HasSuffix(key, "_ok")) {
				failed = append(failed, key)
			}
		case string:
			if strings.HasSuffix(key, "_error") && strings.TrimSpace(typed) != "" {
				failed = append(failed, fmt.Sprintf("%s=%s", key, strings.TrimSpace(typed)))
			}
		}
	}
	if len(failed) == 0 {
		return "未就绪"
	}
	sort.Strings(failed)
	return "未通过：" + strings.Join(failed, "，")
}
