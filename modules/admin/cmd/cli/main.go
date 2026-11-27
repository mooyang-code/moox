package main

import (
	"fmt"
	"os"
)

func main() {
	if isHostBundleCommand(os.Args) {
		if err := runHostBundleCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if isBootstrapCommand(os.Args) {
		if err := runBootstrapCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
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
	if isEventBusCredentialsCommand(os.Args) {
		if err := runEventBusCredentialsCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	printInitError(os.Stderr, fmt.Errorf("unknown command: use bootstrap, host-bundle, user, eventbus-credentials, keys"))
	os.Exit(2)
}
