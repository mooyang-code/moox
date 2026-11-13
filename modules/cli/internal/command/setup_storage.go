package command

import (
	"context"
	"errors"
	"fmt"
	"os"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// openRemoteStorage 连上 Storage 主机并打开经操作员网关的调用通道：SSH 用来读取 Storage 的角色密钥，网关用来写元数据。
func openRemoteStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string) (setupssh.Client, commandGateway, error) {
	if snapshot == nil {
		return nil, nil, errors.New("storage_verification_invalid")
	}
	host, err := resolveStorageDeploymentHost(snapshot.Manifest, name)
	if err != nil {
		return nil, nil, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return nil, nil, err
	}
	secrets, err := readRemoteStorageRoleSecrets(ctx, transport, snapshot.Manifest.Paths.Resolved().StorageRoot)
	if err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	// 顺带在本机操作员目录留一份 Primary 角色密钥：导入、导出数据等命令以后不用再去 Storage 主机取。保存失败不影响本次初始化。
	if _, err := saveOperatorStorageAuth(secrets.Primary); err != nil {
		fmt.Fprintln(os.Stderr, "提示：没能在本机保存 Storage 内部写入身份（不影响本次初始化）；之后可执行 moox-cli setup storage-auth")
	}
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	return transport, gateway, nil
}
