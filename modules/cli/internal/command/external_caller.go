package command

import (
	"context"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// exportRemoteExternalCaller 导出外部调用方签名密钥（实现见 unitdeploy.ExportExternalCaller，外部程序部署也用它）。
func exportRemoteExternalCaller(ctx context.Context, transport setupssh.Client, controlRoot, deployRoot, caller string) (gatewayauth.Credentials, error) {
	return unitdeploy.ExportExternalCaller(ctx, transport, controlRoot, deployRoot, caller)
}
