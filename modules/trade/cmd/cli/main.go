package main

import (
	"fmt"
	"os"
)

func main() {
	switch {
	case isInitCommand(os.Args):
		if err := runInitCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printInitError(os.Stderr, err)
			os.Exit(1)
		}
	case isEventBusCheckCommand(os.Args):
		if err := runEventBusCheckCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			printCommandError(os.Stderr, "eventbus_check_failed", err)
			os.Exit(1)
		}
	default:
		printInitError(os.Stderr, fmt.Errorf("unknown command: use init or eventbus-check"))
		os.Exit(2)
	}
}

func printCommandError(stderr *os.File, code string, err error) {
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintf(stderr, "{\"error\":%q,\"message\":%q}\n", code, err.Error())
}
