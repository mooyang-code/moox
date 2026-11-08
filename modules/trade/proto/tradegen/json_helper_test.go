package tradepb

import (
	"testing"

	"trpc.group/trpc-go/trpc-go/codec"
)

// marshalWithTRPCJSON 用服务端实际使用的 JSON 序列化（trpc-go 注册的 SerializationTypeJSON）编码响应，
// 网关把 Trade 的响应转给管理台前端时走的就是这条路径。
func marshalWithTRPCJSON(t *testing.T, value any) string {
	t.Helper()
	serializer := codec.GetSerializer(codec.SerializationTypeJSON)
	if serializer == nil {
		t.Fatal("没有注册 JSON 序列化")
	}
	raw, err := serializer.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
