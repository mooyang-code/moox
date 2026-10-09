package marketfetch

import (
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// scfGatewayCache 缓存 SCF 函数实例内复用的外部方式 gatewayclient，连接可以跨调用复用。
var scfGatewayCache struct {
	sync.Mutex
	key    string
	client *gatewayclient.Client
}

// scfGateway 返回 SCF 函数使用的外部方式 gatewayclient：外部接入的地址、实例 ID、调用方身份和密钥都来自 Collector
// 经 CloudNode 写入的函数环境变量。函数实例内环境变量不变，始终复用同一个客户端；环境变量变化时重新创建。
// Collector 进程自己不走这里，而是用内部方式的 gatewayclient。
func scfGateway() (*gatewayclient.Client, error) {
	config, credentials, err := gatewayclient.AccessConfigFromEnv()
	if err != nil {
		return nil, err
	}
	key := strings.Join([]string{config.AccessAddress, config.AccessID, credentials.Caller, credentials.KeyID, credentials.Secret}, "\x00")
	scfGatewayCache.Lock()
	defer scfGatewayCache.Unlock()
	if scfGatewayCache.client != nil && scfGatewayCache.key == key {
		return scfGatewayCache.client, nil
	}
	client, err := gatewayclient.New(gatewayclient.Options{Config: config, Credentials: &credentials})
	if err != nil {
		return nil, err
	}
	if scfGatewayCache.client != nil {
		scfGatewayCache.client.Close()
	}
	scfGatewayCache.key, scfGatewayCache.client = key, client
	return client, nil
}
