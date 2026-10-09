package main

import (
	"fmt"
	"os"
)

func main() {
	if isBootstrapCommand(os.Args) {
		if err := runBootstrapCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isTopologyRecoveryCommand(os.Args) {
		if err := runTopologyRecoveryCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isPKICommand(os.Args) {
		if err := runPKICommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isKeysCommand(os.Args) {
		if err := runKeysCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isAdminUserCommand(os.Args) {
		if err := runAdminUserCommand(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isRandomSecretCommand(os.Args) {
		if err := runRandomSecretCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isEventBusCredentialsCommand(os.Args) {
		if err := runEventBusCredentialsCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isServiceDeploymentsCommand(os.Args) {
		if err := runServiceDeploymentsCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if !isInitCommand(os.Args) {
		printInitError(os.Stderr, fmt.Errorf("unknown command: use bootstrap, host, placement, init, user, random-secret, eventbus-credentials, keys, pki, or service-deployments"))
		os.Exit(2)
	}
	if err := runInitCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printInitError(os.Stderr, err)
		os.Exit(1)
	}
}
