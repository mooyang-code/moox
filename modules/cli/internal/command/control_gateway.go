package command

import (
	"context"
	"io"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

type commandGateway interface {
	adminclient.GatewayForwarder
	gatewayclient.Invoker
	io.Closer
}

var openCommandGateway = func(ctx context.Context, file string, snapshot *setupconfig.Snapshot) (commandGateway, error) {
	if snapshot == nil {
		file = defaultFlag(file, "./moox.toml")
		absolute, err := filepath.Abs(file)
		if err != nil {
			return nil, err
		}
		snapshot, err = setupconfig.Load(absolute, filepath.Dir(absolute))
		if err != nil {
			return nil, err
		}
		defer clearSetupSecrets(snapshot)
	}
	// Commands own the connection lifetime explicitly. A fenced cleanup may
	// continue after the operation context is canceled; individual RPCs still
	// use their own live or canceled contexts.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return gatewayio.Open(context.WithoutCancel(ctx), snapshot)
}
