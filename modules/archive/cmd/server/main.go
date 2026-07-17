package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/archive/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
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
	if err := bootstrap.RunFromConfig(trpc.BackgroundContext(), configPath, Version, GitCommit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
