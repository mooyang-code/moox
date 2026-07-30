package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/secretclient"
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
	secrets := secretclient.New(secretclient.Config{
		GatewayBaseURL: cfg.Admin.BaseURL,
		ServiceAuth: secretclient.ServiceAuthConfig{
			AccessKey:  cfg.Admin.ServiceAuth.AccessKey,
			SecretKey:  cfg.Admin.ServiceAuth.SecretKey,
			TargetNode: cfg.Admin.ServiceAuth.TargetNode,
			CAFile:     cfg.Admin.ServiceAuth.CAFile,
			ExpireSecs: cfg.Admin.ServiceAuth.ExpireSeconds,
		},
	})
	value, err := secrets.GetExchangeSecret(ctx, options.SecretID)
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
