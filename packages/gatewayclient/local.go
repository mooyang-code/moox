package gatewayclient

import (
	"net"
	"strconv"
	"sync/atomic"
)

var localServiceOverride atomic.Pointer[func(port int) string]

// localServiceAddress 是本机服务的直连地址：127.0.0.1 加服务目录里的端口。
func localServiceAddress(port int) string {
	if override := localServiceOverride.Load(); override != nil {
		return (*override)(port)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// SetLocalServiceAddressForTest 只给测试用：服务目录里的端口是固定的，测试里的假服务监听临时端口，
// 用它把“本机服务的直连地址”换掉。返回的函数恢复原样；它改的是进程级状态，不能和并行测试一起用。
func SetLocalServiceAddressForTest(resolve func(port int) string) (restore func()) {
	previous := localServiceOverride.Swap(&resolve)
	return func() { localServiceOverride.Store(previous) }
}
