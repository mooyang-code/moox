package main

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// 连不上 EventBus 时报错要带上用的凭据文件和地址：默认路径上留着旧环境的凭据、或用了控制机回环地址的凭据，
// 光看 nats 的 “no servers available” 分辨不出是哪一种。
func TestConnectAdminEventBusErrorNamesTheCredentialFileAndAddress(t *testing.T) {
	_, err := connectAdminEventBus([]string{"nats://127.0.0.1:1"}, "/home/ubuntu/.config/moox/eventbus/internal-admin.yaml", nats.Timeout(300*time.Millisecond))
	require.Error(t, err)
	require.ErrorContains(t, err, "/home/ubuntu/.config/moox/eventbus/internal-admin.yaml")
	require.ErrorContains(t, err, "nats://127.0.0.1:1")
	require.ErrorContains(t, err, "--eventbus-url")
}
