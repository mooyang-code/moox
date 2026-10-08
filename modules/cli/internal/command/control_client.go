package command

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	clicgateway "github.com/mooyang-code/moox/modules/cli/internal/gateway"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// controlCallTimeout 是控制面单次调用的超时。股票全量标的快照会让 SCF 调用公开数据源，
// 耗时可能超过普通请求，这里留足余量但仍有上限。
const controlCallTimeout = 2 * time.Minute

// openControlGateway 以 moox-cli 身份创建经 SSH 隧道访问控制面的 gatewayclient，SSH 连接信息取自 moox.toml。
func openControlGateway(manifest setupconfig.Manifest) (*clicgateway.Client, error) {
	gateway, err := clicgateway.New(manifest, setupssh.Options{Timeout: 15 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("创建 moox-cli 的 gatewayclient: %w", err)
	}
	return gateway, nil
}

// useControlClient 返回命令访问控制面的 adminclient 和对应的关闭函数：
//   - injected 非空时直接使用（测试注入）；
//   - 否则经 SSH 隧道创建，moox.toml 优先用已加载的 manifest，其次用 manifestFile，都没有时读当前目录的 moox.toml。
func useControlClient(injected *adminclient.Client, manifest *setupconfig.Snapshot, manifestFile, spaceID string) (*adminclient.Client, func(), error) {
	spaceID = strings.TrimSpace(spaceID)
	if injected != nil {
		injected.SpaceID = spaceID
		return injected, func() {}, nil
	}
	if manifest == nil {
		path := defaultFlag(strings.TrimSpace(manifestFile), defaultSetupFile)
		loaded, err := setupconfig.Load(path, filepath.Dir(path))
		if err != nil {
			return nil, nil, fmt.Errorf("加载 %s（访问控制面需要其中的 SSH 连接信息）: %w", path, err)
		}
		manifest = loaded
	}
	gateway, err := openControlGateway(manifest.Manifest)
	if err != nil {
		return nil, nil, err
	}
	client := adminclient.New(gateway.Client, controlCallTimeout)
	client.SpaceID = spaceID
	return client, gateway.Close, nil
}
