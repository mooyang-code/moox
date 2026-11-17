package commonpb

import "fmt"

// 生命周期请求的校验：服务端的 validation 过滤器（各模块 trpc_go.yaml）对实现了 Validate 的请求先校验再进处理函数。
// 命令与暂停类型的完整解析在 packages/lifecycle（RequestFromProto），这里只拦明显不对的请求。

// Validate 实现请求校验：读状态没有参数。
func (r *GetLifecycleReq) Validate() error { return nil }

// Validate 实现请求校验：命令只有 pause、resume、release。
func (r *ControlLifecycleReq) Validate() error {
	switch r.GetCommand() {
	case "pause", "resume", "release":
		return nil
	}
	return fmt.Errorf("command 只能是 pause、resume、release，收到 %q", r.GetCommand())
}
