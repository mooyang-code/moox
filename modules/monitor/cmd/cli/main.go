package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: moox-monitor-cli <command>")
	}
	switch args[0] {
	case "init":
		return runInit(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
