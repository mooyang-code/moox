package main

import (
	"fmt"
	"os"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	marketdata "github.com/mooyang-code/moox/modules/collector/internal/serverless/market_data"
)

var Version string

func main() {
	if os.Getenv("MOOX_SPACE_ID") == "crypto_market" {
		panic("market_data SCF does not accept crypto_market")
	}
	cfg := runtimeapp.DefaultConfig()
	if Version != "" {
		cfg.System.Version = Version
	}
	if _, err := runtimeapp.LoadConfigs(cfg); err != nil {
		panic(fmt.Sprintf("load market data SCF config: %v", err))
	}
	marketdata.RegisterCloudFunction()
}
