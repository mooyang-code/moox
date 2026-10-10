package gatewayclient

import (
	"context"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
)

// 模块的 trpc_go.yaml 可以配置白名单型的全局客户端过滤器（例如 transinfo-blocker）。签名头在 gatewayclient 内部写入，
// 不能再被这类过滤器剥掉，否则目标主机网关会把请求当成未签名的请求拒绝。
func TestGlobalWhitelistClientFilterDoesNotStripSignatureHeaders(t *testing.T) {
	const filterName = "test-whitelist"
	filter.Register(filterName, nil, func(ctx context.Context, req, rsp interface{}, next filter.ClientHandleFunc) error {
		msg := codec.Message(ctx)
		kept := codec.MetaData{}
		for key, value := range msg.ClientMetaData() {
			if key == "x-space-id" {
				kept[key] = value
			}
		}
		msg.WithClientMetaData(kept)
		return next(ctx, req, rsp)
	})
	if err := client.RegisterClientConfig(metadataPath, &client.BackendConfig{
		Callee: metadataPath, ServiceName: metadataPath, Filter: []string{filterName},
	}); err != nil {
		t.Fatal(err)
	}
	defer client.RegisterClientConfig(metadataPath, &client.BackendConfig{Callee: metadataPath, ServiceName: metadataPath})

	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	if _, err := c.Forward(context.Background(), metadataPath, "CreateDataset", codec.SerializationTypeJSON,
		[]byte(`{}`), WithMetadata("x-space-id", []byte("s1"))); err != nil {
		t.Fatalf("签名头被全局过滤器剥掉: %v", err)
	}
	if call := hosts.remote.lastCall(t); call.metadata["x-space-id"] != "s1" {
		t.Fatalf("应用元数据没有送到目标网关: %v", call.metadata)
	}
}

// 调用方既没有设置超时、ctx 也没有截止时间时，调用不能无限期等待。
func TestDefaultTimeoutComesFromCatalog(t *testing.T) {
	hosts := newTwoHosts(t)
	c := hosts.client(t, hosts.tls.ca)
	if got := c.defaultTimeout("trpc.moox.storage.PrimaryStore"); got != 302*time.Second {
		t.Fatalf("PrimaryStore 的默认超时 = %v，want 目录超时 300s 加 2s 余量", got)
	}
	if got := c.defaultTimeout("trpc.moox.unknown.Service"); got != 10*time.Second {
		t.Fatalf("未知服务的默认超时 = %v，want 10s", got)
	}
}
