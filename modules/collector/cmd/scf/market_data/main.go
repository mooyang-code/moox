package main

import (
	"fmt"
	"os"
	"strings"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	marketdata "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
)

var Version string

func main() {
	if strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")) == "" {
		panic("MOOX_SPACE_ID is required")
	}
	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
	}
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		panic(fmt.Sprintf("load market_data SCF config: %v", err))
	}
	marketdata.RegisterCloudFunction()
}
