package gatewaycontrol

import (
	"context"
	"net"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

func authorizeContext(remote net.Addr, caller string) context.Context {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{gatewayroute.MetadataVerifiedCaller: []byte(caller)})
	if remote != nil {
		msg.WithRemoteAddr(remote)
	}
	return ctx
}

// 网关控制只在本机回环上提供：对端不是回环地址时，元数据里的调用方身份没有可信度，一律拒绝。
func TestAuthorizeRequiresLoopbackPeer(t *testing.T) {
	loopback := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000}
	external := &net.TCPAddr{IP: net.ParseIP("10.0.0.7"), Port: 40000}

	require.NoError(t, authorize(authorizeContext(loopback, "host-gateway@storage"), "storage"))
	require.ErrorContains(t, authorize(authorizeContext(external, "host-gateway@storage"), "storage"), "回环")
	require.Error(t, authorize(authorizeContext(loopback, "host-gateway@control"), "storage"), "不能代表别的主机")
	require.Error(t, authorize(authorizeContext(loopback, ""), "storage"))
}
