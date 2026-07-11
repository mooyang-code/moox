package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/collector/internal/readiness"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "readiness-lock" {
		readinessLockMain(os.Args[2:])
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

func readinessLockMain(args []string) {
	flags := flag.NewFlagSet("readiness-lock", flag.ExitOnError)
	root := flags.String("markets-dir", filepath.Join("config", "markets"), "market manifest directory")
	output := flags.String("output", "market-readiness-lock.json", "output lock path")
	_ = flags.Parse(args)
	lock, err := readiness.Generate(*root)
	if err == nil {
		err = readiness.Write(*output, lock)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
