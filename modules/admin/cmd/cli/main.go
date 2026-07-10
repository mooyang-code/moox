package main

import (
	"fmt"
	"os"
)

func main() {
	if isEventBusCredentialsCommand(os.Args) {
		if err := runEventBusCredentialsCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if !isInitCommand(os.Args) {
		printInitError(os.Stderr, fmt.Errorf("unknown command: use init"))
		os.Exit(2)
	}
	if err := runInitCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printInitError(os.Stderr, err)
		os.Exit(1)
	}
}
