package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "trade testnet smoke: %v\n", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	getenv func(string) string,
) error {
	options, err := parseOptions(args, getenv)
	if err != nil {
		return err
	}
	cfg, err := config.Load(options.Config)
	if err != nil {
		return err
	}
	// gateway_client 的相对路径按组件目录（配置文件所在目录的上一级）解析，与服务进程一致。
	absConfig, err := filepath.Abs(options.Config)
	if err != nil {
		return err
	}
	gatewayConfig, err := cfg.GatewayClient.ResolvePaths(filepath.Dir(filepath.Dir(absConfig)))
	if err != nil {
		return err
	}
	gateway, err := gatewayclient.New(gatewayclient.Options{Config: gatewayConfig})
	if err != nil {
		return fmt.Errorf("创建 gatewayclient: %w", err)
	}
	defer gateway.Close()
	value, err := secretclient.New(gateway, 10*time.Second).GetExchangeSecret(ctx, options.SecretID)
	if err != nil {
		return fmt.Errorf("GetSecretValue(%s): %w", options.SecretID, err)
	}
	credential, err := credentialFromSecret(value, options.Exchange, options.SecretID)
	if err != nil {
		return err
	}
	phaseCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	switch options.Phase {
	case "submit":
		return runSubmitPhase(phaseCtx, options, credential)
	case "recover":
		return runRecoverPhase(phaseCtx, options, credential)
	default:
		return fmt.Errorf("unsupported phase %q", options.Phase)
	}
}
