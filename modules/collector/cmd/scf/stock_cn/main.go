package main

import (
	"fmt"
	"os"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	stockcn "github.com/mooyang-code/moox/modules/collector/internal/serverless/stock_cn"
)

var Version string

func main() {
	if os.Getenv("MOOX_SPACE_ID") != "stock_cn" {
		panic("MOOX_SPACE_ID must be stock_cn")
	}
	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
	}
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		panic(fmt.Sprintf("load stock_cn SCF config: %v", err))
	}
	stockcn.RegisterCloudFunction()
}
