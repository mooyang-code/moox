package gatewayclient

import trpc "trpc.group/trpc-go/trpc-go"

// maxFrameBytes 是 tRPC 单帧的上限。组件目录里有些服务声明请求体最大 32 MiB（CloudNodeMgr、FactorEngine、
// Metadata、PrimaryStore、egress.Proxy），而 tRPC 默认的单帧上限只有 10 MiB，超过的帧在 tRPC 层就被拒绝，
// 目录里的声明等于没有。上限是进程级的：所有用到 gatewayclient 的进程（调用方，以及导入它的服务端进程）在这里
// 统一提到 40 MiB，没有导入它的服务端进程自己设置同样的值。
const maxFrameBytes = 40 * 1024 * 1024

func init() {
	if trpc.DefaultMaxFrameSize < maxFrameBytes {
		trpc.DefaultMaxFrameSize = maxFrameBytes
	}
}
