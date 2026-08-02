package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/serverless"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-log-cls"
)

var Version string

var registerCloudFunction = serverless.RegisterCloudFunction

func main() {
	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
		runtimeapp.UpdateNodeInfo("", Version)
	}
	if err := startProductionRuntime(trpc.BackgroundContext(), cfg); err != nil {
		panic("failed to initialize short-lived collector SCF: " + err.Error())
	}
}

// startProductionRuntime intentionally blocks in cloudfunction.Start. A short
// SCF process must not start a resident task runner, keepalive loop or timer.
func startProductionRuntime(_ context.Context, cfg *runtimeapp.AppConfig) error {
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) == "" {
		return fmt.Errorf("MOOX_SPACE_ID is required")
	}
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		return err
	}
	registerCloudFunction()
	return nil
}
