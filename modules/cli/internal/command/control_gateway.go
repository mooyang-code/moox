package command

import (
	"context"
	"io"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

type commandGateway interface {
	adminclient.GatewayForwarder
	gatewayclient.Invoker
	io.Closer
}

// openCommandGateway 经 SSH 隧道连到各主机网关。cfg 为空时读 file 指向的 moox.toml（只读拓扑，不碰秘密文件）。
var openCommandGateway = func(ctx context.Context, file string, cfg *topology.Config) (commandGateway, error) {
	if cfg == nil {
		loaded, err := topology.Load(defaultFlag(file, defaultSetupFile))
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}
	// Commands own the connection lifetime explicitly. A fenced cleanup may
	// continue after the operation context is canceled; individual RPCs still
	// use their own live or canceled contexts.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return gatewayio.Open(context.WithoutCancel(ctx), cfg)
}
