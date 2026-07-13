package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/archive/internal/bootstrap"
	"trpc.group/trpc-go/trpc-go"
)

var Version = "dev"
var BuildTime = "unknown"
var GitCommit = "unknown"

func main() {
	configPath := "config/app.yaml"
	frameworkConfigPath := "config/trpc_go.yaml"
	flag.StringVar(&configPath, "config", configPath, "archive config path")
	flag.StringVar(&frameworkConfigPath, "conf", frameworkConfigPath, "tRPC framework config path")
	flag.Parse()
	trpc.ServerConfigPath = frameworkConfigPath
	if err := bootstrap.RunFromConfig(context.Background(), configPath, Version, GitCommit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
