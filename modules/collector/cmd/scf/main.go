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
var initializeSCFTRPC = func() { _ = trpc.NewServer() }

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
	// cloudfunction.Start does not initialize tRPC's config/plugins. Do that
	// explicitly so the packaged CLS-only logger replaces the console writer
	// before any handler emits a log line.
	initializeSCFTRPC()
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		return err
	}
	registerCloudFunction()
	return nil
}
