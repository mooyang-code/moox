package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/archive/internal/bootstrap"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

var Version = "dev"
var BuildTime = "unknown"
var GitCommit = "unknown"

func main() {
	configPath := "config/app.yaml"
	flag.StringVar(&configPath, "config", configPath, "archive config path")
	flag.Parse()
	if err := bootstrap.RunFromConfig(context.Background(), configPath, Version, GitCommit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
