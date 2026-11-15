package command

import (
	"context"
	"errors"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// openRemoteStorage 核对 Storage 主机后打开经操作员网关的调用通道，用来写元数据。
func openRemoteStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string) (commandGateway, error) {
	if snapshot == nil {
		return nil, errors.New("storage_verification_invalid")
	}
	if _, err := resolveStorageDeploymentHost(snapshot.Manifest, name); err != nil {
		return nil, err
	}
	return openCommandGateway(ctx, "", snapshot)
}
